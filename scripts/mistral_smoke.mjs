import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig, projectRulesFixture } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, writeFileSync, rmSync, readFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// Native Mistral protocol and Agent tool roundtrip with a local fake endpoint.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-mistral-'))
const readRules = projectRulesFixture(state)
const inputPath = join(state, 'note.txt')
writeFileSync(inputPath, 'hello from Mistral tool')
let calls = 0
const provider = createServer(async (request, response) => {
  if (request.url !== '/v1/chat/completions' || request.headers.authorization !== 'Bearer fake-key') throw new Error(`wrong Mistral route or key: ${request.url}`)
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  if (readRules(body, response)) return
  calls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  if (calls === 1) {
    if (!body.tools?.some(item => item.function?.name === 'read_file' && item.function?.strict === false)) throw new Error('Mistral native tool schema missing')
    const event = { choices: [{ delta: { tool_calls: [{ index: 0, id: 'tool-id-1', function: { name: 'read_file', arguments: JSON.stringify({ path: inputPath }) } }] }, finish_reason: 'stop' }] }
    response.end(`data: ${JSON.stringify(event)}\n\ndata: [DONE]\n\n`)
  } else if (calls === 2) {
    const tool = body.messages.find(item => item.role === 'tool')
    const assistant = body.messages.find(item => item.role === 'assistant' && item.tool_calls?.length)
    if (!tool?.content?.includes('hello from Mistral tool') || tool.name !== 'read_file' || tool.tool_call_id !== assistant.tool_calls[0].id || tool.tool_call_id.length !== 9) throw new Error('Mistral named tool result was not returned')
    response.end('data: {"choices":[{"delta":{"content":[{"type":"text","text":"I read the note."}]},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
  } else throw new Error('unexpected Mistral replay')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_MISTRAL_API_KEY: 'fake-key', VISTA_MISTRAL_MODEL: 'codestral-latest', VISTA_MISTRAL_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1` }
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
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Mistral native protocol' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Read my note' }) })
  let completed
  for (let i = 0; i < 200; i++) {
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    completed = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
    if (completed) break
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (completed?.data.status !== 'succeeded' || completed.data.message?.content !== 'I read the note.' || calls !== 2) throw new Error(`Mistral run failed: ${JSON.stringify(completed?.data)}, calls=${calls}`)
  console.log('Mistral native tool roundtrip passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
