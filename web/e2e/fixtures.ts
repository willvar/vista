import { test as base, expect } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { createHash } from 'node:crypto'
import { createServer } from 'node:http'
import { createServer as createPortReservation } from 'node:net'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve, join, parse } from 'node:path'
import { restoreHistoryState } from './history-state'
import { projectRulesFixture } from '../../scripts/test_config.mjs'

export type VistaFixture = {
  origin: string
  modelEndpoint: string
  modelId: string
  workspacePath: string
  modelRequests: ModelRequest[]
  modelUserAgents: string[]
  pauseModelReplies: (afterText?: boolean) => () => void
  finishDetachedTask: () => void
  api: (path: string, options?: RequestInit) => Promise<any>
}

type ModelRequest = { model?: string; reasoning?: { effort?: string; summary?: string; max_tokens?: number; enabled?: boolean }; messages?: {
  role: string
  content: unknown
  tool_calls?: { id: string; function: { name: string; arguments: string } }[]
  tool_call_id?: string
}[] }
type ProviderOutput = 'perplexity' | 'openrouter' | 'responses'
type ModelFailure = { status: number; body: object; stream?: boolean; attempts?: number; skipAttempts?: number; retryAfterMs?: number }

const execute = promisify(execFile)
let catalogFixture: string | undefined
function modelCatalogFixture() {
  if (catalogFixture !== undefined) return catalogFixture
  const catalog = JSON.parse(readFileSync(resolve(import.meta.dirname, '../../internal/provider/omp_models.json'), 'utf8'))
  const reasoning = JSON.parse(readFileSync(resolve(import.meta.dirname, '../../internal/testcatalog/testdata/reasoning.json'), 'utf8'))
  const providers = Object.fromEntries(Object.keys(reasoning).sort().map(provider => [provider,
    Object.fromEntries(Object.keys(reasoning[provider]).sort().map(id => [id, reasoning[provider][id]]))]))
  catalog.modelsDev = { schema: 1, revision: createHash('sha256').update(JSON.stringify(providers)).digest('hex'),
    modelCount: Object.values(providers).reduce((count, models) => count + Object.keys(models).length, 0), providers }
  return catalogFixture = JSON.stringify(catalog)
}

type NativeOptions = { configuredProvider: boolean; imageModel: boolean; budgetModel: boolean; replyWithPrompt: boolean; followUpTool: boolean; questionTool: boolean; memoryTool: boolean; progressCalls: number; longProgress: boolean; terminalReasoning: boolean; interleavedOutput: boolean; emptyReply: boolean; detachedTask: boolean; backgroundProcess: boolean; fileChanges: boolean; providerOutput: ProviderOutput | undefined; modelFailure: ModelFailure | undefined }

const nativeDefaults: NativeOptions = {
  configuredProvider: true,
  imageModel: false,
  budgetModel: false,
  replyWithPrompt: false,
  followUpTool: false,
  questionTool: false,
  memoryTool: false,
  progressCalls: 0,
  longProgress: false,
  terminalReasoning: false,
  interleavedOutput: false,
  emptyReply: false,
  detachedTask: false,
  backgroundProcess: false,
  fileChanges: false,
  providerOutput: undefined,
  modelFailure: undefined,
}

