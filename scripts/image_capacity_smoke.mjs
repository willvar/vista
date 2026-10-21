import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { bedrockReply } from './bedrock_mock.mjs'

// Backend-only synthetic image load; no browser or live Provider is started.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const protocol = process.env.VISTA_IMAGE_PROTOCOL || 'openai-chat'
if (!['openai-chat', 'mistral', 'cohere', 'bedrock', 'vercel-gateway', 'bedrock-mantle-responses', 'sap-ai-core', 'copilot'].includes(protocol)) throw new Error('unsupported VISTA_IMAGE_PROTOCOL')
const requiredCgroupMiB = Number(process.env.VISTA_REQUIRE_CGROUP_MIB || 0)
if (!Number.isInteger(requiredCgroupMiB) || requiredCgroupMiB < 0) throw new Error('VISTA_REQUIRE_CGROUP_MIB must be a nonnegative integer')
function cgroupMemory() {
  const line = readFileSync('/proc/self/cgroup', 'utf8').split('\n').find(item => item.startsWith('0::'))
  if (!line) return null
  const root = join('/sys/fs/cgroup', line.slice(3).replace(/^\//, ''))
  const read = name => readFileSync(join(root, name), 'utf8').trim()
  const events = Object.fromEntries(read('memory.events').split('\n').filter(Boolean).map(item => item.split(/\s+/)))
  const mib = value => +(Number(value) / 1048576).toFixed(1)
  return { maxMiB: read('memory.max') === 'max' ? null : mib(read('memory.max')), currentMiB: mib(read('memory.current')), peakMiB: mib(read('memory.peak')), oom: Number(events.oom || 0), oomKill: Number(events.oom_kill || 0) }
}
const initial = cgroupMemory()
if (requiredCgroupMiB && initial?.maxMiB !== requiredCgroupMiB) throw new Error(`expected ${requiredCgroupMiB} MiB cgroup, got ${initial?.maxMiB ?? 'none'}`)
const state = mkdtempSync(join(process.env.VISTA_TEST_TMPDIR || process.cwd(), 'vista-image-capacity-'))
const png = Buffer.concat([Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=', 'base64'), Buffer.alloc((1 << 20) - 68)])
if (png.length !== 1 << 20) throw new Error('image fixture length changed')
const encoded = png.toString('base64')
let calls = 0
let summaryCalls = 0
const requestBytes = { first: 0, last: 0, max: 0 }
const provider = createServer(async (request, response) => {
  try {
    if (protocol === 'sap-ai-core' && request.url === '/oauth/token') {
      if (request.headers.authorization !== `Basic ${Buffer.from('test-client:test-secret').toString('base64')}`) throw new Error('SAP token auth changed')
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end('{"access_token":"test-run-token"}')
      return
    }
    if (protocol === 'copilot' && request.url === '/models') {
      if (request.headers.authorization !== 'Bearer test-token') throw new Error('Copilot model discovery credential changed')
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end('{"data":[{"id":"gpt-6-sol","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":8000,"max_output_tokens":1000},"supports":{"tool_calls":true,"vision":true}}}]}')
      return
    }
    if (protocol === 'bedrock' ? !/^\/model\/.+\/converse-stream$/.test(request.url) : request.url !== (protocol === 'sap-ai-core' ? '/v2/inference/deployments/deployment-one/v2/completion' : protocol === 'bedrock-mantle-responses' ? '/openai/v1/responses' : protocol === 'copilot' ? '/responses' : protocol === 'vercel-gateway' ? '/v3/ai/language-model' : protocol === 'cohere' ? '/v2/chat' : '/v1/chat/completions')) throw new Error(`wrong route ${request.url}`)
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    const parts = (protocol === 'sap-ai-core' ? body.config?.modules?.prompt_templating?.prompt?.template : protocol === 'bedrock-mantle-responses' || protocol === 'copilot' ? body.input : protocol === 'vercel-gateway' ? body.prompt : body.messages).at(-1).content
    const summary = protocol === 'bedrock' ? !parts.some(part => part.image) : protocol === 'sap-ai-core' ? !Array.isArray(parts) : protocol === 'vercel-gateway' ? !parts.some(part => part.type === 'file') : !Array.isArray(parts)
    const imageCount = raw.split(encoded).length - 1
    if (imageCount !== (summary ? 0 : 2)) throw new Error(`expected only current images in request, got ${imageCount}`)
    if (summary) summaryCalls++
    else {
      const size = Buffer.byteLength(raw)
      if (!requestBytes.first) requestBytes.first = size
      requestBytes.last = size
      requestBytes.max = Math.max(requestBytes.max, size)
      if (protocol === 'bedrock') {
        if (parts.length !== 3 || parts[1]?.image?.format !== 'png' || parts[2]?.image?.format !== 'png' || parts[1]?.image?.source?.bytes !== encoded || parts[2]?.image?.source?.bytes !== encoded) throw new Error('Bedrock image payload changed')
      } else if (protocol === 'bedrock-mantle-responses' || protocol === 'copilot') {
        if (parts.length !== 3 || parts[1]?.type !== 'input_image' || parts[2]?.type !== 'input_image' || parts[1]?.image_url !== `data:image/png;base64,${encoded}` || parts[2]?.image_url !== `data:image/png;base64,${encoded}`) throw new Error('Responses image payload changed')
        if (protocol === 'copilot' && (request.headers.authorization !== 'Bearer test-token' || request.headers['copilot-vision-request'] !== 'true')) throw new Error('Copilot vision header or credential missing')
      } else if (protocol === 'sap-ai-core') {
        if (request.headers.authorization !== 'Bearer test-run-token' || parts.length !== 3 || parts[1]?.type !== 'image_url' || parts[2]?.type !== 'image_url' || parts[1]?.image_url?.url !== `data:image/png;base64,${encoded}` || parts[2]?.image_url?.url !== `data:image/png;base64,${encoded}`) throw new Error('SAP image payload or auth changed')
      } else if (protocol === 'vercel-gateway') {
        if (parts.length !== 3 || parts[1]?.type !== 'file' || parts[2]?.type !== 'file' || parts[1]?.mediaType !== 'image/png' || parts[2]?.mediaType !== 'image/png' || parts[1]?.data !== `data:image/png;base64,${encoded}` || parts[2]?.data !== `data:image/png;base64,${encoded}`) throw new Error('Gateway v3 file parts changed')
      } else {
        const imageURL = part => protocol === 'mistral' ? part.image_url : part.image_url?.url
        if (parts.length !== 3 || imageURL(parts[1]) !== `data:image/png;base64,${encoded}` || imageURL(parts[2]) !== `data:image/png;base64,${encoded}`) throw new Error('model image payload changed')
      }
      calls++
    }
    await new Promise(resolve => setTimeout(resolve, !summary && calls <= 20 ? 5000 : 20))
    response.writeHead(200, { 'Content-Type': protocol === 'bedrock' ? 'application/vnd.amazon.eventstream' : 'text/event-stream' })
    const answer = summary ? 'Earlier images were attached.' : 'Seen two images.'
    response.end(protocol === 'bedrock' ? bedrockReply(answer) : protocol === 'sap-ai-core'
      ? `data: {"final_result":{"choices":[{"delta":{"content":"${answer}"},"finish_reason":"stop_sequence"}]}}\n\n`
      : protocol === 'bedrock-mantle-responses' || protocol === 'copilot'
      ? `data: {"type":"response.output_text.delta","item_id":"msg","delta":"${answer}"}\n\ndata: {"type":"response.completed","response":{"status":"completed","output":[]}}\n\n`
      : protocol === 'vercel-gateway'
      ? `data: {"type":"text-delta","id":"text_1","delta":"${answer}"}\n\ndata: {"type":"finish","finishReason":{"unified":"stop"}}\n\n`
      : protocol === 'cohere'
      ? `data: {"type":"content-delta","delta":{"message":{"content":{"text":"${answer}"}}}}\n\ndata: {"type":"message-end","delta":{"finish_reason":"COMPLETE"}}\n\n`
      : `data: {"choices":[{"delta":{"content":"${answer}"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n`)
  } catch (error) {
    response.writeHead(500)
    response.end(String(error))
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
const vistaConfig = join(state, 'vista')
mkdirSync(vistaConfig, { mode: 0o700 })
const model = protocol === 'bedrock'
  ? { key: 'image', provider: 'catalog', providerId: 'amazon-bedrock', id: 'us.amazon.nova-lite-v1:0', region: 'us-east-1', baseUrl: `http://127.0.0.1:${provider.address().port}` }
  : protocol === 'sap-ai-core'
    ? { key: 'image', provider: 'sap-ai-core', providerId: 'sap-ai-core', id: 'gpt-4.1', apiKey: JSON.stringify({ url: `http://127.0.0.1:${provider.address().port}`, clientid: 'test-client', clientsecret: 'test-secret', serviceurls: { AI_API_URL: `http://127.0.0.1:${provider.address().port}` } }), inputModalities: ['text', 'image'], deploymentId: 'deployment-one', resourceGroup: 'default', baseUrl: `http://127.0.0.1:${provider.address().port}/v2` }
  : protocol === 'bedrock-mantle-responses'
    ? { key: 'image', provider: 'catalog', providerId: 'amazon-bedrock', id: 'openai.gpt-5.4', region: 'us-east-1', baseUrl: `http://127.0.0.1:${provider.address().port}/openai/v1` }
  : protocol === 'copilot'
    ? { key: 'image', provider: 'catalog', providerId: 'github-copilot', id: 'gpt-6-sol', baseUrl: `http://127.0.0.1:${provider.address().port}` }
  : protocol === 'vercel-gateway'
    ? { key: 'image', provider: 'catalog', providerId: 'vercel', id: 'alibaba/qwen3.5-flash', apiKey: 'test', baseUrl: `http://127.0.0.1:${provider.address().port}/v3/ai` }
  : { key: 'image', provider: protocol, id: 'mock', apiKey: 'test', baseUrl: `http://127.0.0.1:${provider.address().port}/${protocol === 'cohere' ? 'v2' : 'v1'}`, inputModalities: ['text', 'image'] }
writeFileSync(join(vistaConfig, 'models.json'), JSON.stringify({ default: 'image', models: [model] }), { mode: 0o600 })
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
if (protocol === 'bedrock' || protocol === 'bedrock-mantle-responses') Object.assign(env, { AWS_ACCESS_KEY_ID: 'test-access', AWS_SECRET_ACCESS_KEY: 'test-secret', AWS_REGION: 'us-east-1', AWS_BEARER_TOKEN_BEDROCK: '' })
if (protocol === 'sap-ai-core') env.AICORE_SERVICE_KEY = JSON.stringify({ url: `http://127.0.0.1:${provider.address().port}`, clientid: 'test-client', clientsecret: 'test-secret', serviceurls: { AI_API_URL: `http://127.0.0.1:${provider.address().port}` } })
if (protocol === 'copilot') env.GITHUB_TOKEN = 'test-token'
let started = false
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
    return response.json()
  }
  const sessions = []
  for (let i = 0; i < 20; i++) sessions.push(await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `image load ${i}` }) }))
  const payload = JSON.stringify({ content: 'describe', images: [{ mediaType: 'image/png', data: encoded }, { mediaType: 'image/png', data: encoded }] })
  const runs = await Promise.all(sessions.map(session => api(`/sessions/${session.id}/messages`, { method: 'POST', body: payload })))
  await new Promise(resolve => setTimeout(resolve, 1500))
  const during = await api('/status')
  if (during.sessions.running !== 20 || during.sessions.workers !== 20) throw new Error(`expected 20 active Workers: ${JSON.stringify(during.sessions)}`)
  const results = await Promise.all(sessions.map(async (session, index) => {
    let cursor = runs[index].event.sequence
    for (let poll = 0; poll < 300; poll++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
      for (const event of events) {
        cursor = event.sequence
        if (event.kind === 'run_finished' && event.data.runId === runs[index].runId) return event.data
      }
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    throw new Error(`Session ${index} did not finish`)
  }))
  if (calls !== 20 || results.some(item => item.status !== 'succeeded')) throw new Error(`image runs failed: calls=${calls}, results=${JSON.stringify(results)}`)
  const checkpoints = []
  for (let i = 0; i < 20; i++) {
    const sent = await api(`/sessions/${sessions[0].id}/messages`, { method: 'POST', body: payload })
    let cursor = sent.event.sequence
    let finished = false
    for (let poll = 0; poll < 300 && !finished; poll++) {
      const events = await api(`/sessions/${sessions[0].id}/event-page?after=${cursor}`)
      for (const event of events) {
        cursor = event.sequence
        if (event.kind === 'run_finished' && event.data.runId === sent.runId) {
          if (event.data.status !== 'succeeded') throw new Error(`repeat ${i} failed: ${JSON.stringify(event.data)}`)
          finished = true
        }
      }
      if (!finished) await new Promise(resolve => setTimeout(resolve, 50))
    }
    if (!finished) throw new Error(`repeat ${i} did not finish`)
    if ((i + 1) % 10 === 0) {
      await new Promise(resolve => setTimeout(resolve, 5500))
      const status = await api('/status')
      const worker = status.workerMetrics.find(item => item.sessionId === sessions[0].id)
      checkpoints.push({ turns: i + 1, serverRSSMiB: +(status.memory.rssBytes / 1048576).toFixed(1), workerRSSMiB: +(worker.memory.rssBytes / 1048576).toFixed(1), workerHeapMiB: +(worker.memory.heapBytes / 1048576).toFixed(1), workerGC: worker.memory.gcCount })
    }
  }
  if (calls !== 40) throw new Error(`expected 40 image model requests, got ${calls}`)
  const after = await api('/status')
  const final = cgroupMemory()
  if (requiredCgroupMiB && final?.maxMiB !== requiredCgroupMiB) throw new Error('backend cgroup changed')
  if (initial && final && (final.oom > initial.oom || final.oomKill > initial.oomKill)) throw new Error('backend cgroup reported OOM')
  const mib = value => +(value / 1048576).toFixed(1)
  console.log(JSON.stringify({ sessions: after.sessions, calls, summaryCalls, imageBytesPerRun: png.length * 2, requestBytes, duringServerRSSMiB: mib(during.memory.rssBytes), duringWorkerRSSMiB: mib(during.workerMetrics.reduce((sum, item) => sum + item.memory.rssBytes, 0)), afterServerRSSMiB: mib(after.memory.rssBytes), afterWorkerRSSMiB: mib(after.workerMetrics.reduce((sum, item) => sum + item.memory.rssBytes, 0)), checkpoints, cgroup: final }, null, 2))
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}
