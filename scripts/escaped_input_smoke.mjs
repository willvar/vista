import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// Exercises HTTP persistence and Worker IPC with a valid prompt that grows when JSON escaped.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-escaped-input-'))
const prompt = '<'.repeat(8 << 10)
let calls = 0
let promptCalls = 0
const provider = createServer(async (request, response) => {
  let body = ''
  for await (const chunk of request) body += chunk
  const messages = JSON.parse(body).messages || []
  if (messages.some(message => message.role === 'user' && message.content === prompt)) promptCalls++
  calls++
  await new Promise(resolve => setTimeout(resolve, 500))
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end('data: {"choices":[{"delta":{"content":"received"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'mcp.json'), JSON.stringify(['first', 'second'].map(name => ({ id: name, name, transport: 'http', endpoint: `http://127.0.0.1:${provider.address().port}/mcp`, token: '<'.repeat(3800) }))), { mode: 0o600 })

prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: '<'.repeat(4096), VISTA_OPENAI_MODEL: 'mock', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
for (const name of ['EXA', 'FIRECRAWL', 'PARALLEL', 'TAVILY']) env[`VISTA_WEBSEARCH_${name}_API_KEY`] = '<'.repeat(4096)
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
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'escaped input' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: prompt }) })
  if (run.message.content !== prompt || run.event.data.content !== prompt) throw new Error('initial message changed during persistence')
  const queued = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: prompt, mode: 'later' }) })
  if (queued.input.content !== prompt || queued.event.data.content !== prompt) throw new Error('queued message changed during persistence')
  let cursor = queued.event.sequence
  let finished = 0
  let delivered = false
  let replayed = false
  for (let attempt = 0; attempt < 200 && finished < 2; attempt++) {
    const events = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
    for (const event of events) {
      cursor = event.sequence
      if (event.kind === 'input_delivered' && event.data.id === queued.input.id) delivered = true
      if (event.kind === 'message' && event.data.role === 'user' && event.data.runId !== run.runId && event.data.content === prompt) replayed = true
      if (event.kind === 'run_finished') {
        if (event.data.status !== 'succeeded') throw new Error(`run failed: ${JSON.stringify(event.data)}`)
        finished++
      }
    }
    if (finished < 2) await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (promptCalls !== 2 || finished !== 2 || !delivered || !replayed) throw new Error(`expected two completed runs and a durable queued delivery, got ${promptCalls} prompt calls, ${calls} Provider calls, ${finished} finished runs, delivered=${delivered}, replayed=${replayed}`)
  console.log(JSON.stringify({ promptBytes: prompt.length, promptCalls, providerCalls: calls, finishedRuns: finished }))
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
