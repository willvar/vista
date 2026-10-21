import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, execFileSync } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// Pinned catalog route and credential isolation smoke; no browser or live key.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-catalog-'))
let calls = 0
const provider = createServer(async (request, response) => {
  if (request.url !== '/v1/chat/completions' || request.headers.authorization !== 'Bearer private-catalog-key') throw new Error(`wrong catalog route or key: ${request.url}`)
  let raw = ''
  for await (const chunk of request) raw += chunk
  if (JSON.parse(raw).model !== 'deepseek-flash') throw new Error('wrong catalog model')
  calls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end('data: {"choices":[{"delta":{"content":"Catalog route works."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'deepseek', models: [{ key: 'deepseek', provider: 'catalog', providerId: 'deepseek', id: 'deepseek-flash', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', DEEPSEEK_API_KEY: 'private-catalog-key' }
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
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'catalog test' }) })
  const serverPid = JSON.parse(readFileSync(join(vistaConfig, 'state', 'instance.json'), 'utf8')).pid
  const workerPid = Number(execFileSync('ps', ['-o', 'pid=', '--ppid', String(serverPid)], { encoding: 'utf8' }).trim().split(/\s+/)[0])
  if (!workerPid) throw new Error('Worker was not started')
  if (readFileSync(`/proc/${workerPid}/environ`, 'utf8').includes('DEEPSEEK_API_KEY')) throw new Error('catalog key leaked into Worker environment')
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'hello' }) })
  let finished
  for (let i = 0; i < 200; i++) {
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    finished = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
    if (finished) {
      if (!events.some(event => event.kind === 'model_request' && event.data.provider === 'deepseek' && event.data.id === 'deepseek-flash')) throw new Error('catalog attribution missing')
      const fragment = events.find(event => event.kind === 'model_delta' && event.data.runId === run.runId)
      if (!fragment || fragment.data.content !== 'Catalog route works.' || fragment.sequence >= finished.sequence) throw new Error('model text was not persisted before completion')
      const result = events.find(event => event.kind === 'model_result' && event.data.runId === run.runId)
      if (!result || result.data.modelResult?.finishReason !== 'stop' || result.data.modelResult?.usage !== undefined || result.sequence >= finished.sequence) throw new Error('model finish reason or absent usage was not preserved')
      break
    }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (finished?.data.status !== 'succeeded' || finished.data.message?.content !== 'Catalog route works.' || calls !== 1) throw new Error(`catalog run failed: ${JSON.stringify(finished?.data)}, calls=${calls}`)
  console.log('Catalog route and credential isolation passed')
} catch (error) {
  try { console.error(readFileSync(join(vistaConfig, 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
