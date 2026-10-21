import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// Real Server/Worker/SQLite reactive and manual compaction; local fixture only.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-long-context-'))
const requests = []
let summaries = 0
let forceOverflow = false
let forceUnknownFailure = false
const provider = createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/v1/models') { response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'long-test' }] })); return }
  if (request.method !== 'POST' || request.url !== '/v1/chat/completions') { response.writeHead(404).end(); return }
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  const isSummary = String(body.messages?.[0]?.content).startsWith('Compress the completed execution transcript')
  const isTitle = String(body.messages?.[0]?.content).startsWith('Write a short, specific title')
  if (isSummary) summaries++
  else if (!isTitle) requests.push(body)
  if (!isSummary && !isTitle && forceOverflow) { forceOverflow = false; response.writeHead(503, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: { message: 'all pool attempts failed; last: fixture: context_length_exceeded' } })); return }
  if (!isSummary && !isTitle && forceUnknownFailure) { forceUnknownFailure = false; response.writeHead(400, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: { type: 'invalid_request_error', message: 'unrecognized parameter' } })); return }
  const content = isSummary ? 'Summary retained.' : 'Noted.'
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`)
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'long', models: [{ key: 'long', provider: 'openai-chat', id: 'long-test', apiKey: 'fake-key', context: 128000, baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
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
  async function send(session, content, expectedStatus = 'succeeded') {
    const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content }) })
    for (let i = 0; i < 200; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
      const finished = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
      if (finished) {
        if (finished.data.status !== expectedStatus) throw new Error(`run failed: ${JSON.stringify(finished.data)}`)
        return events
      }
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    throw new Error('run did not finish')
  }
  const plain = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'long plain history' }) })
  for (let i = 0; i < 7; i++) await send(plain, `${i === 0 ? 'EARLY_MARKER' : `turn ${i}`} ${'a'.repeat(6900)}`)
  if (summaries !== 0 || !JSON.stringify(requests.at(-1).messages).includes('EARLY_MARKER')) throw new Error('history was discarded or compacted without overflow evidence')
  forceOverflow = true
  const events = await send(plain, 'Recover and continue the same task')
  if (!events.some(event => event.kind === 'context_notice') || summaries === 0) throw new Error('explicit overflow did not compact')
  forceUnknownFailure = true
  await send(plain, 'Report an unknown failure', 'failed')
  const before = await api(`/sessions/${plain.id}/messages`)
  if (!before.at(-1).failure) throw new Error('failed reply lost its durable marker')
  await api(`/sessions/${plain.id}/compact`, { method: 'POST', body: '{}' })
  let compacted = false
  for (let i = 0; i < 200; i++) {
    const status = await api(`/sessions/${plain.id}/context`)
    if (status?.manual && status.phase !== 'compacting') {
      if (status.phase !== 'compacted') throw new Error(JSON.stringify(status))
      compacted = true; break
    }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!compacted) throw new Error('manual compaction did not finish')
  if (JSON.stringify(before) !== JSON.stringify(await api(`/sessions/${plain.id}/messages`))) throw new Error('manual compaction modified visible chat')
  await send(plain, 'Continue')
  if (!JSON.stringify(requests.at(-1).messages).includes('Summary retained.')) throw new Error('checkpoint not reused')
  console.log('Reactive recovery, manual compaction, durable context and full chat history passed')

} catch (error) {
  try { console.error(readFileSync(join(vistaConfig, 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
