import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { spawn, execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// Vista Agent -> independent SDK MCP servers over stdio and Streamable HTTP.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const fixture = process.env.VISTA_MCP_FIXTURE || join(process.cwd(), 'bin', 'mcp-fixture')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-mcp-e2e-'))
const largeInput = 'x'.repeat(2048)
const largeOutput = `fixture:${largeInput.repeat(64)}`
const remote = spawn(fixture, ['http'], { stdio: ['ignore', 'pipe', 'pipe'] })
let remoteError = ''
remote.stderr.on('data', chunk => { remoteError += chunk.toString().slice(0, 2048) })
let provider
let started = false
let providerError
try {
  const address = await Promise.race([
    new Promise((resolve, reject) => {
      remote.stdout.once('data', chunk => resolve(chunk.toString().trim()))
      remote.once('error', reject)
      remote.once('exit', code => reject(new Error(`remote MCP exited ${code}: ${remoteError}`)))
    }),
    new Promise((_, reject) => setTimeout(() => reject(new Error('remote MCP did not start')), 5000))
  ])
  const counts = { local: 0, remote: 0, 'local-large': 0, 'remote-large': 0 }
  provider = createServer((request, response) => {
    handleProvider(request, response).catch(error => {
      providerError = error
      response.destroy()
    })
  })
  async function handleProvider(request, response) {
    if (request.method === 'GET' && request.url === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'mock' }] }))
      return
    }
    if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
      response.writeHead(404).end()
      return
    }
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    if (!body.tools?.some(tool => tool.function?.name === 'mcp_list_tools') || !body.tools?.some(tool => tool.function?.name === 'mcp_call_tool')) throw new Error('MCP Agent tools missing')
    const prompt = body.messages.find(message => message.role === 'user')?.content || ''
    const name = prompt.includes('local') ? 'local' : 'remote'
    const large = prompt.includes('large')
    const key = large ? `${name}-large` : name
    const system = body.messages.find(message => message.role === 'system')?.content || ''
    const expectedServers = 'Configured MCP servers: ' + JSON.stringify(key === 'local' ? [{ id: 'local', name: 'Local tools' }] : [{ id: 'local', name: 'Renamed local tools' }, { id: 'remote', name: 'Remote tools' }])
    if (!system.includes(expectedServers)) throw new Error(`MCP hot configuration snapshot missing: expected ${expectedServers}`)
    const results = body.messages.filter(message => message.role === 'tool')
    counts[key]++
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    if (results.length === 0) return tool('mcp_list_tools', { server: name })
    if (results.length === 1) {
      if (key === 'local') {
        await api('/mcp-config', { method: 'PUT', body: JSON.stringify({ ...mcpConfigs[0], name: 'Renamed local tools' }) })
        const renamed = (await api('/mcp-config')).servers.find(server => server.id === 'local')
        if (renamed.name !== 'Renamed local tools' || renamed.check.status !== 'available') throw new Error('Renaming the active MCP server lost its identity or connection check')
      }
      const page = JSON.parse(results[0].content)
      if (!page.tools?.some(item => item.name === 'echo') || !page.nextCursor) throw new Error(`${name} MCP first page failed: ${results[0].content}`)
      return tool('mcp_list_tools', { server: name, cursor: page.nextCursor })
    }
    if (results.length === 2) {
      const page = JSON.parse(results[1].content)
      if (!page.tools?.some(item => item.name === 'z_extra') || page.nextCursor) throw new Error(`${name} MCP second page failed: ${results[1].content}`)
      return tool('mcp_call_tool', { server: name, name: 'echo', arguments: { text: large ? largeInput : name, repeat: large ? 64 : 1 } })
    }
    const result = results.length === 3 ? JSON.parse(results[2].content) : null
    if (large ? !result?.artifactId || result.outputPath !== undefined || !result?.complete || !result.truncated || result.bytes < largeOutput.length : !result?.content?.includes(`fixture:${name}`)) throw new Error(`${key} MCP tool result missing: ${results.at(-1)?.content?.slice(0, 1024)}`)
    return text(`MCP ${key} complete`)
    function tool(toolName, args) {
      const event = { choices: [{ delta: { tool_calls: [{ index: 0, id: `call_${counts[key]}`, type: 'function', function: { name: toolName, arguments: JSON.stringify(args) } }] }, finish_reason: 'tool_calls' }] }
      response.end(`data: ${JSON.stringify(event)}\n\ndata: [DONE]\n\n`)
    }
    function text(content) {
      response.end(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`)
    }
  }
  await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
  const vistaConfig = join(state, 'vista')
  mkdirSync(vistaConfig, { mode: 0o700 })
  writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'mock', models: [{ key: 'mock', provider: 'openai-chat', id: 'mock', apiKey: 'fake', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` }] }), { mode: 0o600 })
  const mcpConfigs = [
    { id: 'local', name: 'Local tools', transport: 'stdio', command: fixture, args: ['stdio'], directory: state },
    { id: 'remote', name: 'Remote tools', transport: 'http', endpoint: `http://${address}/mcp`, token: 'fixture-token' }
  ]
  prepareVistaTestConfig(state)
  const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.status === 204 ? null : response.json()
  }
  if ((await api('/mcp-config')).servers.length) throw new Error('MCP test must start without saved services')
  // Add services after startup, using the same APIs as WebUI. Existing Workers
  // also take the latest snapshot at their next execution boundary.
  const warmSession = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'local MCP' }) })
  for (const config of mcpConfigs) {
    await api('/mcp-config', { method: 'POST', body: JSON.stringify(config) })
    const checked = await api(`/mcp-servers/${config.id}/check`, { method: 'POST' })
    if (checked.check.status !== 'available' || checked.tools.length !== 2 || checked.tools[1].name !== 'z_extra') throw new Error(`${config.name} MCP settings did not discover every page: ${JSON.stringify(checked)}`)
  }
  const settings = await api('/mcp-config')
  if (settings.servers.length !== 2 || JSON.stringify(settings).includes('fixture-token') || !settings.servers.find(server => server.id === 'remote')?.hasToken) throw new Error('MCP settings credential masking failed')
  await api('/mcp-config', { method: 'PATCH', body: JSON.stringify({ id: 'remote', disabled: true }) })
  for (const key of ['local', 'remote', 'local-large', 'remote-large']) {
    if (key === 'remote') await api('/mcp-config', { method: 'PATCH', body: JSON.stringify({ id: 'remote', disabled: false }) })
    const session = key === 'local' ? warmSession : await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `${key} MCP` }) })
    const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `Use the ${key.replace('-', ' ')} MCP echo tool` }) })
    let finished
    for (let i = 0; i < 600; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${run.event.sequence}`)
      finished = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
      if (finished) break
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    if (finished?.data.status !== 'succeeded' || finished.data.message?.content !== `MCP ${key} complete` || counts[key] !== 4 || providerError) throw new Error(`${key} MCP Agent roundtrip failed: ${JSON.stringify(finished?.data)}, calls=${counts[key]}, provider error=${providerError}`)
    if (key.endsWith('-large')) {
      const artifacts = await api(`/sessions/${session.id}/artifacts`)
      const artifact = artifacts.find(item => item.runId === run.runId && item.status === 'succeeded' && item.complete)
      if (!artifact || artifact.bytes < largeOutput.length) throw new Error(`${key} full result artifact was not persisted`)
      let recovered = ''
      let offset = 0
      while (offset < artifact.bytes) {
        const page = await api(`/sessions/${session.id}/artifacts/${artifact.id}/output?offset=${offset}`)
        if (!page.complete || page.nextOffset <= offset) throw new Error(`${key} full result artifact page did not advance`)
        recovered += page.content
        offset = page.nextOffset
      }
      if (JSON.parse(recovered).content !== largeOutput) throw new Error(`${key} full result artifact differs from the MCP output`)
    }
  }
  await api('/mcp-config', { method: 'DELETE', body: JSON.stringify({ id: 'local' }) })
  await api('/mcp-config', { method: 'DELETE', body: JSON.stringify({ id: 'remote' }) })
  if ((await api('/mcp-config')).servers.length) throw new Error('MCP service deletion did not take effect')
  console.log('MCP settings hot updates, in-flight renaming, stdio/HTTP tool discovery, Agent roundtrips and large artifacts passed')
  await promisify(execFile)(binary, ['stop'], { env })
  started = false
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env: { ...process.env, XDG_CONFIG_HOME: state, VISTA_NO_BROWSER: '1' } }) } catch {}
  if (provider) await new Promise(resolve => provider.close(resolve))
  remote.kill('SIGTERM')
  if (remote.exitCode === null && remote.signalCode === null) await new Promise(resolve => remote.once('exit', resolve))
  rmSync(state, { recursive: true, force: true })
}
