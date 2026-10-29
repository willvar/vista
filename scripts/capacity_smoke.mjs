import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile, execFileSync } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only capacity smoke: no browser is launched, and the Provider is local.
// Set VISTA_BIN to an absolute binary path when running outside the repo.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const extraTurns = Number(process.env.VISTA_EXTRA_TURNS ?? 50)
if (!Number.isInteger(extraTurns) || extraTurns < 0 || extraTurns > 2000) throw new Error('VISTA_EXTRA_TURNS must be an integer from 0 to 2000')
const memoryRegression = process.env.VISTA_MEMORY_REGRESSION === '1'
if (memoryRegression && extraTurns !== 200) throw new Error('VISTA_MEMORY_REGRESSION requires VISTA_EXTRA_TURNS=200')
const state = mkdtempSync(join(process.env.VISTA_TEST_TMPDIR || process.cwd(), 'vista-capacity-'))
let calls = 0
let summaryCalls = 0
const provider = createServer(async (request, response) => {
  if (request.url !== '/v1/chat/completions') throw new Error(request.url)
  let raw = ''
  for await (const chunk of request) raw += chunk
  const summary = !JSON.parse(raw).tools?.length
  if (summary) summaryCalls++
  else calls++
  setTimeout(() => {
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    response.end(`data: {"choices":[{"delta":{"content":"${summary ? 'Summary of earlier turns.' : 'capacity answer'}"},"finish_reason":"stop"}]}\n\ndata: {"choices":[],"usage":{"prompt_tokens":37,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":3}}}\n\ndata: [DONE]\n\n`)
  }, !summary && calls <= 20 ? 9000 : 20)
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
writeFileSync(join(state, 'vista/capacity.json'), JSON.stringify({ budgetMiB: 8192, workerMiB: 1024, concurrentRuns: 20 }), { mode: 0o600 })
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'capacity-key', VISTA_OPENAI_MODEL: 'capacity-test', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1`, ...(memoryRegression ? { GOGC: '30' } : {}) }
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
  function memory(status) {
    const workers = status.workerMetrics.map(item => item.memory)
    if (status.memory.peakRssBytes < status.memory.rssBytes || workers.some(item => item?.sampledAt && item.peakRssBytes < item.rssBytes)) throw new Error('reported peak RSS is below current RSS')
    return { serverRSSMiB: +(status.memory.rssBytes / 1048576).toFixed(1), workerRSSMiB: +(workers.reduce((sum, item) => sum + item.rssBytes, 0) / 1048576).toFixed(1), maxWorkerRSSMiB: +(Math.max(...workers.map(item => item.rssBytes)) / 1048576).toFixed(1), workerHeapMiB: +(workers.reduce((sum, item) => sum + item.heapBytes, 0) / 1048576).toFixed(1) }
  }
  function workerSnapshot(status, sessionId) {
    const item = status.workerMetrics.find(item => item.sessionId === sessionId)
    if (!item?.pid || !item.memory?.sampledAt) throw new Error(`Worker metrics unavailable for ${sessionId}`)
    return { pid: item.pid, rssBytes: item.memory.rssBytes, heapBytes: item.memory.heapBytes, heapObjects: item.memory.heapObjects, goroutines: item.memory.goroutines, gcCount: item.memory.gcCount, peakRssBytes: item.memory.peakRssBytes }
  }
  const sessions = []
  for (let i = 0; i < 20; i++) sessions.push(await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `capacity ${i}` }) }))
  await new Promise(resolve => setTimeout(resolve, 5500))
  const idle = await api('/status')
  if (idle.sessions.workers !== 20 || idle.sessions.open !== 20 || idle.sessions.running !== 0) throw new Error(`idle state: ${JSON.stringify(idle.sessions)}`)
  const firstWorker = memoryRegression ? workerSnapshot(idle, sessions[0].id) : null
  const sends = await Promise.all(sessions.map((session, i) => api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `run ${i}` }) })))
  if (sends.length !== 20) throw new Error('not all sends accepted')
  const samples = []
  for (let i = 0; i < 5; i++) {
    const current = await api('/status')
    if (current.sessions.running !== 20) throw new Error(`running state: ${JSON.stringify(current.sessions)}`)
    samples.push(memory(current))
    await new Promise(resolve => setTimeout(resolve, 1500))
  }
  const peak = samples.reduce((max, value) => value.workerRSSMiB > max.workerRSSMiB ? value : max)
  await new Promise(resolve => setTimeout(resolve, 3500))
  const after = await api('/status')
  if (after.sessions.running !== 0 || calls !== 20 || after.sessions.workers !== 20) throw new Error(`after state: ${JSON.stringify(after.sessions)}, calls=${calls}`)
  const checkpoints = []
  const workerCheckpoints = []
  for (let i = 0; i < extraTurns; i++) {
    const sent = await api(`/sessions/${sessions[0].id}/messages`, { method: 'POST', body: JSON.stringify({ content: `repeat ${i}` }) })
    let cursor = sent.event.sequence
    let finished = false
    for (let poll = 0; poll < 100 && !finished; poll++) {
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
    if ((i + 1) % 100 === 0) {
      await new Promise(resolve => setTimeout(resolve, 5500))
      const status = await api('/status')
      checkpoints.push({ turns: i + 1, ...memory(status) })
      if (memoryRegression) workerCheckpoints.push({ turns: i + 1, ...workerSnapshot(status, sessions[0].id), serverRSSBytes: status.memory.rssBytes })
    }
  }
  await new Promise(resolve => setTimeout(resolve, 5500))
  const repeated = await api('/status')
  if (repeated.sessions.running !== 0 || repeated.sessions.workers !== 20 || calls !== 20 + extraTurns) throw new Error(`repeated state: ${JSON.stringify(repeated.sessions)}, calls=${calls}`)
  const recentEvents = await api(`/sessions/${sessions[0].id}/event-page`)
  const modelResult = recentEvents.find(event => event.kind === 'model_result' && event.data?.modelResult?.usage?.inputTokens === 37)
  if (!modelResult || modelResult.data.modelResult.finishReason !== 'stop' || modelResult.data.modelResult.usage.outputTokens !== 12 || modelResult.data.modelResult.usage.cacheReadTokens !== 8 || modelResult.data.modelResult.usage.reasoningTokens !== 3) throw new Error('model result usage was not persisted')
  if (memoryRegression) {
    const [early, late] = workerCheckpoints
    if (!early || !late || early.turns !== 100 || late.turns !== 200 || early.pid !== firstWorker.pid || late.pid !== firstWorker.pid) throw new Error(`repeated Worker was replaced: ${JSON.stringify(workerCheckpoints)}`)
    if (late.gcCount <= early.gcCount) throw new Error(`no Worker GC between checkpoints: ${early.gcCount} -> ${late.gcCount}`)
    const growth = { heapMiB: +(late.heapBytes - early.heapBytes) / 1048576, rssMiB: +(late.rssBytes - early.rssBytes) / 1048576, serverRssMiB: +(late.serverRSSBytes - early.serverRSSBytes) / 1048576, heapObjects: late.heapObjects - early.heapObjects, goroutines: late.goroutines - early.goroutines }
    if (growth.heapMiB > 4 || growth.rssMiB > 8 || growth.serverRssMiB > 16 || growth.heapObjects > 10000 || growth.goroutines > 4 || late.peakRssBytes > 128 * 1048576) throw new Error(`short Worker memory regression: ${JSON.stringify(growth)}, peakMiB=${(late.peakRssBytes / 1048576).toFixed(1)}`)
  }
  console.log(JSON.stringify({ idle: memory(idle), parallel20: peak, after: memory(after), afterSameSessionTurns: memory(repeated), extraTurns, checkpoints, workerCheckpoints, sessions: repeated.sessions, calls, summaryCalls }, null, 2))
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
