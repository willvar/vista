import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, execFileSync } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, rmSync, existsSync, readFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// Linux-only, HTTP-only crash smoke. No browser is launched.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-worker-crash-'))
const childPidFile = join(state, 'child.pid')
let calls = 0
const provider = createServer((request, response) => {
  if (request.url === '/v1/models') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ data: [{ id: 'crash-fixture' }] }))
    return
  }
  calls++
  if (calls !== 1 || request.url !== '/v1/chat/completions') throw new Error('side-effecting tool was replayed')
  const args = JSON.stringify({ directory: state, command: `sleep 30 & echo $! > ${childPidFile}; wait`, timeoutSeconds: 60 })
  const chunk = JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'call_shell', type: 'function', function: { name: 'shell', arguments: args } }] }, finish_reason: 'tool_calls' }] })
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  response.end(`data: ${chunk}\n\ndata: [DONE]\n\n`)
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'crash-key', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
let childPid = 0
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  let endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.json()
  }
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'crash cleanup' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'run shell' }) })
  for (let i = 0; i < 200 && !existsSync(childPidFile); i++) await new Promise(resolve => setTimeout(resolve, 25))
  if (!existsSync(childPidFile)) throw new Error('shell child never started')
  childPid = Number(readFileSync(childPidFile, 'utf8').trim())
  const beforeCrash = await api(`/sessions/${session.id}/event-page?after=0`)
  if (!beforeCrash.some(event => event.kind === 'tool' && event.data.status === 'started' && event.data.tool?.name === 'shell')) throw new Error('shell started before its record was persisted')
  if (!beforeCrash.some(event => event.kind === 'artifact' && event.data.status === 'started')) throw new Error('shell output was not registered before the command started')
  const serverPid = JSON.parse(readFileSync(join(state, 'vista', 'state', 'instance.json'), 'utf8')).pid
  const workerPid = Number(execFileSync('ps', ['-o', 'pid=', '--ppid', String(serverPid)], { encoding: 'utf8' }).trim().split(/\s+/)[0])
  if (!workerPid || !childPid) throw new Error(`missing process identities: worker=${workerPid} child=${childPid}`)
  const masterCrash = process.env.VISTA_CRASH_MASTER === '1'
  process.kill(masterCrash ? serverPid : workerPid, 'SIGKILL')
  if (masterCrash) {
    let childGone = false
    for (let i = 0; i < 120; i++) {
      try { childGone = readFileSync(`/proc/${childPid}/status`, 'utf8').includes('State:\tZ') } catch { childGone = true }
      if (childGone) break
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    if (!childGone) throw new Error('Server death left a shell descendant running')
    const restarted = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
    endpoint = new URL(restarted).origin
  }
  let recovered = false
  for (let i = 0; i < 120; i++) {
    const status = await api('/status')
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    const interrupted = events.some(event => event.kind === 'run_finished' && event.data.runId === run.runId && event.data.status === 'interrupted')
    const unknown = events.some(event => event.kind === 'tool' && event.data.runId === run.runId && event.data.status === 'unknown' && event.data.tool?.name === 'shell')
    const artifact = events.some(event => event.kind === 'artifact' && event.data.runId === run.runId && event.data.status === 'failed' && !event.data.complete)
    if (status.sessions.workers === 1 && !status.sessions.running && interrupted && unknown && artifact) { recovered = true; break }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!recovered) throw new Error('Worker did not recover with run marked interrupted')
  let childAlive = true
  try { childAlive = !readFileSync(`/proc/${childPid}/status`, 'utf8').includes('State:\tZ') } catch { childAlive = false }
  if (childAlive || calls !== 1) throw new Error(`orphan child=${childAlive}, model calls=${calls}`)
  console.log(masterCrash ? 'Server crash descendant cleanup passed' : 'Worker crash descendant cleanup passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  if (childPid) try { process.kill(childPid, 'SIGKILL') } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
