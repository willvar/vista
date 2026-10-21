import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { existsSync, mkdtempSync, rmSync, readFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only manual compaction and earlier-message recall smoke.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-history-'))
let calls = 0
let summaries = 0
let mockError = ''
const provider = createServer((request, response) => {
  handleProvider(request, response).catch(error => {
    mockError = error.message
    response.destroy()
  })
})
async function handleProvider(request, response) {
  if (request.url !== '/v1/chat/completions') throw new Error(request.url)
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  function sendText(content) {
    const chunk = JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })
    response.end(`data: ${chunk}\n\ndata: [DONE]\n\n`)
  }
  function sendTool(id, name, args) {
    const chunk = JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id, type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, finish_reason: 'tool_calls' }] })
    response.end(`data: ${chunk}\n\ndata: [DONE]\n\n`)
  }
  if (String(body.messages?.[0]?.content).startsWith('Compress the completed execution transcript')) {
    summaries++
    if (body.tools?.length || summaries === 1 && !JSON.stringify(body.messages).includes('EARLY CONSTRAINT')) throw new Error('summary model request did not include earlier history without tools')
    return sendText('Summary: keep output concise.')
  }
  calls++
  if (calls <= 2) return sendText('Noted.')
  if (calls === 3) {
    if (!JSON.stringify(body.messages).includes('Summary: keep output concise.')) throw new Error('persisted summary was not loaded into the Agent context')
    return sendTool('history_index_call', 'history_index', {})
  }
  if (calls === 4) {
    const index = JSON.parse(body.messages.at(-1).content)
    const old = index.messages.find(message => message.preview.includes('EARLY CONSTRAINT'))
    if (!old) throw new Error(`early message not indexed: ${JSON.stringify(index)}`)
    return sendTool('history_read_call', 'read_history', { sequence: old.sequence })
  }
  if (calls === 5) {
    const page = JSON.parse(body.messages.at(-1).content)
    if (!page.content.includes('EARLY CONSTRAINT')) throw new Error('early constraint was not recalled')
    return sendText('I found the early constraint.')
  }
  throw new Error('unexpected model call')
}
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'fake-key', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.json()
  }
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'history recall' }) })
  async function send(content) {
    const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content }) })
    for (let i = 0; i < 200; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
      const result = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
      if (result) {
        if (result.data.status !== 'succeeded') throw new Error(`run failed: ${JSON.stringify(result.data)}; mock: ${mockError}`)
        return { result, events }
      }
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    throw new Error('run did not finish')
  }
  await send('EARLY CONSTRAINT: keep output concise. ' + 'a'.repeat(6900))
  await send('SECOND TURN: ' + 'b'.repeat(6900))
  const before = await api(`/sessions/${session.id}/messages`)
  const compaction = await api(`/sessions/${session.id}/compact`, { method: 'POST', body: '{}' })
  let compacted = false
  for (let i = 0; i < 200; i++) {
    const status = await api(`/sessions/${session.id}/context`)
    if (status?.runId === compaction.runId && status.phase !== 'compacting') {
      if (status.phase !== 'compacted') throw new Error(`compaction failed: ${JSON.stringify(status)}; mock: ${mockError}`)
      compacted = true
      break
    }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!compacted) throw new Error('manual compaction did not finish')
  if (JSON.stringify(before) !== JSON.stringify(await api(`/sessions/${session.id}/messages`))) throw new Error('compaction modified visible history')
  const events = await api(`/sessions/${session.id}/event-page?after=${compaction.event.sequence}`)
  const checkpoint = events.find(event => event.kind === 'context_notice' && event.data.checkpoint?.path)?.data.checkpoint
  if (!checkpoint || !existsSync(checkpoint.path)) throw new Error('compaction checkpoint was not persisted')
  const third = await send('What was my early constraint?')
  if (third.result.data.message?.content !== 'I found the early constraint.' || calls !== 5 || summaries === 0 || mockError) throw new Error(`history summary and recall failed: ${JSON.stringify(third.result.data)}, calls=${calls}, summaries=${summaries}; mock: ${mockError}`)
  console.log('Manual compaction checkpoint and original history recall passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
