import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, spawn } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only backend load: 20 Sessions execute shell, MCP, and subagent work.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const fixture = process.env.VISTA_MCP_FIXTURE || join(process.cwd(), 'bin', 'mcp-fixture')
const mcpTransport = process.env.VISTA_MCP_TRANSPORT || 'stdio'
if (!['stdio', 'http'].includes(mcpTransport)) throw new Error('VISTA_MCP_TRANSPORT must be stdio or http')
const rounds = Number(process.env.VISTA_MIXED_ROUNDS ?? 1)
if (!Number.isInteger(rounds) || rounds < 1 || rounds > 240) throw new Error('VISTA_MIXED_ROUNDS must be an integer from 1 to 240')
const closedSessionCount = Number(process.env.VISTA_CLOSED_SESSIONS || 0)
if (!Number.isInteger(closedSessionCount) || closedSessionCount < 0 || closedSessionCount > 20) throw new Error('VISTA_CLOSED_SESSIONS must be an integer from 0 to 20')
const diagnosticHoldMs = Number(process.env.VISTA_DIAGNOSTIC_HOLD_MS || 0)
if (!Number.isInteger(diagnosticHoldMs) || diagnosticHoldMs < 0 || diagnosticHoldMs > 300000) throw new Error('VISTA_DIAGNOSTIC_HOLD_MS must be 0–300000')
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
function cgroupMemory() {
  try {
    const line = readFileSync('/proc/self/cgroup', 'utf8').split('\n').find(line => line.startsWith('0::'))
    if (!line) return null
    const root = join('/sys/fs/cgroup', line.slice(3).replace(/^\//, ''))
    const value = name => readFileSync(join(root, name), 'utf8').trim()
    const max = value('memory.max')
    const events = Object.fromEntries(value('memory.events').split('\n').filter(Boolean).map(line => line.split(/\s+/)))
    const stat = Object.fromEntries(value('memory.stat').split('\n').filter(Boolean).map(line => line.split(/\s+/)))
    const mib = bytes => +(Number(bytes || 0) / 1048576).toFixed(1)
    return { maxMiB: max === 'max' ? null : mib(max), currentMiB: mib(value('memory.current')), peakMiB: mib(value('memory.peak')), anonMiB: mib(stat.anon), fileMiB: mib(stat.file), kernelMiB: mib(stat.kernel), oom: Number(events.oom || 0), oomKill: Number(events.oom_kill || 0) }
  } catch { return null }
}
const initialCgroup = cgroupMemory()
const requiredCgroupMiB = Number(process.env.VISTA_REQUIRE_CGROUP_MIB || 0)
if (!Number.isInteger(requiredCgroupMiB) || requiredCgroupMiB < 0) throw new Error('VISTA_REQUIRE_CGROUP_MIB must be a nonnegative integer')
if (requiredCgroupMiB && initialCgroup?.maxMiB !== requiredCgroupMiB) throw new Error(`expected ${requiredCgroupMiB} MiB cgroup limit, got ${initialCgroup?.maxMiB ?? 'none'}`)
const state = mkdtempSync(join(process.env.VISTA_TEST_TMPDIR || process.cwd(), 'vista-mixed-capacity-'))
const counts = { shell: 0, mcp: 0, delegate: 0, child: 0, summary: 0 }
const provider = createServer((request, response) => {
  handleProvider(request, response).catch(error => {
    console.error(error)
    if (response.headersSent) response.destroy(error)
    else {
      response.writeHead(500)
      response.end(error.message)
    }
  })
})
async function handleProvider(request, response) {
  if (request.url !== '/v1/chat/completions') throw new Error(request.url)
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  const userIndex = body.messages.findLastIndex(message => message.role === 'user')
  const prompt = body.messages[userIndex]?.content
  const results = body.messages.slice(userIndex + 1).filter(message => message.role === 'tool')
  await delay(2500)
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  const send = event => response.end(`data: ${JSON.stringify({ choices: [event] })}\n\ndata: [DONE]\n\n`)
  const tool = (name, args, id) => send({ delta: { tool_calls: [{ index: 0, id, type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, finish_reason: 'tool_calls' })
  const text = value => send({ delta: { content: value }, finish_reason: 'stop' })
  if (body.messages[0]?.content?.startsWith('Compress the completed execution transcript')) {
    counts.summary++
    return text('Earlier synthetic workload steps completed.')
  }
  if (prompt?.startsWith('child:')) {
    counts.child++
    return text(`child result ${'x'.repeat(1024)}`)
  }
  if (prompt?.startsWith('shell:')) {
    counts.shell++
    if (results.length === 0) return tool('shell', { directory: state, command: "sleep 3; printf '%65536s' x", timeoutSeconds: 15 }, 'call_shell')
    const shell = JSON.parse(results[0].content)
    if (shell.bytes !== 65536 || shell.exitCode !== 0 || !shell.complete) throw new Error(`shell output or exit status incorrect: ${JSON.stringify({ bytes: shell.bytes, exitCode: shell.exitCode, complete: shell.complete, error: shell.error })}`)
    return text('shell completed')
  }
  if (prompt?.startsWith('mcp:')) {
    counts.mcp++
    if (results.length === 0) return tool('mcp_list_tools', { server: 'local' }, 'call_list')
    if (results.length === 1) {
      const page = JSON.parse(results[0].content)
      if (!page.tools?.some(item => item.name === 'echo')) throw new Error('MCP tool discovery failed')
      return tool('mcp_call_tool', { server: 'local', name: 'echo', arguments: { text: 'm'.repeat(1024) } }, 'call_mcp')
    }
    if (!results[1].content.includes('fixture:')) throw new Error('MCP result missing')
    return text('MCP completed')
  }
  if (prompt?.startsWith('delegate:')) {
    counts.delegate++
    if (results.length === 0) return tool('delegate', { task: `child:${prompt.slice(9)}` }, 'call_child')
    if (results.length === 1) return tool('await_agent', { taskIds: [JSON.parse(results[0].content).taskId] }, 'await_child')
    if (!results[1].content.includes('child result')) throw new Error('subagent result missing')
    return text('subagent completed')
  }
  throw new Error(`unexpected model prompt: ${prompt}`)
}
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'mock', models: [{ key: 'mock', provider: 'openai-chat', id: 'mock', apiKey: 'fake', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
let fixtureProcess
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
let started = false
try {
  let mcpConfig = { id: 'local', name: 'local', transport: 'stdio', command: fixture, args: ['stdio'], directory: state }
  if (mcpTransport === 'http') {
    fixtureProcess = spawn(fixture, ['http'], { stdio: ['ignore', 'pipe', 'pipe'] })
    const address = await Promise.race([
      new Promise((resolve, reject) => {
        let output = ''
        fixtureProcess.stdout.on('data', chunk => {
          output += chunk.toString()
          if (output.includes('\n')) resolve(output.split('\n')[0].trim())
        })
        fixtureProcess.once('error', reject)
        fixtureProcess.once('exit', code => reject(new Error(`HTTP MCP fixture exited before startup: ${code}`)))
      }),
      delay(10000).then(() => { throw new Error('HTTP MCP fixture did not start') })
    ])
    mcpConfig = { id: 'local', name: 'local', transport: 'http', endpoint: `http://${address}`, token: 'fixture-token' }
  }
  writeFileSync(join(vistaConfig, 'mcp.json'), JSON.stringify([mcpConfig]), { mode: 0o600 })
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    if (response.status === 204) return null
    return response.json()
  }
  function memory(status) {
    const workers = status.workerMetrics.map(item => item.memory)
    if (status.memory.peakRssBytes < status.memory.rssBytes || workers.some(item => item?.sampledAt && item.peakRssBytes < item.rssBytes)) throw new Error('reported peak RSS is below current RSS')
    return { serverRSSMiB: +(status.memory.rssBytes / 1048576).toFixed(1), serverPeakRSSMiB: +(status.memory.peakRssBytes / 1048576).toFixed(1), workerRSSMiB: +(workers.reduce((sum, item) => sum + item.rssBytes, 0) / 1048576).toFixed(1), sumWorkerProcessPeakRSSMiB: +(workers.reduce((sum, item) => sum + (item.peakRssBytes || 0), 0) / 1048576).toFixed(1), workerHeapMiB: +(workers.reduce((sum, item) => sum + item.heapBytes, 0) / 1048576).toFixed(1) }
  }
  const closedIDs = []
  for (let i = 0; i < closedSessionCount; i++) {
    const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `closed mixed ${i}` }) })
    await api(`/sessions/${session.id}/close`, { method: 'POST' })
    closedIDs.push(session.id)
  }
  if (closedIDs.length) {
    const closed = await api('/status')
    if (closed.sessions.closed !== closedSessionCount || closed.sessions.open !== 0 || closed.sessions.workers !== 0 || closed.workerMetrics.length !== 0) throw new Error(`closed Sessions retained Workers: ${JSON.stringify(closed.sessions)}`)
    const listed = await api('/sessions')
    if (closedIDs.some(id => !listed.some(item => item.id === id && !item.open))) throw new Error('closed Session record disappeared')
  }
  const sessions = []
  for (let i = 0; i < 20; i++) sessions.push(await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `mixed ${i}` }) }))
  function groups(status) {
    const index = new Map(sessions.map((session, i) => [session.id, i]))
    const grouped = { shell: [], mcp: [], delegate: [] }
    for (const worker of status.workerMetrics) {
      const i = index.get(worker.sessionId)
      if (i === undefined) throw new Error(`unknown Worker: ${worker.sessionId}`)
      grouped[i < 8 ? 'shell' : i < 14 ? 'mcp' : 'delegate'].push(worker.memory)
    }
    return Object.fromEntries(Object.entries(grouped).map(([name, workers]) => [name, {
      count: workers.length,
      rssMiB: +(workers.reduce((sum, item) => sum + item.rssBytes, 0) / 1048576).toFixed(1),
      sumProcessPeakRssMiB: +(workers.reduce((sum, item) => sum + (item.peakRssBytes || 0), 0) / 1048576).toFixed(1),
      heapMiB: +(workers.reduce((sum, item) => sum + item.heapBytes, 0) / 1048576).toFixed(1),
      heapInuseMiB: +(workers.reduce((sum, item) => sum + item.heapInuseBytes, 0) / 1048576).toFixed(1),
      heapIdleMiB: +(workers.reduce((sum, item) => sum + item.heapIdleBytes, 0) / 1048576).toFixed(1),
      heapReleasedMiB: +(workers.reduce((sum, item) => sum + item.heapReleasedBytes, 0) / 1048576).toFixed(1),
      stackInuseMiB: +(workers.reduce((sum, item) => sum + (item.stackInuseBytes || 0), 0) / 1048576).toFixed(1),
      otherSysMiB: +(workers.reduce((sum, item) => sum + (item.otherSysBytes || 0), 0) / 1048576).toFixed(1),
      sysMiB: +(workers.reduce((sum, item) => sum + (item.sysBytes || 0), 0) / 1048576).toFixed(1),
      heapObjects: workers.reduce((sum, item) => sum + item.heapObjects, 0),
      gcCount: workers.reduce((sum, item) => sum + (item.gcCount ?? 0), 0),
      goroutines: workers.reduce((sum, item) => sum + item.goroutines, 0),
    }]))
  }
  await delay(5500)
  const idle = await api('/status')
  if (idle.sessions.open !== 20 || idle.sessions.closed !== closedSessionCount || idle.sessions.workers !== 20) throw new Error(`idle Sessions: ${JSON.stringify(idle.sessions)}`)
  const workerPIDs = status => Object.fromEntries(status.workerMetrics.map(item => [item.sessionId, item.pid]).sort(([a], [b]) => a.localeCompare(b)))
  const originalWorkers = workerPIDs(idle)
  if (Object.keys(originalWorkers).length !== 20 || Object.values(originalWorkers).some(pid => !Number.isInteger(pid) || pid <= 0)) throw new Error('idle Worker identities are incomplete')
  const samples = []
  const checkpoints = []
  const sample = setInterval(async () => {
    try {
      const status = await api('/status')
      samples.push({ running: status.sessions.running, ...memory(status) })
    } catch {}
  }, 500)
  try {
    for (let round = 0; round < rounds; round++) {
      const runs = await Promise.all(sessions.map((session, i) => api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `${i < 8 ? 'shell' : i < 14 ? 'mcp' : 'delegate'}:${i}:${round}` }) })))
      const results = await Promise.all(sessions.map(async (session, index) => {
        let cursor = runs[index].event.sequence
        const tools = []
        for (let poll = 0; poll < 900; poll++) {
          const events = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
          for (const event of events) {
            cursor = event.sequence
            if (event.kind === 'tool' && event.data.runId === runs[index].runId && event.data.status !== 'started') tools.push(event.data)
            if (event.kind === 'run_finished' && event.data.runId === runs[index].runId) return { finished: event.data, tools }
          }
          await delay(50)
        }
        throw new Error(`Session ${index} did not finish round ${round}`)
      }))
      const failures = results.filter((result, index) => result.finished.status !== 'succeeded' || result.tools.length !== (index < 8 ? 1 : 2) || result.tools.some(item => item.status !== 'succeeded'))
      if (failures.length) throw new Error(`mixed round ${round} failed: ${JSON.stringify(failures.map(result => ({ status: result.finished.status, message: result.finished.message?.content, tools: result.tools.map(item => ({ name: item.tool?.name, status: item.status })) })))}`)
      await delay(5500)
      const status = await api('/status')
      if (JSON.stringify(workerPIDs(status)) !== JSON.stringify(originalWorkers)) throw new Error(`a Worker restarted during mixed round ${round + 1}`)
      checkpoints.push({ round: round + 1, ...memory(status), cgroup: cgroupMemory(), groups: groups(status) })
      if (process.env.VISTA_MIXED_PROGRESS === '1') console.error(JSON.stringify(checkpoints.at(-1)))
    }
  } finally {
    clearInterval(sample)
  }
  const after = await api('/status')
  if (JSON.stringify(workerPIDs(after)) !== JSON.stringify(originalWorkers)) throw new Error('a Worker restarted before the final mixed-load check')
  if (after.sessions.open !== 20 || after.sessions.closed !== closedSessionCount || after.sessions.workers !== 20 || after.sessions.running !== 0 || counts.shell !== 16 * rounds || counts.mcp !== 18 * rounds || counts.delegate !== 18 * rounds || counts.child !== 6 * rounds || !samples.some(item => item.running === 20)) throw new Error(`mixed state: ${JSON.stringify(after.sessions)}, calls=${JSON.stringify(counts)}`)
  const finalCgroup = cgroupMemory()
  if (requiredCgroupMiB && finalCgroup?.maxMiB !== requiredCgroupMiB) throw new Error(`cgroup limit disappeared or changed: ${finalCgroup?.maxMiB ?? 'none'}`)
  if (initialCgroup && finalCgroup && (finalCgroup.oom > initialCgroup.oom || finalCgroup.oomKill > initialCgroup.oomKill)) throw new Error(`cgroup OOM events increased: ${JSON.stringify({ initialCgroup, finalCgroup })}`)
  console.log(JSON.stringify({ rounds, mcpTransport, closedSessionCount, idle: { ...memory(idle), groups: groups(idle) }, sampledPeak: samples.reduce((best, item) => item.workerRSSMiB > best.workerRSSMiB ? item : best, samples[0]), checkpoints, after: memory(after), cgroup: finalCgroup, sessions: after.sessions, calls: counts, workerPIDs: originalWorkers, workerIdentityChecks: rounds + 1 }, null, 2))
  if (diagnosticHoldMs) await delay(diagnosticHoldMs)
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  if (fixtureProcess?.pid && fixtureProcess.exitCode === null && fixtureProcess.signalCode === null) {
    fixtureProcess.kill('SIGTERM')
    await new Promise(resolve => fixtureProcess.once('exit', resolve))
  }
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
