import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// Two live Sessions read the same file, then race to write with its original hash.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const parent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'session-conflict-'))
const path = join(state, 'shared.txt')
writeFileSync(path, 'initial')
const turns = new Map()
let releaseWrites
const writesReady = new Promise(resolve => { releaseWrites = resolve })
const server = createServer(async (request, response) => {
  try {
    if (request.url !== '/v1/chat/completions') throw new Error(`unexpected path ${request.url}`)
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    const name = ['alpha', 'beta'].find(value => body.messages.some(message => message.role === 'user' && message.content?.includes(`session ${value}`)))
    if (!name) throw new Error('Session marker missing')
    const turn = (turns.get(name) || 0) + 1
    turns.set(name, turn)
    let event
    if (turn === 1) {
      event = toolCall(`${name}-read`, 'read_file', { path })
    } else if (turn === 2) {
      const result = body.messages.findLast(message => message.role === 'tool')
      const file = JSON.parse(result?.content || '{}')
      if (file.content !== 'initial' || !/^[a-f0-9]{64}$/.test(file.sha256)) throw new Error(`bad read result for ${name}`)
      if ([...turns.values()].filter(value => value >= 2).length === 2) releaseWrites()
      await Promise.race([writesReady, new Promise((_, reject) => setTimeout(() => reject(new Error('Sessions did not reach the write barrier')), 10000))])
      event = toolCall(`${name}-write`, 'write_file', { path, expectedSha256: file.sha256, content: name })
    } else if (turn === 3) {
      const result = JSON.parse(body.messages.findLast(message => message.role === 'tool')?.content || '{}')
      if (result.error && result.error !== 'file changed since it was read') throw new Error(`bad conflict for ${name}: ${result.error}`)
      if (!result.error && !/^[a-f0-9]{64}$/.test(result.sha256)) throw new Error(`bad write result for ${name}`)
      event = { choices: [{ delta: { content: result.error ? `${name}: conflict` : `${name}: wrote` }, finish_reason: 'stop' }] }
    } else throw new Error(`unexpected model turn ${name}:${turn}`)
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    response.end(`data: ${JSON.stringify(event)}\n\ndata: [DONE]\n\n`)
  } catch (error) {
    response.writeHead(500)
    response.end(String(error))
  }
})
function toolCall(id, name, args) {
  return { choices: [{ delta: { tool_calls: [{ index: 0, id, type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, finish_reason: 'tool_calls' }] }
}
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'fake', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${server.address().port}/v1` }
let started = false
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(route, options = {}) {
    const response = await fetch(endpoint + '/api' + route, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${route}: ${response.status} ${await response.text()}`)
    if (response.status === 204) return null
    return response.json()
  }
  const sessions = await Promise.all(['alpha', 'beta'].map(title => api('/sessions', { method: 'POST', body: JSON.stringify({ title }) })))
  const submitted = await Promise.all(sessions.map((session, index) => api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `session ${['alpha', 'beta'][index]}: update the shared file` }) })))
  const finished = new Map()
  for (let attempt = 0; attempt < 300 && finished.size < 2; attempt++) {
    for (let index = 0; index < sessions.length; index++) {
      if (finished.has(index)) continue
      const events = await api(`/sessions/${sessions[index].id}/event-page?after=${submitted[index].event.sequence}`)
      const done = events.find(event => event.kind === 'run_finished' && event.data.runId === submitted[index].runId)
      if (done) {
        if (done.data.status !== 'succeeded') throw new Error(`run failed: ${JSON.stringify(done)}`)
        finished.set(index, events)
      }
    }
    if (finished.size < 2) await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (finished.size !== 2) throw new Error(`runs did not finish: ${JSON.stringify([...turns])}`)
  const statuses = [...finished.values()].map(events => events.find(event => event.kind === 'tool' && event.data?.tool?.name === 'write_file' && event.data.status !== 'started')?.data.status)
  if (statuses.sort().join(',') !== 'failed,succeeded') throw new Error(`write statuses: ${JSON.stringify(statuses)}`)
  const content = readFileSync(path, 'utf8')
  if (!['alpha', 'beta'].includes(content) || turns.get('alpha') !== 3 || turns.get('beta') !== 3) throw new Error(`bad final state: ${content}, turns=${JSON.stringify([...turns])}`)
  await Promise.all(sessions.map(session => api(`/sessions/${session.id}/close`, { method: 'POST' })))
  const closed = await api('/status')
  if (closed.sessions.open !== 0 || closed.sessions.closed !== 2 || closed.sessions.workers !== 0) throw new Error(`closed Sessions retained Workers: ${JSON.stringify(closed.sessions)}`)
  await api(`/sessions/${sessions[0].id}/open`, { method: 'POST' })
  const reopened = await api('/status')
  if (reopened.sessions.open !== 1 || reopened.sessions.closed !== 1 || reopened.sessions.workers !== 1) throw new Error(`Session did not reopen: ${JSON.stringify(reopened.sessions)}`)
  await Promise.all(sessions.map(session => api(`/sessions/${session.id}`, { method: 'DELETE' })))
  const deleted = await api('/status')
  if (deleted.sessions.total !== 0 || deleted.sessions.workers !== 0 || (await api('/sessions')).length !== 0) throw new Error(`deleted Sessions persisted: ${JSON.stringify(deleted.sessions)}`)
  console.log('Concurrent Session conflict, close, reopen, and delete passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => server.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
