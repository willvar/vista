import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { spawn, execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { createServer as createTCPServer, connect } from 'node:net'

// Agent roundtrips against independently published MCP servers.
// make test mcp installs pinned packages; explicit entry paths are also accepted.
const filesystem = process.env.VISTA_MCP_FILESYSTEM_ENTRY || join(process.cwd(), 'scripts', 'mcp_upstream', 'node_modules', '@modelcontextprotocol', 'server-filesystem', 'dist', 'index.js')
const everything = process.env.VISTA_MCP_EVERYTHING_ENTRY || join(process.cwd(), 'scripts', 'mcp_upstream', 'node_modules', '@modelcontextprotocol', 'server-everything', 'dist', 'index.js')
if (!isAbsolute(filesystem) || !isAbsolute(everything)) throw new Error('MCP upstream entries must be absolute')
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-mcp-upstream-'))
const sample = join(state, 'sample.txt')
writeFileSync(sample, 'vista upstream filesystem result\n')
// The upstream example listens on all interfaces for numeric PORT values.
// Give it a Unix socket and expose only a loopback proxy to Vista.
const socket = join(state, 'everything.sock')
const proxy = createTCPServer(client => {
  const upstream = connect(socket)
  upstream.on('error', () => client.destroy())
  client.on('error', () => upstream.destroy())
  client.pipe(upstream).pipe(client)
})
await new Promise(resolve => proxy.listen(0, '127.0.0.1', resolve))
const port = proxy.address().port
const remote = spawn(process.execPath, [everything, 'streamableHttp'], { env: { ...process.env, PORT: socket }, stdio: ['ignore', 'pipe', 'pipe'] })
let remoteLog = ''
remote.stdout.on('data', chunk => { remoteLog = (remoteLog + chunk.toString()).slice(-4096) })
let provider
let started = false
try {
  await Promise.race([
    new Promise((resolve, reject) => {
      remote.stderr.on('data', chunk => {
        remoteLog = (remoteLog + chunk.toString()).slice(-4096)
        if (remoteLog.includes('MCP Streamable HTTP Server listening')) resolve()
      })
      remote.once('error', reject)
      remote.once('exit', code => reject(new Error(`upstream HTTP MCP exited ${code}: ${remoteLog}`)))
    }),
    new Promise((_, reject) => setTimeout(() => reject(new Error(`upstream HTTP MCP did not start: ${remoteLog}`)), 10000))
  ])
  const counts = { filesystem: 0, 'filesystem-error': 0, everything: 0, 'everything-cancel': 0, 'everything-timeout': 0, 'everything-crash': 0 }
  let providerError
  let longCallIssued = false
  let timeoutCallIssued = false
  let crashCallIssued = false
  provider = createServer((request, response) => {
    try {
      if (request.url !== '/v1/chat/completions') throw new Error(`unexpected route ${request.url}`)
      let raw = ''
      request.on('data', chunk => { raw += chunk })
      request.on('end', () => {
        try {
          const body = JSON.parse(raw)
          if (!body.tools?.some(tool => tool.function?.name === 'mcp_list_tools') || !body.tools?.some(tool => tool.function?.name === 'mcp_call_tool')) throw new Error('MCP Agent tools missing')
          const prompt = body.messages.find(message => message.role === 'user')?.content || ''
          const name = prompt.includes('filesystem error') ? 'filesystem-error' : prompt.includes('filesystem') ? 'filesystem' : prompt.includes('everything cancel') ? 'everything-cancel' : prompt.includes('everything timeout') ? 'everything-timeout' : prompt.includes('everything crash') ? 'everything-crash' : 'everything'
          const results = body.messages.filter(message => message.role === 'tool')
          counts[name]++
          if (results.length === 0) return tool('mcp_list_tools', { server: name.startsWith('filesystem') ? 'filesystem' : 'everything' })
          if (name === 'everything-cancel' || name === 'everything-timeout' || name === 'everything-crash') {
            if (name === 'everything-cancel' && longCallIssued) throw new Error('cancelled MCP call was replayed or the Agent continued')
            if (name === 'everything-crash' && crashCallIssued) throw new Error('crashed MCP call was replayed or the Agent continued')
            if (name === 'everything-timeout' && timeoutCallIssued) {
              const result = JSON.parse(results.at(-1).content)
              if (!/(deadline|timeout)/i.test(result?.error || '')) throw new Error(`MCP timeout was not reported: ${results.at(-1).content}`)
              return send({ delta: { content: `${name} upstream MCP complete` }, finish_reason: 'stop' })
            }
            const page = JSON.parse(results.at(-1).content)
            const target = 'trigger-long-running-operation'
            if (page.tools?.some(item => item.name === target)) {
              if (name === 'everything-cancel') longCallIssued = true
              else if (name === 'everything-crash') crashCallIssued = true
              else timeoutCallIssued = true
              return tool('mcp_call_tool', { server: 'everything', name: target, arguments: { duration: name === 'everything-timeout' ? 35 : 5, steps: 1 } })
            }
            if (!page.pageComplete || page.nextCursor) return tool('mcp_list_tools', { server: 'everything', cursor: page.nextCursor, offset: page.pageComplete ? 0 : page.nextOffset })
            throw new Error(`everything did not expose ${target}`)
          }
          if (results.length === 1) {
            const page = JSON.parse(results[0].content)
            const target = name.startsWith('filesystem') ? 'read_text_file' : 'echo'
            if (!page.tools?.some(item => item.name === target)) throw new Error(`${name} did not expose ${target}: ${results[0].content}`)
            return tool('mcp_call_tool', { server: name.startsWith('filesystem') ? 'filesystem' : 'everything', name: target, arguments: name === 'filesystem-error' ? { path: join(state, 'missing.txt') } : name === 'filesystem' ? { path: sample } : { message: 'vista upstream everything result' } })
          }
          const result = results.length === 2 ? JSON.parse(results[1].content) : null
          if (name === 'filesystem-error') {
            if (result?.isError !== true || result.error !== 'MCP tool reported an error' || !result.content?.includes('ENOENT')) throw new Error(`${name} error missing: ${results.at(-1)?.content}`)
          } else {
            const expected = name === 'filesystem' ? 'vista upstream filesystem result' : 'Echo: vista upstream everything result'
            if (result?.isError !== false || !result.content?.includes(expected)) throw new Error(`${name} result missing: ${results.at(-1)?.content}`)
          }
          return send({ delta: { content: `${name} upstream MCP complete` }, finish_reason: 'stop' })
          function tool(toolName, args) {
            return send({ delta: { tool_calls: [{ index: 0, id: `call_${counts[name]}`, type: 'function', function: { name: toolName, arguments: JSON.stringify(args) } }] }, finish_reason: 'tool_calls' })
          }
          function send(choice) {
            response.writeHead(200, { 'Content-Type': 'text/event-stream' })
            response.end(`data: ${JSON.stringify({ choices: [choice] })}\n\ndata: [DONE]\n\n`)
          }
        } catch (error) {
          providerError = error
          response.writeHead(500)
          response.end(error.message)
        }
      })
    } catch (error) {
      response.writeHead(500)
      response.end(error.message)
    }
  })
  await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
  const vistaConfig = join(state, 'vista')
  mkdirSync(vistaConfig, { mode: 0o700 })
  writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'mock', models: [{ key: 'mock', provider: 'openai-chat', id: 'mock', apiKey: 'fake', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
  writeFileSync(join(vistaConfig, 'mcp.json'), JSON.stringify([
    { id: 'filesystem', name: 'filesystem', transport: 'stdio', command: process.execPath, args: [filesystem, state], directory: state },
    { id: 'everything', name: 'everything', transport: 'http', endpoint: `http://127.0.0.1:${port}/mcp` }
  ]), { mode: 0o600 })
  prepareVistaTestConfig(state)
  const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    const body = await response.text()
    return body ? JSON.parse(body) : null
  }
  for (const name of ['filesystem', 'filesystem-error', 'everything']) {
    const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `${name} MCP` }) })
    const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `Use the ${name.replace('-', ' ')} MCP tool` }) })
    let finished
    let toolFailed = false
    for (let i = 0; i < 600; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
      if (events.some(event => event.kind === 'tool' && event.data.status === 'failed' && event.data.tool?.name === 'mcp_call_tool')) toolFailed = true
      finished = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
      if (finished) break
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    if (finished?.data.status !== 'succeeded' || finished.data.message?.content !== `${name} upstream MCP complete` || counts[name] !== 3) throw new Error(`${name} Agent roundtrip failed: ${JSON.stringify(finished?.data)}, model calls=${counts[name]}, provider error=${providerError}`)
    if (name === 'filesystem-error' && !toolFailed) throw new Error('upstream MCP tool error was not recorded as a failed tool')
  }
  const cancelSession = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'everything cancellation' }) })
  const cancelRun = await api(`/sessions/${cancelSession.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Use the everything cancel MCP tool' }) })
  let callStarted = false
  for (let i = 0; i < 400; i++) {
    const events = await api(`/sessions/${cancelSession.id}/event-page?after=${cancelRun.event.sequence}`)
    if (events.some(event => event.kind === 'tool' && event.data.status === 'started' && event.data.tool?.name === 'mcp_call_tool')) { callStarted = true; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!callStarted || !longCallIssued) throw new Error(`upstream long-running MCP call did not start: started=${callStarted}, model calls=${counts['everything-cancel']}, provider error=${providerError}`)
  const callsAtCancel = counts['everything-cancel']
  await new Promise(resolve => setTimeout(resolve, 300))
  await api(`/sessions/${cancelSession.id}/cancel`, { method: 'POST' })
  let cancelled = false
  for (let i = 0; i < 80; i++) {
    const events = await api(`/sessions/${cancelSession.id}/event-page?after=${cancelRun.event.sequence}`)
    const finish = events.find(event => event.kind === 'run_finished' && event.data.runId === cancelRun.runId)
    if (finish) { cancelled = finish.data.status === 'interrupted'; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!cancelled || counts['everything-cancel'] !== callsAtCancel) throw new Error('upstream MCP cancellation did not interrupt the run without replay')
  await new Promise(resolve => setTimeout(resolve, 5300))
  if (counts['everything-cancel'] !== callsAtCancel) throw new Error('upstream MCP completion resumed a cancelled Agent run')
  const timeoutSession = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'everything timeout' }) })
  const timeoutRun = await api(`/sessions/${timeoutSession.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Use the everything timeout MCP tool' }) })
  let timeoutFinish
  let timeoutToolFailed = false
  for (let i = 0; i < 1600; i++) {
    const events = await api(`/sessions/${timeoutSession.id}/event-page?after=${timeoutRun.event.sequence}`)
    if (events.some(event => event.kind === 'tool' && event.data.status === 'failed' && event.data.tool?.name === 'mcp_call_tool')) timeoutToolFailed = true
    timeoutFinish = events.find(event => event.kind === 'run_finished' && event.data.runId === timeoutRun.runId)
    if (timeoutFinish) break
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!timeoutCallIssued || !timeoutToolFailed || timeoutFinish?.data.status !== 'succeeded' || timeoutFinish.data.message?.content !== 'everything-timeout upstream MCP complete' || providerError) throw new Error(`upstream MCP timeout was not handled: ${JSON.stringify(timeoutFinish?.data)}, model calls=${counts['everything-timeout']}, provider error=${providerError}`)
  const callsAtTimeout = counts['everything-timeout']
  await new Promise(resolve => setTimeout(resolve, 5300))
  if (counts['everything-timeout'] !== callsAtTimeout) throw new Error('upstream MCP completion replayed a timed-out tool call')
  const crashSession = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'everything Worker crash' }) })
  const crashRun = await api(`/sessions/${crashSession.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Use the everything crash MCP tool' }) })
  let crashToolStarted = false
  for (let i = 0; i < 400; i++) {
    const events = await api(`/sessions/${crashSession.id}/event-page?after=${crashRun.event.sequence}`)
    if (events.some(event => event.kind === 'tool' && event.data.status === 'started' && event.data.tool?.name === 'mcp_call_tool')) { crashToolStarted = true; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!crashToolStarted || !crashCallIssued) throw new Error('upstream MCP call was not started before the Worker crash')
  const beforeCrash = await api('/status')
  const workerPid = beforeCrash.workerMetrics.find(item => item.sessionId === crashSession.id)?.pid
  if (!workerPid) throw new Error('crash Session Worker PID was unavailable')
  const callsAtCrash = counts['everything-crash']
  await new Promise(resolve => setTimeout(resolve, 300))
  process.kill(workerPid, 'SIGKILL')
  let recovered = false
  for (let i = 0; i < 400; i++) {
    const status = await api('/status')
    const events = await api(`/sessions/${crashSession.id}/event-page?after=${crashRun.event.sequence}`)
    const interrupted = events.some(event => event.kind === 'run_finished' && event.data.runId === crashRun.runId && event.data.status === 'interrupted')
    const unknown = events.some(event => event.kind === 'tool' && event.data.runId === crashRun.runId && event.data.status === 'unknown' && event.data.tool?.name === 'mcp_call_tool')
    const replacement = status.workerMetrics.find(item => item.sessionId === crashSession.id)?.pid
    if (interrupted && unknown && replacement && replacement !== workerPid) { recovered = true; break }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (!recovered || counts['everything-crash'] !== callsAtCrash) throw new Error('Worker crash did not interrupt the MCP call without replay')
  await new Promise(resolve => setTimeout(resolve, 5300))
  if (counts['everything-crash'] !== callsAtCrash) throw new Error('upstream MCP completion resumed a crashed Agent run')
  console.log('Upstream filesystem stdio and everything Streamable HTTP Agent roundtrips, tool error, cancellation, timeout, and Worker crash passed')
  await promisify(execFile)(binary, ['stop'], { env })
  started = false
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  console.error(remoteLog)
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env: { ...process.env, XDG_CONFIG_HOME: state, VISTA_NO_BROWSER: '1' } }) } catch {}
  if (provider) await new Promise(resolve => provider.close(resolve))
  remote.kill('SIGTERM')
  if (remote.exitCode === null && remote.signalCode === null) await new Promise(resolve => remote.once('exit', resolve))
  await new Promise(resolve => proxy.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
