import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, execFileSync } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, readFileSync, existsSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only Linux smoke test. VISTA_DOUBLE_CRASH=1 kills Server and Worker
// without giving the Worker a chance to clean its Session descendants.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-background-'))
const readRules = projectRulesFixture(state)
const childPidFile = join(state, 'job-child.pid')
const doubleCrash = process.env.VISTA_DOUBLE_CRASH === '1'
let calls = 0
const provider = createServer(async (request, response) => {
  if (request.url === '/v1/models') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ data: [{ id: 'background-fixture' }] }))
    return
  }
  if (request.url !== '/v1/chat/completions') throw new Error(request.url)
  let raw = ''
  for await (const chunk of request) raw += chunk
  if (readRules(JSON.parse(raw), response)) return
  calls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  if (calls === 1) {
    const args = JSON.stringify({ directory: state, command: `sleep 30 & echo $! > ${childPidFile}; wait`, timeoutSeconds: 60 })
    const chunk = JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'call_background', type: 'function', function: { name: 'start_background', arguments: args } }] }, finish_reason: 'tool_calls' }] })
    response.end(`data: ${chunk}\n\ndata: [DONE]\n\n`)
  } else if (calls === 2) {
    response.end('data: {"choices":[{"delta":{"content":"Job started; I will leave it running."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
  } else {
    throw new Error('background command was replayed')
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'test-key', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
let childPid = 0
let workerPid = 0
try {
  let url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  let endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.json()
  }
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'background job' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'start a background job' }) })
  let job
  for (let i = 0; i < 200; i++) {
    const jobs = await api(`/sessions/${session.id}/jobs`)
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    if (jobs[0]?.status === 'running' && events.some(event => event.kind === 'run_finished' && event.data.runId === run.runId && event.data.status === 'succeeded') && existsSync(childPidFile)) { job = jobs[0]; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!job) throw new Error('job did not remain running after the reply')
  childPid = Number(readFileSync(childPidFile, 'utf8').trim())
  const status = await api('/status')
  if (status.sessions.running !== 1 || status.managedBackgroundProcesses !== 1 || calls !== 2) throw new Error(`wrong background state: ${JSON.stringify(status.sessions)}, jobs=${status.managedBackgroundProcesses}, calls=${calls}`)
  const output = await api(`/sessions/${session.id}/jobs/${job.id}/output?offset=0`)
  if (output.jobId !== job.id) throw new Error('job output lookup failed')
  if (doubleCrash) {
    const serverPid = JSON.parse(readFileSync(join(state, 'vista', 'state', 'instance.json'), 'utf8')).pid
    workerPid = Number(execFileSync('ps', ['-o', 'pid=', '--ppid', String(serverPid)], { encoding: 'utf8' }).trim().split(/\s+/)[0])
    if (!workerPid) throw new Error('Worker PID unavailable')
    process.kill(workerPid, 'SIGSTOP')
    process.kill(serverPid, 'SIGKILL')
    process.kill(workerPid, 'SIGKILL')
    url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
    endpoint = new URL(url).origin
  } else {
    const response = await fetch(endpoint + `/api/sessions/${session.id}/jobs/${job.id}/stop`, { method: 'POST' })
    if (response.status !== 202) throw new Error(`stop job: HTTP ${response.status}`)
  }
  let finalStatus
  for (let i = 0; i < 200; i++) {
    const jobs = await api(`/sessions/${session.id}/jobs`)
    const current = await api('/status')
    if (jobs[0]?.status === 'interrupted' && current.sessions.running === 0 && current.managedBackgroundProcesses === 0) { finalStatus = jobs[0]; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!finalStatus) throw new Error('job was not marked interrupted')
  let alive = true
  try { alive = !readFileSync(`/proc/${childPid}/status`, 'utf8').includes('State:\tZ') } catch { alive = false }
  if (alive || calls !== 2) throw new Error(`orphan child=${alive}, model calls=${calls}`)
  console.log(doubleCrash ? 'Double-crash startup cleanup passed' : 'Background job lifecycle passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  if (workerPid) try { process.kill(workerPid, 'SIGKILL') } catch {}
  if (childPid) try { process.kill(childPid, 'SIGKILL') } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
