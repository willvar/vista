import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// A slow Copilot inventory must not prevent the Linux CLI from reporting a ready Server.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-copilot-startup-'))
let inventoryCalls = 0
let badRequests = 0
const inventory = createServer(async (request, response) => {
  inventoryCalls++
  if (request.url !== '/models' || request.headers.authorization !== 'Bearer test-copilot-token') {
    badRequests++
    response.writeHead(400); response.end('wrong discovery request'); return
  }
  await new Promise(resolve => setTimeout(resolve, 3000))
  response.writeHead(200, { 'Content-Type': 'application/json' })
  response.end(JSON.stringify({ data: [{ id: 'new-copilot-model', model_picker_enabled: true, supported_endpoints: ['/responses'], capabilities: { limits: { max_prompt_tokens: 4000, max_output_tokens: 500 }, supports: { tool_calls: true } } }] }))
})
await new Promise(resolve => inventory.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'copilot', models: [{ key: 'copilot', provider: 'catalog', providerId: 'github-copilot', id: 'gpt-6-sol', apiKey: 'test-copilot-token', baseUrl: `http://127.0.0.1:${inventory.address().port}` }] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
try {
  const began = Date.now()
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8', timeout: 8000 })).stdout.trim()
  const startMs = Date.now() - began
  if (startMs > 2500) throw new Error(`slow Copilot discovery delayed CLI readiness: ${startMs} ms`)
  const endpoint = new URL(url).origin
  let discovered = false
  let observedModels = []
  for (let i = 0; i < 120; i++) {
    const response = await fetch(endpoint + '/api/models', {})
    if (!response.ok) throw new Error(`model list HTTP ${response.status}`)
    const models = (await response.json()).models
    observedModels = models.map(model => ({ id: model.id, discoverySource: model.discoverySource }))
    if (models.some(model => model.id === 'new-copilot-model' && model.discoverySource === 'github-copilot')) {
      discovered = true
      break
    }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!discovered) throw new Error(`asynchronous Copilot inventory never appeared: ${JSON.stringify({ inventoryCalls, badRequests, observedModels })}`)
  console.log(`Copilot slow discovery did not block CLI startup (${startMs} ms); discovered model became visible`)
} catch (error) {
  try { console.error(readFileSync(join(vistaConfig, 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  try { await promisify(execFile)(binary, ['stop'], { env, timeout: 10000 }) } catch {}
  await new Promise(resolve => inventory.close(resolve))
  rmSync(state, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}
