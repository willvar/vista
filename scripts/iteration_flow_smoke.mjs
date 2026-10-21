import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import assert from 'node:assert/strict'

// Real CLI/Server/Worker and SQLite, entirely local model responses.
const binary = process.env.VISTA_BIN
if (!binary) throw new Error('VISTA_BIN is required')
const parent = resolve('.vista/test-state')
mkdirSync(parent, { recursive: true })
const root = mkdtempSync(join(parent, 'iteration-flow-'))
const config = join(root, 'vista')
mkdirSync(config, { mode: 0o700 })
const models = ['gpt-6-luna', 'gpt-sol-6.1']
const requests = []
let releaseHold, held = false
const hold = new Promise(resolve => { releaseHold = resolve })
const upstream = createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/v1/models') {
    response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: models.map(id => ({ id })) })); return
  }
  if (request.method !== 'POST' || request.url !== '/v1/chat/completions') { response.writeHead(404).end(); return }
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  const title = String(body.messages?.[0]?.content || '').startsWith('Write a short, specific title')
  if (!title) {
    requests.push(body)
    if (body.messages.at(-1)?.content === 'HOLD_INITIAL') { held = true; await hold }
  }
  if (response.destroyed) return
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.write(`data: ${JSON.stringify({ choices: [{ delta: { reasoning_content: 'thinking' } }] })}\n\n`)
  await new Promise(done => setTimeout(done, 15))
  response.end(`data: ${JSON.stringify({ choices: [{ delta: { content: title ? 'Fixture title' : 'Fixture answer' }, finish_reason: 'stop' }], usage: { prompt_tokens: 2000, completion_tokens: 8 } })}\n\ndata: [DONE]\n\n`)
})
await new Promise(done => upstream.listen(0, '127.0.0.1', done))
writeFileSync(join(config, 'models.json'), JSON.stringify({ default: 'fixture-0', models: models.map((id, i) => ({ key: `fixture-${i}`, name: 'Fixture', id, provider: 'openai-chat', apiKey: 'fixture-key', context: 128000, baseUrl: `http://127.0.0.1:${upstream.address().port}/v1` })) }), { mode: 0o600 })
prepareVistaTestConfig(root)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: root, APPDATA: root, TMPDIR: parent, VISTA_NO_BROWSER: '1' }
const execute = promisify(execFile)
let started = false, info
async function cli(action) { await execute(binary, [action], { env, timeout: 20000 }) }
function loadInfo() { info = JSON.parse(readFileSync(join(config, 'state/instance.json'), 'utf8')) }
async function api(path, method = 'GET', body) {
  const response = await fetch(info.address + '/api' + path, { method, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000), headers: { 'Content-Type': 'application/json' } })
  if (!response.ok) throw new Error(`Fixture API ${path} returned ${response.status}: ${await response.text()}`)
  const text = await response.text(); return text ? JSON.parse(text) : null
}
async function until(check, label) {
  const end = Date.now() + 15000
  while (Date.now() < end) { const result = await check(); if (result) return result; await new Promise(done => setTimeout(done, 30)) }
  throw new Error(`Timed out: ${label}`)
}
async function finish(id, runId) {
  return until(async () => {
    const messages = await api(`/sessions/${id}/messages`)
    return messages.find(message => message.runId === runId && message.role === 'assistant')
  }, 'reply')
}
async function sessionState(id) { return (await api('/sessions')).find(session => session.id === id) }
try {
  await cli('start'); started = true; loadInfo()
  const prefs = await api('/preferences')
  assert.equal(prefs.feedbackInvitationSeen, false)
  const catalog = await api('/models')
  const options = catalog.models.find(model => model.key === 'fixture-0').reasoningOptions || []
  const manualEffort = options[0] || ''
  await api('/preferences', 'PATCH', { lastMode: 'manual', manualModelKey: 'fixture-0', reasoningByModelId: { [models[0]]: manualEffort }, feedbackInvitationSeen: true })
  const manual = await api('/sessions', 'POST', {})
  assert.equal(manual.manualModelKey, 'fixture-0')
  const first = await api(`/sessions/${manual.id}/messages`, 'POST', { content: 'Hi', selectionMode: 'manual', modelKey: 'fixture-0', reasoningEffort: manualEffort })
  const answer = await finish(manual.id, first.runId)
  for (const value of ['up', 'down', null]) {
    await api(`/sessions/${manual.id}/messages/${answer.id}/feedback`, 'PUT', { value })
    const feedback = await api(`/sessions/${manual.id}/feedback`)
    assert.equal(feedback.feedback[answer.id] || null, value)
  }
  assert.equal(await api(`/sessions/${manual.id}/context`), null)

  const selected = []
  for (let i = 0; i < 2; i++) {
    const session = await api('/sessions', 'POST', {})
    const sent = await api(`/sessions/${session.id}/messages`, 'POST', { content: 'Automatic Hi', selectionMode: 'auto', selectionTier: 'speed' })
    const reply = await finish(session.id, sent.runId)
    selected.push(reply.modelId)
    const actual = requests.at(-1)
    const effort = catalog.models.find(model => model.id === actual.model).reasoningOptions?.at(-1)
    if (effort) assert.equal(actual.reasoning_effort, effort, 'automatic mode must use highest effort')
  }
  assert.equal(new Set(selected).size, 2, 'cold exploration must cover unfamiliar IDs')
  assert.equal((await api('/preferences')).manualModelKey, 'fixture-0')
  await api(`/sessions/${manual.id}/selection`, 'PATCH', { selectionMode: 'auto', selectionTier: 'quality' })
  const restored = await api(`/sessions/${manual.id}/selection`, 'PATCH', { selectionMode: 'manual' })
  assert.equal(restored.manualModelKey, 'fixture-0')
  assert.equal(restored.manualReasoningEffort, manualEffort)

  const queue = await api('/sessions', 'POST', {})
  await api(`/sessions/${queue.id}/messages`, 'POST', { content: 'HOLD_INITIAL', modelKey: 'fixture-0' })
  await until(() => held, 'held provider request')
  const pending = []
  for (let i = 0; i < 12; i++) {
    const queued = await api(`/sessions/${queue.id}/messages`, 'POST', { content: `Pending ${i}`, mode: 'later', modelKey: 'fixture-0' })
    pending.push(queued.input)
  }
  await api(`/sessions/${queue.id}/inputs/${pending[0].id}`, 'PATCH', { content: 'Edited pending', mode: 'now' })
  await api(`/sessions/${queue.id}/inputs/${pending[1].id}`, 'DELETE')
  const withdrawn = await api(`/sessions/${queue.id}/inputs/${pending[2].id}/withdraw`, 'POST', {})
  assert.equal(withdrawn.input.content, 'Pending 2')
  await api(`/sessions/${queue.id}/cancel`, 'POST', {})
  releaseHold()
  await until(async () => !(await sessionState(queue.id)).executing, 'cancel')
  const preserved = await api(`/sessions/${queue.id}/inputs`)
  assert.equal(preserved.filter(input => input.status === 'paused').length, 10, 'stop must preserve queue')
  const beforeMessages = await api(`/sessions/${queue.id}/messages`)
  assert.ok(!beforeMessages.some(message => message.content === 'Edited pending'), 'pending input entered history before delivery')
  await api(`/sessions/${queue.id}/inputs/resume`, 'POST', {})
  await until(async () => {
    const messages = await api(`/sessions/${queue.id}/messages`)
    return messages.filter(message => message.role === 'user').length === 11 && messages.some(message => message.content === 'Edited pending') && !(await sessionState(queue.id)).executing
  }, 'resume queue')
  const finalMessages = await api(`/sessions/${queue.id}/messages`)
  assert.equal(finalMessages.filter(message => message.content === 'Edited pending').length, 1)
  assert.ok(!finalMessages.some(message => message.content === 'Pending 1' || message.content === 'Pending 2'))
  await cli('stop'); started = false
  await cli('start'); started = true; loadInfo()
  const persisted = await api('/preferences')
  assert.equal(persisted.userId, prefs.userId)
  assert.equal(persisted.feedbackInvitationSeen, true)
  assert.equal(persisted.reasoningByModelId[models[0]], manualEffort)
  assert.equal(await api(`/sessions/${manual.id}/context`), null)
  console.log(JSON.stringify({ result: 'passed', checks: ['manual/automatic independence', 'highest reasoning', 'cold model exploration', 'editable feedback', 'no proactive compaction', '12 queued inputs', 'edit/delete/withdraw', 'cancel preserves queue', 'resume without duplicates', 'restart persistence'] }))
} finally {
  releaseHold()
  if (started) await cli('stop').catch(() => {})
  await new Promise(done => { upstream.closeAllConnections(); upstream.close(done) })
  rmSync(root, { recursive: true, force: true })
}
