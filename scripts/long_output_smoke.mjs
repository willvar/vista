import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, readFileSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only load test for concurrent durable model output; no browser or live key.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
function cgroupMemory() {
  try {
    const line = readFileSync('/proc/self/cgroup', 'utf8').split('\n').find(line => line.startsWith('0::'))
    if (!line) return null
    const root = join('/sys/fs/cgroup', line.slice(3).replace(/^\//, ''))
    const read = name => readFileSync(join(root, name), 'utf8').trim()
    const events = Object.fromEntries(read('memory.events').split('\n').filter(Boolean).map(line => line.split(/\s+/)))
    const mib = value => +(Number(value) / 1048576).toFixed(1)
    return { maxMiB: read('memory.max') === 'max' ? null : mib(read('memory.max')), currentMiB: mib(read('memory.current')), peakMiB: mib(read('memory.peak')), oom: Number(events.oom || 0), oomKill: Number(events.oom_kill || 0) }
  } catch { return null }
}
const initialCgroup = cgroupMemory()
const requiredCgroupMiB = Number(process.env.VISTA_REQUIRE_CGROUP_MIB || 0)
if (!Number.isInteger(requiredCgroupMiB) || requiredCgroupMiB < 0) throw new Error('VISTA_REQUIRE_CGROUP_MIB must be a nonnegative integer')
if (requiredCgroupMiB && initialCgroup?.maxMiB !== requiredCgroupMiB) throw new Error(`expected ${requiredCgroupMiB} MiB cgroup limit, got ${initialCgroup?.maxMiB ?? 'none'}`)
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-long-output-'))
const responses = process.env.VISTA_LONG_OUTPUT_RESPONSES === '1'
const answer = responses ? '<'.repeat(200 << 10) : 'x'.repeat(64 << 10)
let calls = 0
const provider = createServer(async (request, response) => {
  for await (const _ of request) {}
  calls++
  await new Promise(resolve => setTimeout(resolve, 8000))
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  const chunk = responses ? 16 << 10 : answer.length / 2
  for (let offset = 0; offset < answer.length; offset += chunk) {
    const part = answer.slice(offset, offset + chunk)
    const event = responses
      ? { type: 'response.output_text.delta', item_id: 'msg_1', delta: part }
      : { choices: [{ delta: { content: part } }] }
    response.write(`data: ${JSON.stringify(event)}\n\n`)
  }
  if (responses) {
    const part = { type: 'output_text', text: answer }
    const item = { type: 'message', id: 'msg_1', role: 'assistant', content: [part] }
    for (const event of [
      { type: 'response.output_text.done', item_id: 'msg_1', text: answer },
      { type: 'response.content_part.done', item_id: 'msg_1', part },
      { type: 'response.output_item.done', item },
    ]) response.write(`data: ${JSON.stringify(event).replaceAll('<', '\\u003c')}\n\n`)
    const completed = JSON.stringify({ type: 'response.completed', response: { status: 'completed', output: [item] } }).replaceAll('<', '\\u003c')
    response.end(`data: ${completed}\n\n`)
  } else {
    response.end('data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'mock-key', VISTA_OPENAI_MODEL: 'mock', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
if (responses) env.VISTA_DEFAULT_MODEL = 'openai-responses'
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
  for (let i = 0; i < 20; i++) sessions.push(await api('/sessions', { method: 'POST', body: JSON.stringify({ title: `long output ${i}` }) }))
  const runs = await Promise.all(sessions.map((session, i) => api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: `answer ${i}` }) })))
  await new Promise(resolve => setTimeout(resolve, 5500))
  const during = await api('/status')
  if (during.sessions.running !== 20) throw new Error(`expected 20 active Sessions: ${JSON.stringify(during.sessions)}`)
  const results = await Promise.all(sessions.map(async (session, index) => {
    let cursor = runs[index].event.sequence
    for (let i = 0; i < 300; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${cursor}`)
      for (const event of events) {
        cursor = event.sequence
        if (event.kind === 'run_finished' && event.data.runId === runs[index].runId) return event.data
      }
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    throw new Error(`Session ${index} did not finish`)
  }))
  if (calls !== 20 || results.some(result => result.status !== 'succeeded' || result.message?.outputBytes !== answer.length)) throw new Error(`long output runs failed: calls=${calls}, results=${JSON.stringify(results)}`)
  let offset = 0
  let recovered = ''
  while (offset < answer.length) {
    const page = await api(`/sessions/${sessions[0].id}/runs/${runs[0].runId}/model-output?turn=${results[0].message.outputTurn}&offset=${offset}`)
    if (!page.complete || page.nextOffset <= offset) throw new Error(`invalid output page: ${JSON.stringify(page)}`)
    recovered += page.content
    offset = page.nextOffset
  }
  if (recovered !== answer) throw new Error('paged long answer differs from model output')
  const after = await api('/status')
  const finalCgroup = cgroupMemory()
  if (requiredCgroupMiB && finalCgroup?.maxMiB !== requiredCgroupMiB) throw new Error('backend cgroup limit changed during long output test')
  if (initialCgroup && finalCgroup && (finalCgroup.oom > initialCgroup.oom || finalCgroup.oomKill > initialCgroup.oomKill)) throw new Error('backend cgroup reported OOM during long output test')
  console.log(JSON.stringify({ protocol: responses ? 'openai-responses' : 'openai-chat', sessions: after.sessions, calls, answerBytes: answer.length, duringServerRSSMiB: +(during.memory.rssBytes / 1048576).toFixed(1), duringWorkerRSSMiB: +(during.workerMetrics.reduce((sum, item) => sum + item.memory.rssBytes, 0) / 1048576).toFixed(1), afterServerRSSMiB: +(after.memory.rssBytes / 1048576).toFixed(1), afterWorkerRSSMiB: +(after.workerMetrics.reduce((sum, item) => sum + item.memory.rssBytes, 0) / 1048576).toFixed(1), cgroup: finalCgroup }, null, 2))
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  if (process.env.VISTA_LONG_OUTPUT_KEEP_STATE === '1') console.error(`test state kept at ${state}`)
  else rmSync(state, { recursive: true, force: true })
}
