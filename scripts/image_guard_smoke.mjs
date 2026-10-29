import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// A real Worker must own maximum-size image request allocations.
// Deliberately exceed a small Worker soft limit without provoking an OS OOM.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin/vista')
const state = mkdtempSync(join(process.env.VISTA_TEST_TMPDIR || process.cwd(), 'vista-image-guard-'))
prepareVistaTestConfig(state)
const image = Buffer.alloc(50 << 20)
Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=', 'base64').copy(image)
const hash = createHash('sha256').update(image).digest('hex')
const requests = []
const provider = createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/v1/models') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end('{"data":[{"id":"gpt-6-luna"}]}')
    return
  }
  if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
    response.writeHead(405).end()
    return
  }
  const entry = { image: false }
  requests.push(entry)
  let tail = ''
  // Backpressure retains the outgoing request while a real RSS sample arrives.
  if (requests.length === 1) await new Promise(resolve => setTimeout(resolve, 3000))
  try {
    for await (const chunk of request) {
      const fragment = tail + chunk.toString()
      entry.image ||= fragment.includes('"type":"image_url"')
      tail = fragment.slice(-64)
    }
  } catch { return } // The guarded Worker can die during request transmission.
  if (entry.image) await new Promise(resolve => setTimeout(resolve, 5000))
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end('data: {"choices":[{"delta":{"content":"Service recovered."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const config = join(state, 'vista')
writeFileSync(join(config, 'capacity.json'), JSON.stringify({ budgetMiB: 1024, workerMiB: 128, concurrentRuns: 1 }), { mode: 0o600 })
writeFileSync(join(config, 'models.json'), JSON.stringify({ default: 'image', models: [{ key: 'image', provider: 'openai-chat', providerId: 'custom', authentication: 'bearer', id: 'gpt-6-luna', apiKey: 'fixture-key', baseUrl: `http://127.0.0.1:${provider.address().port}/v1`, inputModalities: ['text', 'image'] }] }), { mode: 0o600 })
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, APPDATA: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
let started = false
try {
  const address = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(address).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } })
    if (!response.ok) throw new Error(`${path}: HTTP ${response.status} ${await response.text()}`)
    if (response.status === 204) return null
    return response.json()
  }
  await api('/agent-settings', { method: 'PUT', body: JSON.stringify({ automaticTitles: false }) })
  const session = await api('/sessions', { method: 'POST', body: '{}' })
  const serverPID = JSON.parse(readFileSync(join(config, 'state/instance.json'), 'utf8')).pid
  const before = await api('/status')
  const workerPID = before.workerMetrics[0].pid
  const ref = await api(`/sessions/${session.id}/resources?name=max.png`, { method: 'POST', headers: { 'Content-Type': 'image/png' }, body: image })
  if (ref.hash !== hash || ref.bytes !== image.length) throw new Error('maximum-size original was not retained unchanged')
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Inspect the originals', images: [ref, ref] }) })
  let recovered
  for (let i = 0; i < 300; i++) {
    const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
    const status = await api('/status')
    if (events.some(event => event.kind === 'run_finished' && event.data.runId === run.runId && event.data.status === 'interrupted') && status.workerMetrics[0]?.pid !== workerPID && status.sessions.workers === 1 && !status.sessions.running) {
      recovered = status
      break
    }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!recovered) {
    console.error(JSON.stringify({ status: await api('/status'), events: await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`), requests }))
    console.error(readFileSync(join(config, 'state/server.log'), 'utf8').slice(-4000))
    throw new Error('Worker did not interrupt and recover after exceeding its memory limit')
  }
  if (JSON.parse(readFileSync(join(config, 'state/instance.json'), 'utf8')).pid !== serverPID) throw new Error('image work terminated the main service')
  const saved = await fetch(endpoint + `/api/sessions/${session.id}/resources/${hash}`)
  const digest = createHash('sha256')
  for await (const chunk of saved.body) digest.update(chunk)
  if (!saved.ok || digest.digest('hex') !== hash) throw new Error('guard interruption damaged the original')
  const count = requests.length
  const next = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Continue with text' }) })
  let finished = false
  for (let i = 0; i < 200; i++) {
    const events = await api(`/sessions/${session.id}/event-page?after=${next.event.sequence}`)
    if (events.some(event => event.kind === 'run_finished' && event.data.runId === next.runId && event.data.status === 'succeeded')) { finished = true; break }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!finished || requests.length !== count + 1 || requests.at(-1).image) throw new Error('new work failed or the interrupted image was replayed')
  console.log(JSON.stringify({ originalBytes: ref.bytes, imagesPerRequest: 2, mainServiceSurvived: true, workerReplaced: true, originalIntact: true, nextTextRunSucceeded: true, automaticMediaReplay: false }))
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  provider.closeAllConnections()
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}
