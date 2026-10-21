import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'
import { createServer } from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtempSync, writeFileSync, rmSync, readFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

// HTTP-only native Gemini protocol smoke; no browser or live key is used.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const stateParent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(stateParent, { recursive: true })
const state = mkdtempSync(join(stateParent, 'vista-gemini-'))
const inputPath = join(state, 'note.txt')
writeFileSync(inputPath, 'hello from Gemini tool')
let calls = 0
const provider = createServer(async (request, response) => {
  if (request.url !== '/v1beta/models/gemini-test:streamGenerateContent?alt=sse' || request.headers['x-goog-api-key'] !== 'fake-key') throw new Error(`wrong Gemini route or key: ${request.url}`)
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  calls++
  response.writeHead(200, { 'Content-Type': 'text/event-stream' })
  if (calls === 1) {
    if (!body.tools?.[0]?.functionDeclarations?.some(item => item.name === 'read_file')) throw new Error('Gemini function declarations missing')
    const payload = { candidates: [{ content: { parts: [{ functionCall: { id: 'gemini-tool-1', name: 'read_file', args: { path: inputPath } }, thoughtSignature: 'opaque' }] }, finishReason: 'STOP' }] }
    response.end(`data: ${JSON.stringify(payload)}\n\n`)
  } else if (calls === 2) {
    const parts = body.contents?.at(-1)?.parts || []
    if (!parts.some(part => part.functionResponse?.id === 'gemini-tool-1' && JSON.stringify(part.functionResponse.response).includes('hello from Gemini tool'))) throw new Error('Gemini tool result was not returned')
    response.end('data: {"candidates":[{"content":{"parts":[{"text":"I read the note."}]},"finishReason":"STOP"}]}\n\n')
  } else throw new Error('unexpected Gemini replay')
})
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1', VISTA_GEMINI_API_KEY: 'fake-key', VISTA_GEMINI_MODEL: 'gemini-test', VISTA_GEMINI_BASE_URL: `http://127.0.0.1:${provider.address().port}/v1beta` }
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
  const session = await api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Gemini protocol' }) })
  const run = await api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Read my note' }) })
  let completed
  for (let i = 0; i < 200; i++) {
    const events = await api(`/sessions/${session.id}/event-page?after=0`)
    completed = events.find(event => event.kind === 'run_finished' && event.data.runId === run.runId)
    if (completed) break
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (completed?.data.status !== 'succeeded' || completed.data.message?.content !== 'I read the note.' || calls !== 2) throw new Error(`Gemini run failed: ${JSON.stringify(completed?.data)}, calls=${calls}`)
  console.log('Gemini native function roundtrip passed')
} catch (error) {
  try { console.error(readFileSync(join(state, 'vista', 'state', 'server.log'), 'utf8')) } catch {}
  throw error
} finally {
  if (started) try { await promisify(execFile)(binary, ['stop'], { env }) } catch {}
  await new Promise(resolve => provider.close(resolve))
  rmSync(state, { recursive: true, force: true })
}
