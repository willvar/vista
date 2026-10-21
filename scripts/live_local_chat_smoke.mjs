import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// End-to-end with an actual OpenAI-compatible local inference server.
// The endpoint and model are explicit so this never contacts a hosted API.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const baseURL = process.env.VISTA_LOCAL_BASE_URL
const modelID = process.env.VISTA_LOCAL_MODEL
if (!baseURL || !modelID) throw new Error('VISTA_LOCAL_BASE_URL and VISTA_LOCAL_MODEL are required')
const local = new URL(baseURL)
if (local.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(local.hostname)) throw new Error('local model must use loopback HTTP')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-live-local-'))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'live', models: [{ key: 'live', provider: 'openai-chat', providerId: 'llamacpp', id: modelID, baseUrl: baseURL, apiKey: 'local-only', tools: false, context: 4096 }] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
let started = false
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  const api = async (path, options = {}) => {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.json()
  }
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'real local model' }) })
  const send = async content => {
    const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content }) })
    for (let i = 0; i < 400; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
      const finished = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
      if (finished) return { finished, events }
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    throw new Error('local model run did not finish')
  }
  const first = await send('Reply with the single word READY.')
  const second = await send('Reply with the single word AGAIN.')
  for (const [index, turn] of [[1, first], [2, second]]) {
    if (turn.finished.data.status !== 'succeeded' || !turn.finished.data.message?.content?.trim()) throw new Error(`local turn ${index} failed: ${JSON.stringify(turn.finished.data)}`)
    if (!turn.events.some(event => event.kind === 'model_delta' && event.data.runId === turn.finished.data.runId)) throw new Error(`local turn ${index} did not stream text`)
    if (!turn.events.some(event => event.kind === 'model_result' && event.data.runId === turn.finished.data.runId)) throw new Error(`local turn ${index} lacked a model result`)
  }
  const status = await api('/status')
  if (status.sessions.open !== 1 || status.sessions.workers !== 1 || status.sessions.running !== 0) throw new Error(`local Session state: ${JSON.stringify(status.sessions)}`)
  console.log(JSON.stringify({ model: modelID, first: first.finished.data.message.content, second: second.finished.data.message.content, sessions: status.sessions }))
} catch (error) {
  try { console.error(readFileSync(join(vistaConfig, 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  rmSync(state, { recursive: true, force: true })
}