// The callback owns this instance. Prepared fixtures stop it before copying
// persistent data; ordinary cases retain the same per-test lifecycle.
export const nativeFixture = {
  async use(options: Partial<NativeOptions>, use: (vista: VistaFixture, stop: () => Promise<string>) => Promise<void>, seed?: string) {
    const { configuredProvider, imageModel, budgetModel, replyWithPrompt, followUpTool, questionTool, memoryTool, progressCalls, longProgress, terminalReasoning, interleavedOutput, emptyReply, detachedTask, backgroundProcess, fileChanges, providerOutput, modelFailure } = { ...nativeDefaults, ...options }
    if (!process.env.VISTA_CHROME) throw new Error('Set VISTA_CHROME to the isolated test browser executable')
    const binary = resolve(process.env.VISTA_BIN || join(import.meta.dirname, '../../bin/vista'))
    const root = resolve(import.meta.dirname, '../.vista/test-state')
    mkdirSync(root, { recursive: true })
    const state = mkdtempSync(join(root, 'e2e-'))
    const workspacePath = join(state, 'workspace')
    mkdirSync(workspacePath)
    writeFileSync(join(workspacePath, 'source.txt'), 'original')
    writeFileSync(join(workspacePath, 'deleted.txt'), 'restore me')
    const readRules = projectRulesFixture(state)
    // Text-only command fixtures do not operate on checkout files.
    const outputDirectory = parse(state).root
    const configuredModel = budgetModel ? { provider: 'catalog', providerId: 'openrouter', id: 'qwen/qwen3.8-flash' }
      : providerOutput === 'perplexity' ? { provider: 'openai-chat', providerId: 'custom', authentication: 'bearer', id: 'sonar-pro' }
      : providerOutput === 'openrouter' ? { provider: 'catalog', providerId: 'openrouter', id: 'aion-labs/aion-2.0' }
      : providerOutput === 'responses' ? { provider: 'openai-responses', id: 'gpt-6.1-sol' }
      : { provider: 'openai-chat', id: 'gpt-6-luna' }
    const modelId = configuredModel.id
    const modelRequests: ModelRequest[] = []
    const modelUserAgents: string[] = []
    let replyGate: Promise<void> | undefined
    let pauseAfterText = false
    let releaseReplies = () => {}
    let finishDetachedTask!: () => void
    const backgroundGate = new Promise<void>(resolve => { finishDetachedTask = resolve })
    const pauseModelReplies = (afterText = false) => {
      pauseAfterText = afterText
      replyGate = new Promise<void>(done => { releaseReplies = done })
      return () => { releaseReplies(); replyGate = undefined }
    }
    const upstream = createServer(async (request, response) => {
      const path = new URL(request.url || '/', 'http://127.0.0.1').pathname
      if (request.method === 'GET' && path === '/v1/models') {
        response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: modelId }] }))
        return
      }
      if (request.method === 'GET' && ['/v1/responses', '/v1/chat/completions'].includes(path)) {
        response.writeHead(405).end()
        return
      }
      const completionPath = providerOutput === 'responses' ? '/v1/responses' : '/v1/chat/completions'
      if (request.method !== 'POST' || path !== completionPath) { response.writeHead(404).end(); return }
      request.setEncoding('utf8')
      let body = ''
      for await (const chunk of request) body += chunk
      let input: ModelRequest
      try { input = JSON.parse(body) } catch { response.writeHead(400).end(); return }
      const titleRequest = providerOutput === 'responses' ? body.includes('Write a short, specific title')
        : String(input.messages?.[0]?.content || '').startsWith('Write a short, specific title')
      if (!titleRequest && !providerOutput && fileChanges
        && readRules(input, response)) return
      const gate = titleRequest ? undefined : replyGate
      const streamBeforeGate = pauseAfterText
      if (!titleRequest) {
        modelRequests.push(input)
        modelUserAgents.push(request.headers['user-agent'] || '')
        if (gate && !streamBeforeGate) await gate
      }
      // Timing checks need measurable latency on loopback; bulk progress
      // history only needs the real request, tool and persistence path.
      if (progressCalls <= 6) await new Promise<void>(done => setTimeout(done, 40))
      if (!titleRequest && modelFailure && modelRequests.length > (modelFailure.skipAttempts ?? 0)
        && modelRequests.length <= (modelFailure.skipAttempts ?? 0) + (modelFailure.attempts ?? Infinity)) {
        if (modelFailure.stream) {
          const event = providerOutput === 'responses' ? { type: 'response.failed', response: modelFailure.body } : modelFailure.body
          response.writeHead(200, { 'Content-Type': 'text/event-stream' }).end(`data: ${JSON.stringify(event)}\n\n`)
        } else {
          response.writeHead(modelFailure.status, { 'Content-Type': 'application/json',
            ...(modelFailure.retryAfterMs ? { 'Retry-After-Ms': String(modelFailure.retryAfterMs) } : {}),
          }).end(JSON.stringify(modelFailure.body))
        }
        return
      }
      if (progressCalls && !titleRequest && input.messages?.filter(message => message.role === 'user').at(-1)?.content === 'Fail progress'
        && input.messages.some(message => message.role === 'tool')) {
        // This fixture exercises retained progress, not short transient recovery.
        response.writeHead(500, { 'Content-Type': 'application/json', 'Retry-After': '3600' }).end(JSON.stringify({ error: { message: 'Synthetic failure after saved progress' } }))
        return
      }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' })
      if (providerOutput) {
        const event = (value: object) => `data: ${JSON.stringify(value)}\n\n`
        const source = 'https://example.com/source'
        const text = titleRequest ? 'Provider output' : emptyReply ? '' : interleavedOutput ? 'Final answer🙂.' : 'Cited answer.'
        if (providerOutput === 'responses') {
          const opening = !titleRequest && interleavedOutput ? 'Opening🙂. ' : ''
          const annotations = titleRequest || interleavedOutput ? [] : [{ type: 'url_citation', url: source }]
          const summaries = opening ? ['Checking one. ', 'Checking two. ', 'Checking three.'] : ['Checked the available source before answering.']
          const summary = !titleRequest && !emptyReply && input.reasoning?.summary === 'auto' ? summaries.join('') : ''
          if (opening) response.write(event({ type: 'response.output_text.delta', item_id: 'opening', delta: opening }))
          if (summary && !terminalReasoning) for (const [index, delta] of summaries.entries()) {
            // Cross the runtime's 250 ms stream-coalescing window before
            // pausing, so the last reasoning fragment is durably visible.
            if (opening && index === summaries.length - 1) await new Promise<void>(done => setTimeout(done, 300))
            response.write(event({ type: 'response.reasoning_summary_text.delta', item_id: 'reasoning', delta }))
          }
          if (opening && gate && streamBeforeGate) await gate
          if (text) response.write(event({ type: 'response.output_text.delta', item_id: 'message', delta: text }))
          for (const annotation of annotations) response.write(event({ type: 'response.output_text.annotation.added', item_id: 'message', annotation }))
          response.end(event({ type: 'response.completed', response: { status: 'completed', output: [
            ...opening ? [{ type: 'message', id: 'opening', role: 'assistant', content: [{ type: 'output_text', text: opening }] }] : [],
            ...summary ? [{ type: 'reasoning', id: 'reasoning', summary: [{ type: 'summary_text', text: summary }] }] : [],
            ...(titleRequest || emptyReply || interleavedOutput ? [] : [{ type: 'mcp_call', id: 'hosted-fixture', status: 'completed', result: { evidence: 'Provider executed this tool.' } }]),
            { type: 'message', id: 'message', role: 'assistant', content: [{ type: 'output_text', text, annotations }] },
          ] } }))
        } else {
          response.end(event({
            ...!titleRequest && providerOutput === 'perplexity' ? { citations: [source] } : {},
            choices: [{ delta: { content: text,
              ...!titleRequest && providerOutput === 'openrouter' ? { annotations: [{ type: 'url_citation', url_citation: { url: source } }] } : {},
            }, finish_reason: 'stop' }],
          }) + 'data: [DONE]\n\n')
        }
        return
      }
      if (String(input.messages?.[0]?.content || '').startsWith('Compress the completed execution transcript')) {
        response.end('data: {"choices":[{"delta":{"content":"Saved verified findings and constraints."},"finish_reason":"stop"}],"usage":{"prompt_tokens":321,"completion_tokens":12}}\n\ndata: [DONE]\n\n')
        return
      }
      const lastMessage = input.messages?.at(-1)
      const prompt = replyWithPrompt ? lastMessage?.role === 'tool' && lastMessage.tool_call_id === 'setup-question'
        ? lastMessage.content : input.messages?.filter(message => message.role === 'user').at(-1)?.content : 'Hi'
      const lastUser = input.messages?.map(message => message.role).lastIndexOf('user') ?? -1
      if (memoryTool && !titleRequest) {
        const currentPrompt = input.messages?.[lastUser]?.content
        const tools = input.messages?.slice(lastUser + 1).filter(message => message.role === 'tool') || []
        let args: object | undefined
        if (currentPrompt === 'Remember concise answers' && !tools.length) args = { action: 'add', text: 'Keep answers concise.' }
        if (currentPrompt === 'Forget my preference') {
          if (!tools.length) args = { action: 'list' }
          else if (tools.length === 1) {
            const memory = JSON.parse(String(tools[0].content)).memories[0]
            args = { action: 'remove', id: memory.id, previousText: memory.text }
          }
        }
        if (args) {
          const tool_calls = [{ index: 0, id: `memory-${tools.length}`, type: 'function', function: { name: 'memory', arguments: JSON.stringify(args) } }]
          response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
          return
        }
      }
      if (fileChanges && !titleRequest && String(input.messages?.[lastUser]?.content).startsWith('Edit files')
        && !input.messages?.slice(lastUser + 1).some(message => message.role === 'tool')) {
        const version = String(input.messages?.[lastUser]?.content).includes('v2') ? 'agent version 2' : 'agent version 1'
        const tool_calls = [{ index: 0, id: 'edit-files', type: 'function', function: { name: 'shell', arguments: JSON.stringify({
          directory: workspacePath, command: `printf '${version}' > source.txt; rm -f deleted.txt; printf 'created' > added.txt`,
        }) } }]
        response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
        return
      }
      if (backgroundProcess && !titleRequest) {
        const currentPrompt = input.messages?.[lastUser]?.content
        const results = input.messages?.slice(lastUser + 1).filter(message => message.role === 'tool') || []
        if (!results.length && ['Start process', 'Start timed process', 'Fail command'].includes(String(currentPrompt))) {
          const start = currentPrompt !== 'Fail command'
          const functionName = start ? 'start_background' : 'shell'
          const command = start ? "printf 'monitor ready\\n'; while :; do printf 'live tick\\n'; sleep 0.2; done" : "printf 'command evidence'; exit 7"
          const timeoutSeconds = currentPrompt === 'Start timed process' ? 172800 : start ? undefined : 60
          const tool_calls = [{ index: 0, id: 'process-tool', type: 'function', function: { name: functionName, arguments: JSON.stringify({ directory: outputDirectory, command, timeoutSeconds }) } }]
          response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
          return
        }
        const content = currentPrompt === 'Fail command' ? (String(results.at(-1)?.content).includes('exit code 7') ? 'Handled exit 7 with command evidence.' : 'Missing failure evidence.') : 'Process started.'
        response.end(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`)
        return
      }
      if (detachedTask && !titleRequest) {
        const currentPrompt = input.messages?.[lastUser]?.content
        const hasTools = input.messages?.slice(lastUser + 1).some(message => message.role === 'tool')
        const completion = input.messages?.slice(lastUser + 1)
          .filter(message => message.role === 'system' && String(message.content).startsWith('Runtime completion event.'))
          .map(message => JSON.parse(String(message.content).slice(String(message.content).indexOf('\n') + 1)))
          .find(event => event.kind === 'agent' && event.sourceId === 'main/background')
        if (completion) {
          const content = `Background completion received: ${completion.status}.`
          response.end(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`)
          return
        }
        if (currentPrompt === 'Start background' && !hasTools) {
          const tool_calls = [{ index: 0, id: 'background', type: 'function', function: { name: 'delegate', arguments: JSON.stringify({ task: 'Background check', join: 'detached' }) } }]
          response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
          return
        }
        if (currentPrompt === 'Background check') {
          if (!hasTools) {
            response.write('data: {"choices":[{"delta":{"reasoning_content":"Background inspection in progress."}}]}\n\n')
            await Promise.race([backgroundGate, new Promise<void>(resolve => response.once('close', resolve))])
            if (response.destroyed) return
            const tool_calls = [{ index: 0, id: 'background-shell', type: 'function', function: { name: 'shell', arguments: JSON.stringify({ directory: outputDirectory, command: "printf 'background evidence'" }) } }]
            response.end(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls }, finish_reason: 'tool_calls' }] })}\n\ndata: [DONE]\n\n`)
          } else {
            response.end('data: {"choices":[{"delta":{"content":"Background report complete."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
          }
          return
        }
      }
      if (progressCalls && !titleRequest) {
        const turn = (input.messages?.slice(lastUser + 1).filter(message => message.role === 'tool').length || 0) + 1
        const content = turn <= progressCalls ? `Progress ${turn}.\n\n${longProgress && turn === 1 ? '完整正文 café 🙂。\n\n'.repeat(800) + 'Long progress ending.' : 'Saved progress text.'}` : 'Final progress answer.'
        const reasoning = longProgress && turn === 1 ? '完整推理 café 🙂。\n\n'.repeat(800) + 'Long reasoning ending.' : 'Private progress reasoning.'
        response.write(`data: ${JSON.stringify({ choices: [{ delta: { reasoning_content: reasoning }, finish_reason: null }] })}\n\n`)
        if (gate && streamBeforeGate) await new Promise<void>(done => setTimeout(done, 300))
        response.write(`data: ${JSON.stringify({ choices: [{ delta: { content }, finish_reason: null }] })}\n\n`)
        if (gate && streamBeforeGate) await gate
        const delta = turn <= progressCalls ? { tool_calls: [{ index: 0, id: `progress-${turn}`, type: 'function', function: {
          name: 'shell', arguments: JSON.stringify({ directory: outputDirectory, command: progressCalls <= 6 ? "sleep 0.3; printf 'Tool result'" : "printf 'Tool result'" }),
        } }] } : {}
        response.write(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: turn <= progressCalls ? 'tool_calls' : 'stop' }] })}\n\n`)
        response.end('data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5}}\n\ndata: [DONE]\n\n')
        return
      }
      if (questionTool && !titleRequest && input.messages?.[lastUser]?.content === 'Ask me about the setup'
        && !input.messages.slice(lastUser + 1).some(message => message.role === 'tool' && message.tool_call_id === 'setup-question')) {
        const toolCalls = [{ index: 0, id: 'setup-question', type: 'function', function: {
          name: 'question', arguments: JSON.stringify({ questions: [
            { header: 'Deployment', question: 'Where should the service run?', custom: false, options: [
              { label: 'Keep local', description: 'Run the service on this computer and keep the current local workflow.' },
              { label: 'Remote host', description: 'Run the service on a separate machine.' },
            ] },
            { header: 'Scope', question: 'Which parts should this change include?', multiple: true, custom: false, options: [
              { label: 'Messages', description: 'Conversation messages and their execution details.' },
              { label: 'Sessions', description: 'Session management and navigation.' },
              { label: 'Provider settings', description: 'Connections and model selection.' },
            ] },
            { header: 'Priority', question: 'What should the implementation prioritize?', custom: true, options: [
              { label: 'Fast response', description: 'Optimize the time before the first reply appears.' },
              { label: 'Better quality', description: 'Optimize completeness and accuracy.' },
            ] },
          ] }),
        } }]
        response.write(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls: toolCalls }, finish_reason: 'tool_calls' }] })}\n\n`)
        response.end('data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5}}\n\ndata: [DONE]\n\n')
        return
      }
      const needsFollowUpTool = followUpTool && !titleRequest && input.messages?.[lastUser]?.content === 'Follow-up question'
        && !input.messages.slice(lastUser + 1).some(message => message.role === 'tool')
      if (needsFollowUpTool) {
        const toolCalls = [{ index: 0, id: 'follow-up-price', type: 'function', function: {
          name: 'shell', arguments: JSON.stringify({ directory: outputDirectory, command: "printf 'price lookup result'" }),
        } }]
        response.write(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls: toolCalls }, finish_reason: 'tool_calls' }] })}\n\n`)
        response.end('data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5}}\n\ndata: [DONE]\n\n')
        return
      }
      if (followUpTool && !titleRequest && prompt === 'First question') {
        response.write('data: {"choices":[{"delta":{"reasoning_content":"Checking the first question"},"finish_reason":null}]}\n\n')
        // Keep the answer in a later streaming batch before withholding the
        // completion marker, so the test queues its follow-up after visible text.
        await new Promise<void>(done => setTimeout(done, 300))
      }
      response.write(`data: ${JSON.stringify({ choices: [{ delta: { content: titleRequest ? 'E2E conversation' : `Mock reply: ${prompt}` }, finish_reason: gate && streamBeforeGate ? null : 'stop' }] })}\n\n`)
      if (gate && streamBeforeGate) {
        await gate
        response.write('data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n')
      }
      response.end('data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5}}\n\ndata: [DONE]\n\n')
    })
    await new Promise<void>(done => upstream.listen(0, '127.0.0.1', done))
    const address = upstream.address()
    if (!address || typeof address === 'string') throw new Error('Mock endpoint did not start')
    const modelEndpoint = `http://127.0.0.1:${address.port}/v1`
    const configDirectory = join(state, 'vista')
    mkdirSync(configDirectory, { mode: 0o700 })
    if (seed) restoreHistoryState(seed, join(configDirectory, 'state'))
    // Reasoning tests consume explicit models.dev controls, never OMP's inferred tiers.
    writeFileSync(join(configDirectory, 'omp-models.json'), modelCatalogFixture(), { mode: 0o600 })
    // Catalog scheduling has its own mocked tests; ordinary UI fixtures stay offline.
    writeFileSync(join(configDirectory, 'model-catalog-status.json'), JSON.stringify({ nextCheckAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString() }), { mode: 0o600 })
    writeFileSync(join(configDirectory, 'models.json'), JSON.stringify(configuredProvider
      ? { default: 'test', models: [{ key: 'test', name: 'Layout test', ...configuredModel, baseUrl: modelEndpoint, apiKey: 'test-key', ...(imageModel ? { inputModalities: ['text', 'image'] } : {}) }] }
      : { default: '', models: [] }), { mode: 0o600 })
    const reservePort = async () => {
      const reservation = createPortReservation()
      await new Promise<void>(done => reservation.listen(0, '127.0.0.1', done))
      const address = reservation.address()
      if (!address || typeof address === 'string') throw new Error('Test port reservation failed')
      await new Promise<void>((done, reject) => reservation.close(error => error ? reject(error) : done()))
      return String(address.port)
    }
    const env = { ...process.env, XDG_CONFIG_HOME: state, XDG_DATA_HOME: join(state, 'data'), OPENCODE_CONFIG: '', OPENCODE_CONFIG_DIR: '', APPDATA: state, VISTA_NO_BROWSER: '1', VISTA_PORT: await reservePort() }
    let started = false
    const stop = async () => {
      if (started) { await execute(binary, ['stop'], { env, timeout: 15_000 }); started = false }
      return join(configDirectory, 'state')
    }
    try {
      for (let attempt = 0; ; attempt++) {
        const logPath = join(configDirectory, 'state', 'server.log')
        const previousLog = existsSync(logPath) ? readFileSync(logPath, 'utf8') : ''
        try { await execute(binary, ['start'], { env, timeout: 15_000 }); break }
        catch (error) {
          const log = existsSync(logPath) ? readFileSync(logPath, 'utf8') : ''
          // Another process can claim the port between reservation and bind.
          // Retry only that setup race, never application/test failures.
          if (attempt < 2 && log.slice(previousLog.length).includes('bind: address already in use')) {
            env.VISTA_PORT = await reservePort()
            continue
          }
          throw new Error(`${error}${log ? '\n' + log : ''}`, { cause: error })
        }
      }
      started = true
      const info = JSON.parse(readFileSync(join(configDirectory, 'state', 'instance.json'), 'utf8')) as { address: string }
      const origin = info.address.startsWith('http') ? info.address : 'http://' + info.address
      await use({ origin, modelEndpoint, modelId, workspacePath, modelRequests, modelUserAgents, pauseModelReplies, finishDetachedTask, api: async (path, options = {}) => {
        const response = await fetch(origin + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } })
        if (!response.ok) throw new Error(`Fixture API ${path}: HTTP ${response.status}`)
        const body = await response.text()
        return body ? JSON.parse(body) : null
      } }, stop)
    } finally {
      releaseReplies()
      finishDetachedTask()
      try {
        await stop()
      } finally {
        await new Promise<void>(done => { upstream.closeAllConnections(); upstream.close(() => done()) })
        rmSync(state, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
      }
    }
  },
}

