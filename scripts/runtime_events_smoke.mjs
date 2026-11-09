import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'
import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'

// Real Server/Worker/SQLite boundaries, synthetic model, no live credentials.
const binary = resolve(process.env.VISTA_BIN || 'bin/vista')
const parent = resolve('.vista/test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'runtime-events-'))
const config = join(state, 'vista')
mkdirSync(config, { mode: 0o700 })
const readRules = projectRulesFixture(state)
let failure, releaseChildren
const children = new Promise(resolve => { releaseChildren = resolve })
const requests = { background: 0, idle: 0, child: 0, slow: 0 }
const provider = createServer(async (request, response) => {
  try {
    if (request.method === 'GET') return response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'runtime-fixture', input_modalities: ['text'], supports_tools: true }] }))
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    const send = (delta, finish_reason = 'stop') => {
      response.writeHead(200, { 'Content-Type': 'text/event-stream' })
      response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason }] })}\n\ndata: [DONE]\n\n`)
    }
    const text = content => send({ content })
    const tool = (id, name, args) => ({ index: 0, id, type: 'function', function: { name, arguments: JSON.stringify(args) } })
    if (body.messages[0].content.startsWith('Write a short, specific title')) return text('Runtime fixture')
    if (readRules(body, response)) return
    assert.ok(!body.tools.some(tool => tool.function.name === 'await_agent'), 'obsolete wait tool still offered')
    const users = body.messages.filter(message => message.role === 'user').map(message => message.content)
    const outputs = body.messages.filter(message => message.role === 'tool')
    const notices = body.messages.filter(message => message.role === 'system' && message.content.startsWith('Runtime completion event.'))
      .map(message => JSON.parse(message.content.slice(message.content.indexOf('\n') + 1)))
    if (users[0]?.startsWith('child-')) {
      requests.child++
      await children
      return text(`Completed ${users[0]}`)
    }
    if (users[0] === 'background-report') {
      assert.ok(++requests.background <= 3, 'background completion repeated inference')
      if (!outputs.length) return send({ tool_calls: [tool('background', 'start_background', { directory: state, command: 'sleep 2; printf background-evidence' })] }, 'tool_calls')
      if (!notices.length) return text('Background started.')
      assert.equal(notices.length, 1)
      assert.equal(notices[0].kind, 'job')
      return text('Background finished.')
    }
    if (users[0] === 'slow-shell') {
      assert.ok(++requests.slow <= 3, 'slow tool repeated inference')
      if (!outputs.length) return send({ tool_calls: [tool('slow', 'shell', { directory: state, command: 'sleep 2; printf slow-evidence' })] }, 'tool_calls')
      assert.equal(outputs.length, 1, 'tool must have exactly one protocol reply')
      if (!notices.length) {
        assert.equal(JSON.parse(outputs[0].content).status, 'running')
        return text('Waiting for shell.')
      }
      assert.equal(notices[0].kind, 'tool')
      assert.match(notices[0].content, /slow-evidence/)
      return text('Slow tool finished.')
    }
    assert.equal(users[0], 'idle-main')
    assert.ok(++requests.idle <= 7, 'idle coordinator made extra requests')
    if (!outputs.length) return send({ tool_calls: [tool('child-a', 'delegate', { task: 'child-a' }), { ...tool('child-b', 'delegate', { task: 'child-b' }), index: 1 }] }, 'tool_calls')
    if (users.at(-1) === 'steer-now' && !notices.length) return text('Steering handled.')
    return text(notices.length === 2 ? 'All children finished.' : 'Waiting for children.')
  } catch (error) {
    failure = error
    releaseChildren()
    if (response.headersSent) response.destroy()
    else response.writeHead(500).end('fixture assertion failed')
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
writeFileSync(join(config, 'models.json'), JSON.stringify({ default: 'fixture', models: [{ key: 'fixture', provider: 'openai-chat', id: 'runtime-fixture', inputModalities: ['text'], apiKey: 'fixture', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
const execute = promisify(execFile)
let started = false
try {
  started = true
  await execute(binary, ['start'], { env, timeout: 15000 })
  const info = JSON.parse(readFileSync(join(config, 'state', 'instance.json'), 'utf8'))
  async function api(path, options = {}) {
    const response = await fetch(info.address + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' }, signal: AbortSignal.timeout(5000) })
    if (!response.ok) throw new Error(`API ${path}: ${response.status} ${await response.text()}`)
    return response.status === 204 ? null : response.json()
  }
  async function waitFor(session, predicate) {
    const deadline = Date.now() + 20000
    let cursor = 0
    const events = []
    while (Date.now() < deadline) {
      if (failure) throw failure
      const page = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
      if (page.length) cursor = page.at(-1).sequence
      events.push(...page)
      if (predicate(events)) return events
      await new Promise(resolve => setTimeout(resolve, 20))
    }
    throw new Error('runtime event fixture timed out')
  }
  const background = await api('/sessions', { method: 'POST', body: '{}' })
  await api(`/sessions/${background.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'background-report', modelKey: 'fixture' }) })
  const completed = await waitFor(background, events => events.filter(event => event.kind === 'run_finished').length === 2)
  const messages = await api(`/sessions/${background.id}/messages`)
  assert.equal(messages.filter(message => message.role === 'user').length, 1, 'invented user turn')
  assert.equal(messages.at(-1).content, 'Background finished.')
  assert.equal(completed.filter(event => event.kind === 'runtime_event').length, 1)
  assert.equal(completed.filter(event => event.kind === 'run_started').length, 1)
  assert.ok(completed.filter(event => event.kind === 'run_finished').every(event => event.data.status === 'succeeded'))

  const slow = await api('/sessions', { method: 'POST', body: '{}' })
  await api(`/sessions/${slow.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'slow-shell', modelKey: 'fixture' }) })
  const slowEvents = await waitFor(slow, events => events.some(event => event.kind === 'run_finished'))
  assert.equal(slowEvents.filter(event => event.kind === 'runtime_event').length, 1)
  assert.equal(slowEvents.filter(event => event.kind === 'run_finished')[0].data.status, 'succeeded')

  const idle = await api('/sessions', { method: 'POST', body: '{}' })
  await api(`/sessions/${idle.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'idle-main', modelKey: 'fixture' }) })
  await waitFor(idle, events => events.some(event => event.kind === 'message' && event.data.content === 'Waiting for children.'))
  await api(`/sessions/${idle.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'steer-now', modelKey: 'fixture' }) })
  const steered = await waitFor(idle, events => events.some(event => event.kind === 'message' && event.data.content === 'Steering handled.'))
  assert.equal(steered.filter(event => event.kind === 'agent_task' && event.data.task?.parentId === 'main' && event.data.task.status !== 'started').length, 0, 'steering canceled children')
  releaseChildren()
  const joined = await waitFor(idle, events => events.some(event => event.kind === 'run_finished'))
  assert.equal(joined.filter(event => event.kind === 'agent_task' && event.data.task?.parentId === 'main' && event.data.task.status === 'succeeded').length, 2)
  assert.equal(joined.filter(event => event.kind === 'runtime_event').length, 2)
  assert.equal(requests.child, 2, 'child was restarted')
  console.log(JSON.stringify({ requests, backgroundRuns: 2, slowResultDeliveries: 1, steeredWithoutCancelingChildren: true }))
} catch (error) {
  try { console.error(readFileSync(join(config, 'state/server.log'), 'utf8').replaceAll(state, '<fixture>')) } catch {}
  throw error
} finally {
  releaseChildren()
  if (started) await execute(binary, ['stop'], { env, timeout: 15000 })
  provider.closeAllConnections()
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
