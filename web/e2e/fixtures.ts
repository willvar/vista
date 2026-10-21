import { test as base, expect } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { createServer } from 'node:http'
import { createServer as createPortReservation } from 'node:net'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve, join } from 'node:path'

export type VistaFixture = {
  origin: string
  modelEndpoint: string
  modelId: string
  workspacePath: string
  modelRequests: ModelRequest[]
  pauseModelReplies: (afterText?: boolean) => () => void
  finishDetachedTask: () => void
  api: (path: string, options?: RequestInit) => Promise<any>
}

type ModelRequest = { model?: string; reasoning?: { effort?: string; summary?: string }; messages?: {
  role: string
  content: unknown
  tool_calls?: { id: string; function: { name: string; arguments: string } }[]
  tool_call_id?: string
}[] }
type ProviderOutput = 'perplexity' | 'openrouter' | 'responses'
type ModelFailure = { status: number; body: object; stream?: boolean }

export const test = base.extend<{ vista: VistaFixture; configuredProvider: boolean; imageModel: boolean; replyWithPrompt: boolean; followUpTool: boolean; questionTool: boolean; memoryTool: boolean; progressCalls: number; longProgress: boolean; terminalReasoning: boolean; detachedTask: boolean; backgroundProcess: boolean; fileChanges: boolean; providerOutput: ProviderOutput | undefined; modelFailure: ModelFailure | undefined }>({
  configuredProvider: [true, { option: true }],
  imageModel: [false, { option: true }],
  replyWithPrompt: [false, { option: true }],
  followUpTool: [false, { option: true }],
  questionTool: [false, { option: true }],
  memoryTool: [false, { option: true }],
  progressCalls: [0, { option: true }],
  longProgress: [false, { option: true }],
  terminalReasoning: [false, { option: true }],
  detachedTask: [false, { option: true }],
  backgroundProcess: [false, { option: true }],
  fileChanges: [false, { option: true }],
  providerOutput: [undefined, { option: true }],
  modelFailure: [undefined, { option: true }],
  vista: async ({ configuredProvider, imageModel, replyWithPrompt, followUpTool, questionTool, memoryTool, progressCalls, longProgress, terminalReasoning, detachedTask, backgroundProcess, fileChanges, providerOutput, modelFailure }, use) => {
    if (!process.env.VISTA_CHROME) throw new Error('Set VISTA_CHROME to the isolated test browser executable')
    const binary = resolve(process.env.VISTA_BIN || join(import.meta.dirname, '../../bin/vista'))
    const root = resolve(import.meta.dirname, '../.vista/test-state')
    mkdirSync(root, { recursive: true })
    const state = mkdtempSync(join(root, 'e2e-'))
    const workspacePath = join(state, 'workspace')
    mkdirSync(workspacePath)
    writeFileSync(join(workspacePath, 'source.txt'), 'original')
    writeFileSync(join(workspacePath, 'deleted.txt'), 'restore me')
    const configuredModel = providerOutput === 'perplexity' ? { provider: 'openai-chat', providerId: 'custom', authentication: 'bearer', id: 'sonar-pro' }
      : providerOutput === 'openrouter' ? { provider: 'catalog', providerId: 'openrouter', id: 'aion-labs/aion-2.0' }
      : providerOutput === 'responses' ? { provider: 'openai-responses', id: 'gpt-6.1-sol' }
      : { provider: 'openai-chat', id: 'gpt-6-luna' }
    const modelId = configuredModel.id
    const modelRequests: ModelRequest[] = []
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
      let body = ''
      for await (const chunk of request) body += chunk
      let input: ModelRequest
      try { input = JSON.parse(body) } catch { response.writeHead(400).end(); return }
      const titleRequest = providerOutput === 'responses' ? body.includes('Write a short, specific title')
        : String(input.messages?.[0]?.content || '').startsWith('Write a short, specific title')
      const gate = titleRequest ? undefined : replyGate
      const streamBeforeGate = pauseAfterText
      if (!titleRequest) {
        modelRequests.push(input)
        if (gate && !streamBeforeGate) await gate
      }
      // A small upstream wait makes request timing measurable on fast loopback.
      await new Promise<void>(done => setTimeout(done, 40))
      if (!titleRequest && modelFailure) {
        if (modelFailure.stream) {
          const event = providerOutput === 'responses' ? { type: 'response.failed', response: modelFailure.body } : modelFailure.body
          response.writeHead(200, { 'Content-Type': 'text/event-stream' }).end(`data: ${JSON.stringify(event)}\n\n`)
        } else {
          response.writeHead(modelFailure.status, { 'Content-Type': 'application/json' }).end(JSON.stringify(modelFailure.body))
        }
        return
      }
      if (progressCalls && !titleRequest && input.messages?.filter(message => message.role === 'user').at(-1)?.content === 'Fail progress'
        && input.messages.some(message => message.role === 'tool')) {
        response.writeHead(500, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: { message: 'Synthetic failure after saved progress' } }))
        return
      }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' })
      if (providerOutput) {
        const event = (value: object) => `data: ${JSON.stringify(value)}\n\n`
        const source = 'https://example.com/source'
        const text = titleRequest ? 'Provider output' : 'Cited answer.'
        if (providerOutput === 'responses') {
          const annotations = titleRequest ? [] : [{ type: 'url_citation', url: source }]
          const summary = !titleRequest && input.reasoning?.summary === 'auto' ? 'Checked the available source before answering.' : ''
          if (summary && !terminalReasoning) response.write(event({ type: 'response.reasoning_summary_text.delta', item_id: 'reasoning', delta: summary }))
          response.write(event({ type: 'response.output_text.delta', item_id: 'message', delta: text }))
          for (const annotation of annotations) response.write(event({ type: 'response.output_text.annotation.added', item_id: 'message', annotation }))
          response.end(event({ type: 'response.completed', response: { status: 'completed', output: [
            ...summary ? [{ type: 'reasoning', id: 'reasoning', summary: [{ type: 'summary_text', text: summary }] }] : [],
            ...titleRequest ? [] : [{ type: 'mcp_call', id: 'hosted-fixture', status: 'completed', result: { evidence: 'Provider executed this tool.' } }],
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
        response.end('data: {"choices":[{"delta":{"content":"Saved verified findings and constraints."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
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
        if (!results.length && ['Start process', 'Fail command'].includes(String(currentPrompt))) {
          const start = currentPrompt === 'Start process'
          const functionName = start ? 'start_background' : 'shell'
          const command = start ? "printf 'monitor ready\\n'; while :; do printf 'live tick\\n'; sleep 0.2; done" : "printf 'command evidence'; exit 7"
          const tool_calls = [{ index: 0, id: 'process-tool', type: 'function', function: { name: functionName, arguments: JSON.stringify({ directory: state, command, timeoutSeconds: 60 }) } }]
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
            const tool_calls = [{ index: 0, id: 'background-shell', type: 'function', function: { name: 'shell', arguments: JSON.stringify({ directory: state, command: "printf 'background evidence'" }) } }]
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
          name: 'shell', arguments: JSON.stringify({ directory: state, command: progressCalls <= 6 ? "sleep 0.3; printf 'Tool result'" : "printf 'Tool result'" }),
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
          name: 'shell', arguments: JSON.stringify({ directory: state, command: "printf 'price lookup result'" }),
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
    // Catalog scheduling has its own mocked tests; ordinary UI fixtures stay offline.
    writeFileSync(join(configDirectory, 'model-catalog-status.json'), JSON.stringify({ nextCheckAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString() }), { mode: 0o600 })
    writeFileSync(join(configDirectory, 'models.json'), JSON.stringify(configuredProvider
      ? { default: 'test', models: [{ key: 'test', name: 'Layout test', ...configuredModel, baseUrl: modelEndpoint, apiKey: 'test-key', ...(imageModel ? { inputModalities: ['text', 'image'] } : {}) }] }
      : { default: 'demo', models: [{ key: 'demo' }] }), { mode: 0o600 })
    const reservation = createPortReservation()
    await new Promise<void>(done => reservation.listen(0, '127.0.0.1', done))
    const reservedAddress = reservation.address()
    if (!reservedAddress || typeof reservedAddress === 'string') throw new Error('Test port reservation failed')
    const port = String(reservedAddress.port)
    await new Promise<void>((done, reject) => reservation.close(error => error ? reject(error) : done()))
    const env = { ...process.env, XDG_CONFIG_HOME: state, APPDATA: state, VISTA_NO_BROWSER: '1', VISTA_PORT: port }
    let started = false
    try {
      execFileSync(binary, ['start'], { env, stdio: 'ignore', timeout: 15_000 })
      started = true
      const info = JSON.parse(readFileSync(join(configDirectory, 'state', 'instance.json'), 'utf8')) as { address: string }
      const origin = info.address.startsWith('http') ? info.address : 'http://' + info.address
      await use({ origin, modelEndpoint, modelId, workspacePath, modelRequests, pauseModelReplies, finishDetachedTask, api: async (path, options = {}) => {
        const response = await fetch(origin + '/api' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } })
        if (!response.ok) throw new Error(`Fixture API ${path}: HTTP ${response.status}`)
        const body = await response.text()
        return body ? JSON.parse(body) : null
      } })
    } finally {
      releaseReplies()
      finishDetachedTask()
      try {
        if (started) execFileSync(binary, ['stop'], { env, stdio: 'ignore', timeout: 15_000 })
      } finally {
        await new Promise<void>(done => { upstream.closeAllConnections(); upstream.close(() => done()) })
        rmSync(state, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
      }
    }
  },
  page: async ({ page, vista }, use) => {
    await page.goto(vista.origin + '/')
    await use(page)
  },
})

export { expect }