export const test = base.extend<NativeOptions & { vista: VistaFixture }>({
  configuredProvider: [true, { option: true }],
  imageModel: [false, { option: true }],
  budgetModel: [false, { option: true }],
  replyWithPrompt: [false, { option: true }],
  followUpTool: [false, { option: true }],
  questionTool: [false, { option: true }],
  memoryTool: [false, { option: true }],
  progressCalls: [0, { option: true }],
  longProgress: [false, { option: true }],
  terminalReasoning: [false, { option: true }],
  interleavedOutput: [false, { option: true }],
  emptyReply: [false, { option: true }],
  detachedTask: [false, { option: true }],
  backgroundProcess: [false, { option: true }],
  fileChanges: [false, { option: true }],
  providerOutput: [undefined, { option: true }],
  modelFailure: [undefined, { option: true }],
  vista: async ({ configuredProvider, imageModel, budgetModel, replyWithPrompt, followUpTool, questionTool, memoryTool, progressCalls, longProgress, terminalReasoning, interleavedOutput, emptyReply, detachedTask, backgroundProcess, fileChanges, providerOutput, modelFailure }, use) => {
    await nativeFixture.use({ configuredProvider, imageModel, budgetModel, replyWithPrompt, followUpTool, questionTool, memoryTool, progressCalls, longProgress, terminalReasoning, interleavedOutput, emptyReply, detachedTask, backgroundProcess, fileChanges, providerOutput, modelFailure }, use)
  },
  page: async ({ page, vista }, use) => {
    await page.goto(vista.origin + '/')
    await use(page)
  },
})

export { expect }
