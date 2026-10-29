import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// CLI → Server → Worker → mock model → persisted events, without live keys or a browser.
const binary = process.env.VISTA_BIN
if (!binary) { console.error('VISTA_BIN is required'); process.exit(1) }
const parent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'tool-continuation-'))
const readRules = projectRulesFixture(state)
const config = join(state, 'vista')
mkdirSync(config, { mode: 0o700 })
const input = join(state, 'input.txt')
const compactContext = Number(process.env.VISTA_COMPACT_CONTEXT || 0)
const content = compactContext ? 'continuation fixture'.repeat(64) : 'continuation fixture'
const answer = 'Completed 32 reads.'
const modelId = 'tool-continuation-test'
writeFileSync(input, content, { mode: 0o600 })
let calls = 0, summaryCalls = 0, mockError = ''
function check(condition, message) { if (!condition) throw new Error(message) }
const provider = createServer(async (request, response) => {
  try {
    if (request.method === 'GET' && request.url === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ data: [{ id: modelId }] }))
      return
    }
    if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
      response.writeHead(404).end()
      return
    }
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    const system = String(body.messages?.[0]?.content || '')
    const title = system.startsWith('Write a short, specific title')
    const summarizing = /^(Summarize|Compact|Compress)\b/i.test(system)
    if (!title && !summarizing && readRules(body, response)) return
    let delta, finish = 'stop'
    if (title) delta = { content: 'Tool continuation' }
    else if (summarizing) { summaryCalls++; delta = { content: 'Completed earlier fixture reads successfully. Continue the remaining reads, then report completion.' } }
    else {
      calls++
      check(calls <= 22, 'unexpected extra model request')
      check(body.tools?.some(tool => tool.function?.name === 'read_file'), 'read_file tool missing')
      const results = body.messages.filter(message => message.role === 'tool')
      const expected = calls === 1 ? 0 : calls + 10
      if (!compactContext) check(results.length === expected, `model request ${calls}: expected ${expected} tool results, got ${results.length}`)
      else {
        check(results.length <= expected, 'compaction introduced extra tool results')
        const declared = new Set(body.messages.flatMap(message => (message.tool_calls || []).map(call => call.id)))
        for (const result of results) check(declared.has(result.tool_call_id), 'compaction orphaned a tool result')
      }
      const ids = new Set()
      for (const result of results) {
        check(!ids.has(result.tool_call_id), 'duplicate tool result in model context')
        ids.add(result.tool_call_id)
        const value = JSON.parse(result.content)
        check(!value.error && value.content === `1: ${content}`, 'tool result content missing or failed')
      }
      if (!compactContext) for (let id = 1; id <= expected; id++) check(ids.has(`read_${id}`), 'tool result ID missing from model context')
      if (calls === 22) delta = { content: answer }
      else {
        const first = calls === 1 ? 1 : calls + 11
        const count = calls === 1 ? 12 : 1
        delta = { tool_calls: Array.from({ length: count }, (_, index) => ({
          index, id: `read_${first + index}`, type: 'function',
          function: { name: 'read_file', arguments: JSON.stringify({ path: input }) },
        })) }
        finish = 'tool_calls'
      }
    }
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: finish }] })}\n\ndata: [DONE]\n\n`)
  } catch (error) {
    mockError = error.message
    response.writeHead(500)
    response.end('mock validation failed')
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
writeFileSync(join(config, 'models.json'), JSON.stringify({
  default: 'test', models: [{ key: 'test', provider: 'openai-chat', id: modelId,
    apiKey: 'test-key', context: compactContext || 128000, baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }],
}), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
const execute = promisify(execFile)
let started = false
async function cli(command) {
  try { await execute(binary, [command], { env, timeout: 15000 }) }
  catch { throw new Error(`fixture CLI ${command} failed`) }
}
try {
  started = true
  await cli('start')
  const info = JSON.parse(readFileSync(join(config, 'state', 'instance.json'), 'utf8'))
  async function api(path, options = {}) {
    const response = await fetch(info.address + '/api' + path, {
      ...options, signal: AbortSignal.timeout(5000),
      headers: { 'Content-Type': 'application/json' },
    })
    check(response.ok, `fixture API returned HTTP ${response.status}`)
    return response.status === 204 ? null : response.json()
  }
  const session = await api('/sessions', { method: 'POST', body: '{}' })
  const before = await api('/status')
  const workerPID = before.workerMetrics.find(worker => worker.sessionId === session.id)?.pid
  check(workerPID > 0, 'fixture Worker missing before run')
  const sent = await api(`/sessions/${session.id}/messages`, {
    method: 'POST', body: JSON.stringify({ content: 'Read the fixture 32 times, then report completion.', modelKey: 'test' }),
  })
  const events = []
  let cursor = sent.event.sequence, finished
  const deadline = Date.now() + 30000
  while (!finished && Date.now() < deadline) {
    const page = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
    if (page.length) cursor = page.at(-1).sequence
    events.push(...page.filter(event => event.data.runId === sent.runId))
    finished = events.find(event => event.kind === 'run_finished')
    if (!finished && page.length < 100) await new Promise(resolve => setTimeout(resolve, 25))
  }
  check(!mockError, mockError)
  check(finished?.data.status === 'succeeded', 'continuation run did not succeed')
  check(finished.data.message?.content === answer, 'final reply missing from run event')
  check(calls === 22, `expected 22 model requests, got ${calls}`)
  const tools = events.filter(event => event.kind === 'tool' && event.data.tool?.name === 'read_file')
  const began = tools.filter(event => event.data.status === 'started')
  const succeeded = tools.filter(event => event.data.status === 'succeeded')
  check(tools.length === 64 && began.length === 32 && succeeded.length === 32, 'expected 32 started and 32 succeeded tool events')
  for (const group of [began, succeeded]) {
    const ids = new Set(group.map(event => event.data.tool?.callId))
    check(ids.size === 32, 'duplicate persisted tool call IDs')
    for (let id = 1; id <= 32; id++) check(ids.has(`read_${id}`), 'persisted tool call ID missing')
    check(group.every(event => event.data.tool.name === 'read_file'), 'unexpected persisted tool name')
  }
  check(succeeded.every(event => JSON.parse(event.data.content).content === `1: ${content}`), 'persisted tool result content missing')
  const modelResults = events.filter(event => event.kind === 'model_result')
  check(modelResults.length === 22 + readRules.requests && modelResults.at(-1).data.turn === 22 + readRules.requests, 'model result sequence incomplete')
  const requests = events.filter(event => event.kind === 'model_request')
  check(requests.filter(event => event.data.purpose !== 'summary').length === 22 + readRules.requests, 'persisted execution model request count incorrect')
  if (compactContext) {
    const notices = events.filter(event => event.kind === 'context_notice')
    check(summaryCalls > 0 && notices.length > 0 && notices.length <= summaryCalls, 'compaction summary or persisted notice missing')
    check(requests.filter(event => event.data.purpose === 'summary').length === summaryCalls, 'compaction request count missing from activity')
    check(notices.every(event => event.data.summary?.length && event.data.turn > 0 && event.data.depth === 0), 'compaction notice lost its summary or run scope')
  }
  const messages = await api(`/sessions/${session.id}/messages`)
  check(messages.some(message => message.runId === sent.runId && message.role === 'assistant' && message.content === answer), 'final reply missing from message history')
  const after = await api('/status')
  check(after.sessions.running === 0 && after.sessions.workers === 1 && after.sessions.unavailable === 0, 'Worker unavailable after continuation')
  check(after.workerMetrics.some(worker => worker.sessionId === session.id && worker.pid === workerPID), 'Worker was replaced during continuation')
  console.log(JSON.stringify({ modelRequests: calls, ruleRequests: readRules.requests, compactionRequests: summaryCalls, toolsStarted: began.length, toolsSucceeded: succeeded.length, persistedModelResults: modelResults.length, availableWorkers: after.sessions.workers }))
} catch (error) {
  console.error((mockError || error.message).replaceAll(state, '<fixture>').replaceAll(binary, '<binary>').replaceAll(process.cwd(), '<repo>'))
  process.exitCode = 1
} finally {
  if (started) try { await cli('stop') } catch { console.error('fixture CLI stop failed'); process.exitCode = 1 }
  provider.closeAllConnections()
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
