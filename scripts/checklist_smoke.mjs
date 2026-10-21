import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, readFileSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only run checklist isolation, persistence, and Worker acknowledgment.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-checklist-'))
const items = [{ text: 'Inspect input', status: 'done' }, { text: 'Verify output', status: 'in_progress' }]
let calls = 0
const provider = createServer(async (request, response) => {
  try {
    if (request.url !== '/v1/chat/completions') throw new Error(request.url)
    let raw = ''
    for await (const chunk of request) raw += chunk
    const body = JSON.parse(raw)
    if (!body.tools?.some(item => item.function?.name === 'update_checklist')) throw new Error('checklist tool missing')
    calls++
    if ([3, 4].includes(calls) && body.messages.some(message => message.role === 'system' && message.content?.includes('Inspect input'))) throw new Error('new run inherited the previous checklist in its system prompt')
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    if ([1, 4, 5].includes(calls)) {
      const next = calls === 5 ? [] : items
      const chunk = { choices: [{ delta: { tool_calls: [{ index: 0, id: `check_${calls}`, type: 'function', function: { name: 'update_checklist', arguments: JSON.stringify({ items: next }) } }] }, finish_reason: 'tool_calls' }] }
      response.end(`data: ${JSON.stringify(chunk)}\n\ndata: [DONE]\n\n`)
    } else if (calls === 2 || calls === 6) {
      if (body.messages.at(-1)?.role !== 'tool') throw new Error('checklist result was not sent to model')
      response.end('data: {"choices":[{"delta":{"content":"Checklist acknowledged."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
    } else if (calls === 3) {
      response.end('data: {"choices":[{"delta":{"content":"No checklist needed."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
    } else throw new Error('unexpected model call')
  } catch (error) {
    if (response.headersSent) response.destroy(error)
    else { response.writeHead(500); response.end(String(error)) }
  }
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_OPENAI_API_KEY: 'fake-key', VISTA_OPENAI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
let started = false
try {
  async function start() {
    const url = (await promisify(execFile)(binary, ['start'], { env, encoding: 'utf8' })).stdout.trim()
    started = true
    const endpoint = new URL(url).origin
    return async (path, options = {}) => {
      const response = await fetch(endpoint + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json' } })
      if (!response.ok) throw new Error(`${path}: ${response.status} ${await response.text()}`)
      return response.json()
    }
  }
  let api = await start()
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'checklist' }) })
  async function run(prompt, expected) {
    const sent = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: prompt }) })
    let finished = false
    for (let i = 0; i < 250; i++) {
      const events = await api(`/sessions/${session.id}/event-page?after=${sent.event.sequence}`)
      finished = events.some(event => event.kind === 'run_finished' && event.data.runId === sent.runId && event.data.status === 'succeeded')
      if (finished) {
        const updates = events.filter(event => event.kind === 'checklist' && event.data.runId === sent.runId)
        const tools = events.filter(event => event.kind === 'tool' && event.data.runId === sent.runId && event.data.tool?.name === 'update_checklist' && event.data.status === 'succeeded')
        if (updates.length !== tools.length || updates.some((event, index) => event.sequence >= tools[index].sequence)
          || JSON.stringify(updates.at(-1)?.data.items) !== JSON.stringify(expected)) throw new Error(`checklist events incorrect: ${JSON.stringify(events)}`)
        break
      }
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    if (!finished) throw new Error('checklist run did not finish')
    const overview = await api(`/sessions/${session.id}/runs/${sent.runId}/overview`)
    const actual = overview.events.find(event => event.kind === 'checklist')?.data.items
    if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`run checklist incorrect: ${JSON.stringify(actual)}`)
    return sent.runId
  }
  const firstRun = await run('create checklist', items)
  await promisify(execFile)(binary, ['stop'], { env })
  started = false
  api = await start()
  const overview = await api(`/sessions/${session.id}/runs/${firstRun}/overview`)
  const retained = overview.events.find(event => event.kind === 'checklist')?.data.items
  if (JSON.stringify(retained) !== JSON.stringify(items)) throw new Error('checklist did not survive Server restart')
  await run('answer without a checklist')
  await run('create and clear checklist', [])
  if (calls !== 6) throw new Error(`unexpected model call count: ${calls}`)
  console.log('Run checklist isolation, persistence, replay, and clear passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
