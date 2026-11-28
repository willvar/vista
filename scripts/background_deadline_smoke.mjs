import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'

// Real Server/Worker/timers with an isolated deterministic model endpoint.
const binary = resolve(process.env.VISTA_BIN || 'bin/vista-server')
const parent = resolve('.vista/test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'deadline-'))
const readRules = projectRulesFixture(state)
let timedCalls = 0, originalExpiry, adjustedExpiry, warningCount = 0
let failed
const counts = new Map()
const provider = createServer(async (request, response) => {
  try {
    if (request.url === '/v1/models') return response.end(JSON.stringify({ data: [{ id: 'deadline-fixture' }] }))
    assert.equal(request.url, '/v1/chat/completions')
    let raw = ''; for await (const part of request) raw += part
    const input = JSON.parse(raw)
    if (readRules(input, response)) return
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    const answer = content => response.end(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`)
    const call = tools => response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls: tools.map(([name, args], index) => ({ index, id: `tool-${timedCalls}-${counts.size}-${index}`, type: 'function', function: { name, arguments: JSON.stringify(args) } })) }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
    const prompt = input.messages.filter(message => message.role === 'user' && typeof message.content === 'string' && /^(timed|limit|add|reject|raised) job/.test(message.content)).at(-1)?.content
    if (prompt === 'timed job') {
      timedCalls++
      const result = input.messages.filter(message => message.role === 'tool').at(-1)
      const notices = input.messages.filter(message => typeof message.content === 'string' && /^(Background job deadline warning|Runtime completion event)/.test(message.content))
      const notice = notices.length ? JSON.parse(notices.at(-1).content.split('\n').slice(1).join('\n')) : undefined
      if (timedCalls === 1) return call([['start_background', { directory: state, command: 'printf ready; sleep 30', timeoutSeconds: 6, warnBeforeSeconds: 4 }]])
      if (timedCalls === 2) { originalExpiry = JSON.parse(result.content).expiresAt; assert.ok(originalExpiry); return answer('Timed job started.') }
      if (timedCalls === 3) {
        assert.equal(notice.kind, 'job_warning'); assert.equal(notice.status, 'running'); warningCount++
        const metadata = JSON.parse(notice.content)
        assert.equal(metadata.expiresAt, originalExpiry)
        return call([['update_background_timeout', { jobId: notice.sourceId, expectedExpiresAt: originalExpiry, timeoutSeconds: 6, warnBeforeSeconds: 2 }]])
      }
      if (timedCalls === 4) {
        adjustedExpiry = JSON.parse(result.content).expiresAt
        assert.ok(Date.parse(adjustedExpiry) > Date.parse(originalExpiry))
        return answer('Deadline adjusted.')
      }
      if (timedCalls === 5) {
        assert.equal(notice.kind, 'job_warning'); warningCount++
        assert.equal(JSON.parse(notice.content).expiresAt, adjustedExpiry)
        return answer('Warning observed; leave the hard deadline unchanged.')
      }
      assert.equal(timedCalls, 6); assert.equal(notice.kind, 'job'); assert.equal(notice.status, 'interrupted')
      assert.equal(JSON.parse(notice.content).terminationReason, 'timeout')
      return answer('Timed job terminated.')
    }
    assert.ok(prompt)
    const count = (counts.get(prompt) || 0) + 1; counts.set(prompt, count)
    if (count === 1) return call(Array.from({ length: prompt === 'limit job' ? 2 : 1 }, () => ['start_background', { directory: state, command: 'sleep 30' }]))
    const results = input.messages.filter(message => message.role === 'tool').slice(prompt === 'limit job' ? -2 : -1).map(message => String(message.content))
    if (prompt === 'limit job') {
      assert.equal(results.filter(text => text.includes('jobId')).length, 1)
      assert.equal(results.filter(text => text.includes('limit reached')).length, 1)
    } else if (prompt === 'reject job') assert.ok(results[0].includes('2 active / 1 allowed'))
    else assert.ok(results[0].includes('jobId'))
    answer(`${prompt} handled.`)
  } catch (error) { failed = error; if (!response.headersSent) response.writeHead(500); response.end(String(error)) }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_PORT: await availableVistaPort(), VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'test-key', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
try {
  const origin = new URL((await promisify(execFile)(binary, ['start'], { env })).stdout.trim()).origin
  started = true
  const api = async (path, options = {}) => {
    const response = await fetch(origin + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    assert.ok(response.ok, `${path}: ${response.status} ${await response.clone().text()}`)
    const text=await response.text()
    return text?JSON.parse(text):null
  }
  const wait = async check => {
    const deadline = Date.now() + 20000
    while (Date.now() < deadline) {
      if (failed) throw failed
      if (await check()) return
      await new Promise(resolve => setTimeout(resolve, 40))
    }
    throw new Error('background contract did not settle')
  }
  const create = () => api('/sessions', { method: 'POST', body: '{}' })
  const send = (id, content) => api(`/sessions/${id}/messages`, { method: 'POST', body: JSON.stringify({ modelKey: 'openai', content }) })
  const initialConfig=await api('/memory-config')
  await api('/memory-config',{method:'PUT',body:JSON.stringify({...initialConfig,concurrentRuns:1})})
  const timed = await create()
  await send(timed.id, 'timed job')
  await wait(async () => timedCalls === 6 && (await api(`/sessions/${timed.id}/jobs`))[0]?.terminationReason === 'timeout')
  assert.equal(warningCount, 2)
  const [finished] = await api(`/sessions/${timed.id}/jobs`)
  assert.equal(finished.expiresAt, adjustedExpiry); assert.ok(finished.warnedAt)
  assert.equal(finished.complete, false)
  await api('/memory-config',{method:'PUT',body:JSON.stringify(initialConfig)})
  const config = await api('/memory-config')
  assert.equal(config.backgroundJobsPerSession, 4)
  const limit = backgroundJobsPerSession => api('/memory-config', { method: 'PUT', body: JSON.stringify({ ...config, backgroundJobsPerSession }) })
  await limit(1)
  const session = await create()
  const exercise = async prompt => {
    const sent = await send(session.id, prompt)
    await wait(async () => (await api(`/sessions/${session.id}/event-page?after=${sent.event.sequence}`)).some(event => event.kind === 'run_finished' && event.data.runId === sent.runId && event.data.status === 'succeeded'))
  }
  await exercise('limit job')
  await limit(2); await exercise('add job')
  await limit(1)
  assert.equal((await api(`/sessions/${session.id}/jobs`)).filter(job => job.status === 'running').length, 2)
  await exercise('reject job')
  await limit(3); await exercise('raised job')
  assert.equal((await api(`/sessions/${session.id}/jobs`)).filter(job => job.status === 'running').length, 3)
  await api(`/sessions/${session.id}/close`, { method: 'POST' })
  console.log('Advance warnings, explicit deadline replacement, hard timeout and resident/new Worker job limits passed')
} finally {
  if (started) await promisify(execFile)(binary, ['stop'], { env }).catch(() => {})
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
