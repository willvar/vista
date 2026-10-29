import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { existsSync, mkdtempSync, readFileSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only shell artifact lifecycle; no browser or live Provider key.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-artifact-'))
const readRules = projectRulesFixture(state)
let calls = 0
const provider = createServer(async (request, response) => {
  if (request.url === '/v1/models') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ data: [{ id: 'artifact-fixture' }] }))
    return
  }
  if (request.url !== '/v1/chat/completions') throw new Error(request.url)
  let raw = ''
  for await (const chunk of request) raw += chunk
  if (readRules(JSON.parse(raw), response)) return
  calls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  if (calls === 1) {
    const args = JSON.stringify({ directory: state, command: "printf 'hello shell artifact'; exit 7", timeoutSeconds: 10 })
    const chunk = JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'call_shell', type: 'function', function: { name: 'shell', arguments: args } }] }, finish_reason: 'tool_calls' }] })
    response.end(`data: ${chunk}\n\ndata: [DONE]\n\n`)
  } else if (calls === 2) {
    response.end('data: {"choices":[{"delta":{"content":"Shell exited with code 7."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
  } else throw new Error('shell was replayed')
})
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
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'shell artifact' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'run shell' }) })
  let artifact
  for (let i = 0; i < 200; i++) {
    const artifacts = await api(`/sessions/${session.id}/artifacts`)
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    if (artifacts[0]?.status === 'failed' && events.some(event => event.kind === 'run_finished' && event.data.runId === run.runId && event.data.status === 'succeeded')) { artifact = artifacts[0]; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!artifact || !artifact.complete || !artifact.error.includes('exit code 7') || !existsSync(artifact.outputPath)) throw new Error('failed shell artifact was not retained with its complete output and exit status')
  const events = await api(`/sessions/${session.id}/event-page?after=0`)
  const start = events.find(event => event.kind === 'artifact' && event.data.id === artifact.id && event.data.status === 'started')
  const finished = events.find(event => event.kind === 'artifact' && event.data.id === artifact.id && event.data.status === 'failed')
  if (!start || !finished || start.sequence >= finished.sequence) throw new Error('artifact start and completion were not ordered')
  if (!events.some(event => event.kind === 'tool' && event.data.status === 'failed' && event.data.tool?.name === 'shell')) throw new Error('nonzero shell exit was not recorded as a failed tool')
  const page = await api(`/sessions/${session.id}/artifacts/${artifact.id}/output?offset=0`)
  if (page.content !== 'hello shell artifact' || page.nextOffset !== artifact.bytes) throw new Error(`artifact page incorrect: ${JSON.stringify(page)}`)
  const other = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'other session' }) })
  const foreign = await fetch(endpoint + `/api/sessions/${other.id}/artifacts/${artifact.id}/output`, {})
  if (foreign.status !== 404) throw new Error('artifact was readable from another Session')
  const path = artifact.outputPath
  const response = await fetch(endpoint + `/api/sessions/${session.id}`, { method: 'DELETE' })
  if (response.status !== 204 || existsSync(path)) throw new Error('Session delete left its shell output behind')
  if (calls !== 2) throw new Error('unexpected model calls')
  console.log('Shell artifact lifecycle passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
