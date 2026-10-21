import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { connect } from 'node:net'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// Backend-only fault load: 128 clients stop reading while 20 Sessions produce long answers.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const rounds = Number(process.env.VISTA_SLOW_SSE_ROUNDS || 8)
if (!Number.isInteger(rounds) || rounds < 1 || rounds > 40) throw new Error('VISTA_SLOW_SSE_ROUNDS must be 1–40')
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
const mib = value => +(Number(value) / 1048576).toFixed(1)
function cgroupMemory() {
  try {
    const line = readFileSync('/proc/self/cgroup', 'utf8').split('\n').find(line => line.startsWith('0::'))
    const root = join('/sys/fs/cgroup', line.slice(3).replace(/^\//, ''))
    const read = name => readFileSync(join(root, name), 'utf8').trim()
    const events = Object.fromEntries(read('memory.events').split('\n').filter(Boolean).map(line => line.split(/\s+/)))
    return { maxMiB: read('memory.max') === 'max' ? null : mib(read('memory.max')), currentMiB: mib(read('memory.current')), peakMiB: mib(read('memory.peak')), oom: Number(events.oom || 0), oomKill: Number(events.oom_kill || 0) }
  } catch { return null }
}
const requiredCgroupMiB = Number(process.env.VISTA_REQUIRE_CGROUP_MIB || 0)
const initialCgroup = cgroupMemory()
if (requiredCgroupMiB && initialCgroup?.maxMiB !== requiredCgroupMiB) throw new Error(`expected ${requiredCgroupMiB} MiB backend cgroup`)

const state = mkdtempSync(join(process.env.VISTA_TEST_TMPDIR || process.cwd(), 'vista-slow-sse-'))
const answer = 'x'.repeat(200 << 10)
let calls = 0
const provider = createServer(async (request, response) => {
  let body = ''
  for await (const chunk of request) body += chunk
  if (!JSON.parse(body).tools?.length) {
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    response.end('data: {"choices":[{"delta":{"content":"Summary of earlier turns."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
    return
  }
  calls++
  await delay(500)
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  for (let offset = 0; offset < answer.length; offset += 2048) {
    response.write(`data: ${JSON.stringify({ choices: [{ delta: { content: answer.slice(offset, offset + 2048) } }] })}\n\n`)
  }
  response.end('data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'mock-key', VISTA_OPENAI_MODEL: 'mock', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
const stalled = []
try {
  const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
  started = true
  const endpoint = new URL(url).origin
  const address = new URL(endpoint)
  async function api(path, options = {}) {
    const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
    if (!response.ok) throw new Error(`${path}: HTTP ${response.status} ${await response.text()}`)
    return response.json()
  }
  async function waitForStreams(expected) {
    for (let attempt = 0; attempt < 100; attempt++) {
      const status = await api('/status')
      if (status.eventStreams === expected) return status
      await delay(50)
    }
    throw new Error(`event stream count never reached ${expected}`)
  }
  const sessions = []
  for (let index = 0; index < 20; index++) sessions.push(await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `slow SSE ${index}` }) }))
  for (const session of sessions.slice(0, 16)) {
    for (let index = 0; index < 8; index++) {
      const socket = connect(Number(address.port), address.hostname)
      socket.on('error', () => {})
      socket.pause()
      socket.write(`GET /api/sessions/${session.id}/events?after=0 HTTP/1.1\r\nHost: ${address.host}\r\nConnection: keep-alive\r\n\r\n`)
      stalled.push(socket)
    }
  }
  const before = await waitForStreams(128)
  const overflow = await fetch(endpoint + `/api/sessions/${sessions[16].id}/events`, {})
  if (overflow.status !== 503) throw new Error(`global stream limit returned HTTP ${overflow.status}`)
  await overflow.body?.cancel()
  let firstRun
  for (let round = 0; round < rounds; round++) {
    const sends = await Promise.all(sessions.map((session, index) => api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `long answer ${index} round ${round}` }) })))
    if (round === 0) firstRun = sends[0]
    const results = await Promise.all(sessions.map(async (session, index) => {
      let cursor = sends[index].event.sequence
      for (let attempt = 0; attempt < 300; attempt++) {
        const page = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
        for (const event of page) {
          cursor = event.sequence
          if (event.kind === 'run_finished' && event.data.runId === sends[index].runId) return event.data
        }
        await delay(50)
      }
      throw new Error(`Session ${index} did not finish round ${round} while clients were stalled`)
    }))
    if (results.some(result => result.status !== 'succeeded' || result.message?.outputBytes !== answer.length)) throw new Error(`long answers failed under stalled event readers in round ${round}`)
  }
  if (calls !== 20 * rounds) throw new Error(`expected ${20 * rounds} Provider calls, got ${calls}`)
  await delay(5500)
  const after = await api('/status')
  if (after.sessions.workers !== 20 || after.sessions.running !== 0 || after.sessions.unavailable !== 0) throw new Error(`Worker state degraded: ${JSON.stringify(after.sessions)}`)
  if (rounds >= 40 && after.eventStreams !== 0) throw new Error(`slow SSE readers were retained after sustained backpressure: ${after.eventStreams}`)
  for (const socket of stalled) socket.destroy()
  await waitForStreams(0)
  const replay = await api(`/sessions/${sessions[0].id}/event-page?after=${firstRun.event.sequence}`)
  if (!replay.some(event => event.kind === 'model_delta')) throw new Error('durable event replay lost model output')
  const finalCgroup = cgroupMemory()
  if (requiredCgroupMiB && finalCgroup?.maxMiB !== requiredCgroupMiB) throw new Error('backend cgroup limit changed')
  if (initialCgroup && finalCgroup && (finalCgroup.oom > initialCgroup.oom || finalCgroup.oomKill > initialCgroup.oomKill)) throw new Error('backend cgroup reported OOM')
  console.log(JSON.stringify({ sessions: after.sessions, stalledClients: stalled.length, rounds, providerCalls: calls, answerBytes: answer.length, streamsBefore: before.eventStreams, streamsAfter: after.eventStreams, serverRSSBeforeMiB: mib(before.memory.rssBytes), serverRSSAfterMiB: mib(after.memory.rssBytes), workerRSSAfterMiB: mib(after.workerMetrics.reduce((sum, item) => sum + item.memory.rssBytes, 0)), cgroup: finalCgroup }, null, 2))
} finally {
  for (const socket of stalled) socket.destroy()
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
