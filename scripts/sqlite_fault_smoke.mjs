import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, spawn } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// Hold SQLite's writer lock, then verify a rejected input has no model side effect.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const parent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'sqlite-fault-'))
let modelCalls = 0
const model = createServer(async (request, response) => {
  for await (const _ of request) {}
  modelCalls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end('data: {"choices":[{"delta":{"content":"Recovered."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
})
await new Promise(resolve => model.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'fake', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${model.address().port}/v1` }
let started = false
let locker
try {
  const address = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(address).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    const body = await response.text()
    return { status: response.status, body: response.ok && body ? JSON.parse(body) : body }
  }
  const created = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'SQLite fault' }) })
  if (created.status !== 201) throw new Error(`Session create failed: ${created.status}`)
  const session = created.body
  locker = spawn('sqlite3', ['-batch', join(state, 'vista', 'state', 'vista.db')], { stdio: ['pipe', 'pipe', 'pipe'] })
  const locked = new Promise((resolve, reject) => {
    locker.stdout.once('data', chunk => chunk.toString().includes('LOCKED') ? resolve() : reject(new Error(`unexpected sqlite output: ${chunk}`)))
    locker.once('error', reject)
    locker.once('exit', code => reject(new Error(`sqlite lock exited ${code}`)))
  })
  locker.stdin.write("BEGIN IMMEDIATE; SELECT 'LOCKED';\n")
  await Promise.race([locked, new Promise((_, reject) => setTimeout(() => reject(new Error('SQLite lock did not start')), 2000))])
  const rejected = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'run while locked' }) })
  // Selection persistence can reject before run admission (400); a later
  // input write failure is 500. Neither path may reach the model.
  if (![400, 500].includes(rejected.status) || modelCalls !== 0) throw new Error(`locked write was not rejected without side effects: ${rejected.status}, calls=${modelCalls}`)
  locker.stdin.end('COMMIT;\n')
  await new Promise((resolve, reject) => { locker.once('exit', code => code === 0 ? resolve() : reject(new Error(`sqlite exit ${code}`))) })
  locker = null
  const saved = await api(`/sessions/${session.id}/messages`)
  if (saved.status !== 200 || saved.body.length !== 0) throw new Error('failed input was persisted')
  const sent = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'run after unlock' }) })
  if (sent.status !== 202) throw new Error(`retry failed: ${sent.status} ${sent.body}`)
  let finished = false
  for (let attempt = 0; attempt < 200; attempt++) {
    const events = await api(`/sessions/${session.id}/event-page?after=${sent.body.event.sequence}`)
    const done = events.body.find(event => event.kind === 'run_finished' && event.data.runId === sent.body.runId)
    if (done) { finished = done.data.status === 'succeeded'; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!finished || modelCalls !== 1) throw new Error(`SQLite recovery failed: finished=${finished}, model calls=${modelCalls}`)
  console.log('SQLite writer lock rejected input without model side effects; retry recovered')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (locker) { locker.stdin.end('ROLLBACK;\n'); locker.kill() }
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => model.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
