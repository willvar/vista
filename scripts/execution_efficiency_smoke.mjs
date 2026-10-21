import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'

// Exercise CLI -> Server -> Worker -> model and persisted artifacts in isolation.
const binary = resolve(process.env.VISTA_BIN || 'bin/vista')
const parent = resolve('.vista/test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'eff-'))
const config = join(state, 'vista')
mkdirSync(config, { mode: 0o700 })
const input = join(state, 'input.txt')
const contents = 'evidence '.repeat(1500) + 'END_OF_EVIDENCE'
writeFileSync(input, contents)
const modelID = 'efficiency-fixture'
const providerName = 'Boundary fixture'
const providerKey = 'fixture-connection-key'
const instructions = 'Keep the execution evidence in the final reply.'
let mainRequests = 0, childRequests = 0, failure, releaseSlow
const slowReady = new Promise(resolve => { releaseSlow = resolve })
const resultsSeen = []
const provider = createServer(async (request, response) => {
  try {
    if (request.method === 'GET' && request.url === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ data: [{ id: modelID }] }))
      return
    }
    assert.equal(request.url, '/v1/chat/completions')
    assert.equal(request.headers.authorization, `Bearer ${providerKey}`)
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    const send = (delta, finish_reason = 'stop') => {
      response.writeHead(200, { 'Content-Type': 'text/event-stream' })
      response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason }] })}\n\ndata: [DONE]\n\n`)
    }
    const tool = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } })
    const calls = (...items) => send({ tool_calls: items.map((item, index) => ({ index, ...item })) }, 'tool_calls')
    if (body.messages[0].content.startsWith('Write a short, specific title')) return send({ content: 'Execution efficiency' })
    assert.ok(body.messages[0].content.includes(instructions), 'global instructions missing from execution context')
    const prompt = body.messages.find(message => message.role === 'user')?.content
    if (prompt === 'fast evidence' || prompt === 'slow evidence') {
      childRequests++
      assert.match(body.messages[0].content, /Verified scope/)
      if (prompt === 'slow evidence') await slowReady
      return send({ content: `Conclusion: ${prompt}.\nEvidence: input.txt:1.\nVerified scope: fixture response only.\nUncertainty: none.\nReview needed: none.` })
    }
    mainRequests++
    const outputs = body.messages.filter(message => message.role === 'tool')
    for (const output of outputs) assert.ok(body.messages.some(message => message.tool_calls?.some(call => call.id === output.tool_call_id)), 'orphan tool result')
    const last = outputs.length ? JSON.parse(outputs.at(-1).content) : null
    switch (mainRequests) {
      case 1:
        return calls(tool('fast', 'delegate', { task: 'fast evidence' }), tool('slow', 'delegate', { task: 'slow evidence' }))
      case 2:
        assert.equal(outputs.length, 2)
        assert.ok(outputs.every(output => JSON.parse(output.content).status === 'running'))
        return calls(tool('own-work', 'read_file', { path: input, startLine: 1, lineCount: 1 }))
      case 3:
        assert.equal(last.content, `1: ${contents}`)
        assert.equal(last.startLine, 1)
        assert.equal(last.endLine, 1)
        assert.equal(last.lineCount, 1)
        assert.equal(last.nextLine, undefined)
        // Directory is explicit; the command does not interpolate a filesystem path.
        return calls(tool('large-output', 'shell', { directory: state, command: 'cat input.txt' }))
      case 4:
        assert.equal(last.previewBytes, 8192)
        assert.equal(last.bytes, Buffer.byteLength(contents))
        assert.equal(last.truncated, true)
        assert.equal(last.outputPath, undefined)
        return calls(tool('remaining-output', 'read_tool_output', { artifactId: last.artifactId, offset: last.nextOffset }))
      case 5:
        assert.equal(last.content, contents.slice(8192))
        assert.equal(last.truncated, false)
        return calls(tool('first-report', 'await_agent', { taskIds: ['main/fast', 'main/slow'] }))
      case 6:
        assert.match(JSON.stringify(last), /fast evidence/)
        assert.doesNotMatch(JSON.stringify(last), /Conclusion: slow evidence/)
        resultsSeen.push('fast')
        releaseSlow()
        return calls(tool('second-report', 'await_agent', { taskIds: ['main/slow'] }))
      case 7:
        assert.match(JSON.stringify(last), /slow evidence/)
        resultsSeen.push('slow')
        return send({ content: 'Collected evidence and complete output.' })
      default:
        throw new Error('unexpected extra model request')
    }
  } catch (error) {
    failure = error
    releaseSlow()
    if (response.headersSent) response.destroy()
    else response.writeHead(500).end('fixture assertion failed')
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
writeFileSync(join(config, 'models.json'), JSON.stringify({ default: 'fixture', models: [{
  key: 'fixture', provider: 'openai-chat', id: modelID, apiKey: 'fixture',
  baseUrl: `http://127.0.0.1:${provider.address().port}/v1`, context: 128000,
}] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
const execute = promisify(execFile)
let started = false
try {
  started = true
  await execute(binary, ['start'], { env, timeout: 15000 })
  const info = JSON.parse(readFileSync(join(config, 'state', 'instance.json'), 'utf8'))
  const api = async (path, options = {}) => {
    const response = await fetch(info.address + '/api' + path, { ...options, signal: AbortSignal.timeout(5000),
      headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`API ${path}: ${response.status} ${await response.text()}`)
    return response.status === 204 ? null : response.json()
  }
  // Exercise catalog validation/persistence and live resolution through the same
  // API as the settings page, with synthetic credentials and no external model.
  await api('/provider-config', { method: 'POST', body: JSON.stringify({
    name: providerName, provider: 'openai-chat', providerId: 'custom', credentialMode: 'bearer',
    baseUrl: `http://127.0.0.1:${provider.address().port}/v1`, apiKey: providerKey,
  }) })
  const catalog = await api('/models')
  const configured = catalog.models.find(model => model.name === providerName && model.id === modelID)
  assert.ok(configured?.key, 'saved provider not available immediately')
  assert.ok(!JSON.stringify(catalog).includes(providerKey), 'public catalog leaked provider credentials')
  const settings = await api('/model-config')
  assert.ok(!JSON.stringify(settings).includes(providerKey), 'model settings leaked provider credentials')
  const saved = JSON.parse(readFileSync(join(config, 'models.json'), 'utf8'))
  const savedModel = saved.models.find(model => model.key === configured.key)
  assert.equal(savedModel.apiKey, providerKey)
  assert.deepEqual(Object.keys(savedModel).sort(), ['apiKey', 'authentication', 'baseUrl', 'connectionId', 'id', 'key', 'name', 'provider', 'providerId'], 'derived catalog fields were written into user settings')
  await api('/agent-settings', { method: 'PUT', body: JSON.stringify({ instructions }) })
  const session = await api('/sessions', { method: 'POST', body: '{}' })
  const sent = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Inspect evidence in parallel.', modelKey: configured.key }) })
  const events = []
  let cursor = sent.event.sequence, finished
  const deadline = Date.now() + 30000
  while (!finished && !failure && Date.now() < deadline) {
    const page = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
    if (page.length) cursor = page.at(-1).sequence
    events.push(...page.filter(event => event.data.runId === sent.runId))
    finished = events.find(event => event.kind === 'run_finished')
    if (!finished) await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (failure) throw failure
  assert.equal(finished?.data.status, 'succeeded')
  assert.deepEqual(resultsSeen, ['fast', 'slow'])
  assert.equal(childRequests, 2)
  const childCompleted = events.filter(event => event.kind === 'agent_task' && event.data.task?.parentId === 'main' && event.data.task.status === 'succeeded')
  assert.equal(childCompleted.length, 2)
  assert.ok(childCompleted.every(event => event.data.task.report?.bytes > 0), 'missing persisted report references')
  const ownWork = events.find(event => event.kind === 'tool' && event.data.tool?.callId === 'own-work' && event.data.status === 'succeeded')
  const slowCompleted = childCompleted.find(event => event.data.task.id === 'main/slow')
  assert.ok(ownWork.sequence < slowCompleted.sequence, 'parent waited for slow task before doing independent work')
  const artifacts = await api(`/sessions/${session.id}/artifacts`)
  assert.ok(artifacts.some(artifact => artifact.bytes === Buffer.byteLength(contents) && artifact.complete), 'shell artifact not persisted')
  console.log(JSON.stringify({ mainRequests, childRequests, reportOrder: resultsSeen, artifactBytes: Buffer.byteLength(contents), status: finished.data.status }))
} catch (error) {
  try { console.error(readFileSync(join(config, 'state', 'server.log'), 'utf8').replaceAll(state, '<fixture>')) } catch {}
  throw error
} finally {
  releaseSlow()
  if (started) await execute(binary, ['stop'], { env, timeout: 15000 })
  provider.closeAllConnections()
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
