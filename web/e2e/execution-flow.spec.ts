import { installMockStateStream } from './state-stream-fixture'
import { closeSettings, openSettingsSection } from './settings-navigation'
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Artifact, ChecklistItem, Job, Message, ModelResultView, RunOverview, Session, StreamEvent } from '../src/client/types'
import { coreSnapshot } from './conversation-fixture'
import { scrollToConversationEntry } from './conversation-scroll'
import { buildRunTrace } from '../src/state/runTrace'
import { startFrontend, startSourceFrontend } from './frontend-server'
const replaySources = new WeakMap<Page, StreamEvent[]>()

// Chromium's Unix socket needs a short profile directory, still inside this checkout.
const browserRuntime = resolve(import.meta.dirname, '../../.vista/b')
mkdirSync(browserRuntime, { recursive: true })
process.env.TMPDIR = browserRuntime
const streamHandlers = new Map<string, (request: IncomingMessage, response: ServerResponse) => void>()

async function executionFrontend(use: (origin: string) => Promise<void>, source = false) {
  let origin = ''
  const handle = (request: IncomingMessage, response: ServerResponse) => {
    const handler = streamHandlers.get(origin)
    if (!handler || !/^\/api\/sessions\/[^/]+\/events\?/.test(request.url || '')) return false
    handler(request, response)
    return true
  }
  const server = source ? await startSourceFrontend(handle) : await startFrontend({ handle })
  origin = server.origin
  try { await use(origin) } finally { streamHandlers.delete(origin); await server.close() }
}
const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => executionFrontend(use), { scope: 'worker' }],
})
const sourceTest = test.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => executionFrontend(use, true), { scope: 'worker' }],
})

const initialChecklist: ChecklistItem[] = [
  { text: 'Check message composer', status: 'done' },
  { text: 'Analyze duplicate images', status: 'in_progress' },
  { text: 'Check mobile navigation', status: 'in_progress' },
]

function deterministicEvents(longMain = false): StreamEvent[] {
  const events: StreamEvent[] = []
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId: 'run-replay', ...data } })
  }
  add('message', { id: 'replay-user', role: 'user', sequence: 1, content: 'Review the message composer, duplicate image rendering, and mobile navigation.', createdAt: '2026-01-01T12:00:00Z' })
  const titles = ['Check message composer', 'Analyze duplicate images', 'Check mobile navigation', 'Verify findings', 'Check message composer']
  for (let i = 1; i <= 5; i++) add('agent_task', { taskId: `task-${i}`, parentTaskId: i === 3 ? 'task-2' : 'main', task: { id: `task-${i}`, parentId: i === 3 ? 'task-2' : 'main', title: titles[i - 1], status: 'started' } })
  for (let turn = 1; turn <= 197; turn++) {
    const taskId = turn < 80 ? 'task-1' : turn < 150 ? 'task-2' : turn < 165 ? 'task-4' : turn < 180 ? 'task-5' : 'main'
    add('model_request', { turn, taskId, depth: taskId === 'main' ? 0 : 1, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra', reasoningEffort: 'high' })
    add('model_delta', { turn, taskId, depth: taskId === 'main' ? 0 : 1, content: 'Findings from source inspection. ' })
    if (turn < 197) add('model_result', { turn, taskId, depth: taskId === 'main' ? 0 : 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls', reasoningEffort: 'high' } })
  }
  const failedTools: object[] = []
  let artifactNumber = 0
  for (let call = 1; call <= 410; call++) {
    const taskId = call === 20 || call === 21 ? 'main' : call === 49 ? 'task-2' : call < 80 ? 'task-1' : call < 160 ? 'task-2' : call < (longMain ? 200 : 240) ? 'task-4' : call < (longMain ? 210 : 320) ? 'task-5' : 'main'
    const failed = call === 20 || call === 21 || call === 49
    const outputRef = longMain && call === 310 ? { artifactId: 'virtual-middle-output', bytes: 12000 } : artifactNumber < 297 && !failed ? { artifactId: `output-${++artifactNumber}`, bytes: 12000 } : undefined
    const tool = { callId: `call-${call}`, name: failed ? 'delegate' : 'shell', summary: 'Inspect source implementation', ...(outputRef ? { outputRef } : {}) }
    add('tool', { status: 'started', taskId, parentTaskId: taskId === 'main' ? '' : 'main', tool, content: '' })
    if (failed) {
      failedTools.push({ status: 'failed', taskId, parentTaskId: taskId === 'main' ? '' : 'main', tool, content: JSON.stringify({ error: 'Subtask report delivery failed' }) })
      continue
    }
    if (outputRef) {
      add('artifact', { id: outputRef.artifactId, status: 'started', bytes: 0, complete: false })
      add('artifact', { id: outputRef.artifactId, status: 'succeeded', bytes: 12000, complete: true })
    }
    add('tool', { status: 'succeeded', taskId, parentTaskId: taskId === 'main' ? '' : 'main', tool, content: JSON.stringify({ preview: 'Source inspection output' }) })
  }
  for (let i = 1; i <= 5; i++) add('agent_task', { taskId: `task-${i}`, parentTaskId: i === 3 ? 'task-2' : 'main', task: { id: `task-${i}`, parentId: i === 3 ? 'task-2' : 'main', title: titles[i - 1], status: i <= 3 ? 'failed' : 'succeeded', ...(i >= 4 ? { report: { runId: 'run-replay', turn: 1000 + i, bytes: 12000 } } : {}) } })
  add('checklist', { items: initialChecklist })
  failedTools.forEach(tool => add('tool', tool))
  add('model_delta', { turn: 197, taskId: 'main', depth: 0, content: 'Findings from source inspection. ' })
  return events
}

function replayEvents(): StreamEvent[] {
  const path = process.env.VISTA_REPLAY_FILE
  if (path && !path.startsWith('/')) throw new Error('VISTA_REPLAY_FILE must be an absolute path')
  return path ? JSON.parse(readFileSync(path, 'utf8')) : deterministicEvents()
}

function hydrationEvents(): StreamEvent[] {
  const events: StreamEvent[] = []
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId: 'run-hydration', taskId: 'main', ...data } })
  }
  add('message', { id: 'hydration-user', role: 'user', sequence: 1, content: 'Read the synthetic source file and search its title function.', createdAt: '2026-01-01T12:00:00Z' })
  add('model_request', { turn: 1, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  const file = Array.from({ length: 45 }, (_, index) => `Line ${index + 1}: synthetic source inspection content.`).join('\n') + '\nHydrated file ending.'
  const read = { callId: 'read-source', name: 'read_file', summary: 'src/title.ts' }
  add('tool', { turn: 1, status: 'started', tool: read, content: '' })
  add('tool', { turn: 1, status: 'succeeded', tool: read, content: JSON.stringify({ content: file }) })
  const search = { callId: 'search-title', name: 'search_files', summary: 'title in synthetic source' }
  add('tool', { turn: 1, status: 'started', tool: search, content: '' })
  add('tool', { turn: 1, status: 'succeeded', tool: search, content: JSON.stringify({ matches: [
    { path: 'src/title.ts', line: 12, text: 'export function makeTitle() { return "Synthetic title" }' + ' '.repeat(550) },
    { path: 'src/title.test.ts', line: 24, text: 'expect(makeTitle()).toBe("Synthetic title")' },
  ], filesScanned: 7, truncated: false }) })
  add('model_result', { turn: 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls', durationMs: 1000, usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, reasoningTokens: 3 } } })
  add('model_request', { turn: 2, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  add('model_reasoning_delta', { turn: 2, content: 'The source and search matches support the same finding.' })
  add('model_result', { turn: 2, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls', durationMs: 1000, usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, reasoningTokens: 3 } } })
  add('model_request', { turn: 3, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  // The first bounded event page excludes both tool results. Their overview bodies
  // are truncated, so expanding them must read the original event by sequence.
  for (let index = 0; index < 140; index++) add('model_delta', { turn: 3, content: `Synthetic finding ${index + 1}.\n\n` })
  return events
}

function agentIdentityEvents(): StreamEvent[] {
  const events: StreamEvent[] = []
  const runId = 'run-agent-identities'
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId, ...data } })
  }
  add('message', { id: 'identities-user', role: 'user', sequence: 1, content: 'Inspect the source with delegated agents.', createdAt: '2026-01-01T12:00:00Z' })
  add('agent_task', { taskId: 'main', task: { id: 'main', title: 'Main task', status: 'started' } })
  const instructions = [
    'Inspect the shared source folder and confirm the behavior of the session history controls.',
    'Read the synthetic source carefully, compare the rendered session identities, and explain the relevant finding with its supporting evidence.',
    'Keep the child session assignment complete, including this third line and every detail after the old 300-byte boundary.',
    'Report the result independently so that the parent and sibling sessions retain their own findings and expansion state.',
    '完整委派说明结束。',
  ].join('\n')
  for (const [id, parentId] of [['child-a', 'main'], ['child-b', 'main'], ['grandchild-a', 'child-a']]) {
    add('agent_task', { taskId: id, parentTaskId: parentId, task: { id, parentId, title: instructions, status: 'started' } })
  }
  for (const [index, taskId] of ['main', 'child-a', 'child-b', 'grandchild-a'].entries()) {
    const parentTaskId = taskId === 'main' ? undefined : taskId === 'grandchild-a' ? 'child-a' : 'main'
    const turn = index + 1
    const depth = taskId === 'main' ? 0 : taskId === 'grandchild-a' ? 2 : 1
    add('model_request', { turn, taskId, depth, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
    const tool = { callId: `read-${taskId}`, name: 'read_file', summary: `src/${taskId}.ts` }
    add('tool', { turn, taskId, parentTaskId, status: 'started', tool, content: '' })
    add('tool', { turn, taskId, parentTaskId, status: taskId === 'grandchild-a' ? 'failed' : 'succeeded', tool, content: JSON.stringify(taskId === 'grandchild-a' ? { error: `Independent result from ${taskId}.` } : { content: `Independent result from ${taskId}.` }) })
    add('model_result', { turn, taskId, depth, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  }
  return events
}

function independentWindowEvents(): StreamEvent[] {
  const events: StreamEvent[] = []
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId: 'run-independent-windows', ...data } })
  }
  add('message', { id: 'independent-windows-user', role: 'user', sequence: 1, content: 'Inspect synthetic source with independent delegated agents.', createdAt: '2026-01-01T12:00:00Z' })
  for (const taskId of ['main', 'child-a', 'child-b', 'grandchild-a']) {
    const parentId = taskId === 'main' ? undefined : taskId === 'grandchild-a' ? 'child-a' : 'main'
    add('agent_task', { taskId, parentTaskId: parentId, task: { id: taskId, parentId, title: `Inspect the ${taskId} source area.`, status: 'started' } })
  }
  for (const [index, taskId] of ['main', 'child-a', 'child-b', 'grandchild-a'].entries()) {
    const turn = index + 1
    const parentTaskId = taskId === 'main' ? undefined : taskId === 'grandchild-a' ? 'child-a' : 'main'
    const depth = taskId === 'main' ? 0 : taskId === 'grandchild-a' ? 2 : 1
    add('model_request', { taskId, parentTaskId, depth, turn, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
    for (let step = 1; step <= 6; step++) {
      const tool = { callId: `${taskId}-${step}`, name: 'read_file', summary: `src/${taskId}/step-${step}.ts` }
      add('tool', { taskId, parentTaskId, depth, turn, status: 'started', tool, content: '' })
      add('tool', { taskId, parentTaskId, depth, turn, status: 'succeeded', tool, content: JSON.stringify({ content: `Body from ${taskId} step ${step}.` }) })
    }
    add('model_result', { taskId, parentTaskId, depth, turn, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  }
  return events
}

function cleanToolOutputEvents() {
  const events: StreamEvent[] = []
  const runId = 'run-clean-tool-output'
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId, taskId: 'main', ...data } })
  }
  add('message', { id: 'clean-output-user', role: 'user', sequence: 1, content: 'Inspect synthetic source and report the results.', createdAt: '2026-01-01T12:00:00Z' })
  add('model_request', { turn: 1, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  const full = fullOutputText('Successful shell')
  const error = 'shell stopped: context deadline exceeded; partial output at synthetic-output.log (11 bytes)'
  const searchFull = JSON.stringify({ matches: [{ path: 'src/example.ts', line: 2, text: 'export const synthetic = true;\n' + 'Search output preserved.\n'.repeat(100) }], filesScanned: 3, truncated: false })
  for (const [callId, artifactId, content, status] of [
    ['shell-full', 'artifact-full', JSON.stringify({ preview: full.slice(0, 1024) }), 'succeeded'],
    ['shell-error', 'artifact-error', JSON.stringify({ error }), 'failed'],
  ]) {
    const tool = { callId, name: 'shell', summary: '/synthetic/source', input: { command: callId === 'shell-full' ? 'cat src/example.ts' : 'inspect synthetic-source', directory: '/synthetic/source', timeoutSeconds: 30 }, outputRef: { artifactId, bytes: callId === 'shell-full' ? Buffer.byteLength(full) : 11 } }
    add('tool', { turn: 1, status: 'started', tool, content: '' })
    add('artifact', { id: artifactId, callId, status, bytes: tool.outputRef.bytes, complete: status === 'succeeded', ...(status === 'failed' ? { error } : {}) })
    add('tool', { turn: 1, status, tool, content })
  }
  const results = [
    ['source-read', 'read_file', 'src/example.ts', { content: 'Source read once.\nexport const synthetic = true;' }],
    ['source-search', 'search_files', 'synthetic', { preview: 'Structured search preview replaced by its full result.' }],
    ['empty-skills', 'list_skills', '/synthetic/source', []],
    ['checklist-updated', 'update_checklist', 'Update findings', { items: [{ text: 'Inspect source', status: 'done' }] }],
    ['delegate-started', 'delegate', 'Check the synthetic source', { taskId: 'synthetic-child', status: 'running' }],
  ] as const
  for (const [callId, name, summary, result] of results) {
    const tool = { callId, name, summary, ...(callId === 'source-search' ? { outputRef: { artifactId: 'artifact-search', bytes: Buffer.byteLength(searchFull) } } : {}) }
    add('tool', { turn: 1, status: 'started', tool, content: '' })
    if (callId === 'source-search') add('artifact', { id: 'artifact-search', callId, status: 'succeeded', bytes: Buffer.byteLength(searchFull), complete: true })
    add('tool', { turn: 1, status: 'succeeded', tool, content: JSON.stringify(result) })
  }
  add('model_result', { turn: 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  return { events, full, error, searchFull }
}

function compactToolEvent(event: StreamEvent): StreamEvent {
  if (event.kind !== 'tool') return event
  const bytes = Buffer.from(event.data.content || '')
  return bytes.length > 512 ? {
    ...event, data: { ...event.data, content: bytes.subarray(0, 512).toString('utf8'), tool: { ...event.data.tool, truncated: true } },
  } : event
}

function overviewOf(events: StreamEvent[]): RunOverview {
  const tools = new Map<string, StreamEvent>()
  const retained: StreamEvent[] = []
  const reasoning = new Set<string>()
  let checklist: StreamEvent | undefined
  for (const event of events) {
    if (event.kind === 'tool') {
      tools.set(event.data.tool.callId || String(event.sequence), compactToolEvent(event))
    }
    else if (event.kind === 'model_reasoning_delta' && !event.data.purpose && event.data.turn > 0 && event.data.content) {
      const key = `${event.data.taskId || 'main'}/${event.data.turn}`
      if (!reasoning.has(key)) retained.push(event)
      reasoning.add(key)
    }
    else if (event.kind === 'checklist') checklist = event
    else if (event.kind === 'run_finished') retained.push({ ...event, data: { runId: event.data.runId, status: event.data.status } })
    else if (['agent_task', 'model_request', 'source', 'context_notice'].includes(event.kind)) retained.push(event)
  }
  const finish = [...events].reverse().find(event => event.kind === 'run_finished')
  return {
    modelCalls: events.filter(event => event.kind === 'model_request' && event.data.purpose !== 'summary').length,
    firstSequence: events[0]?.sequence ?? 0,
    throughSequence: events.at(-1)?.sequence ?? 0,
    startedAt: events[0]?.createdAt ?? '',
    ...(finish ? { finishedAt: finish.createdAt } : {}),
    lastActivityAt: events.at(-1)?.createdAt,
    events: [...retained, ...tools.values(), ...(checklist ? [checklist] : [])].sort((a, b) => a.sequence - b.sequence),
  }
}

function fullOutputText(label: string) {
  const paragraph = 'Synthetic full output preserves 中文、café、🙂 and each markdown section in its original order.\n\n'
  const text = `# ${label}\n\n## Opening section\n\nOpening marker — 中文 🙂.\n\n${paragraph.repeat(60)}## Middle section\n\nMiddle marker — café.\n\n${paragraph.repeat(60)}## Final section\n\n${label} ending — 完整输出 ✅.\n`
  expect(Buffer.byteLength(text)).toBeGreaterThan(12 * 1024)
  return text
}

type MockOptions = { messages?: Message[]; jobs?: Job[]; outputContents?: Record<string, string>; outputFailures?: Record<string, number>; outputDelayMs?: number; historyDelayMs?: number; indexDelayMs?: number }
async function mockExecution(page: Page, origin: string, source = replayEvents(), options: MockOptions = {}) {
  const events = source
  replaySources.set(page, events)
  const runId = events[0].data.runId as string
  const session: Session = { id: 'execution-session', title: 'Source inspection replay', modelKey: 'replay-model', manualModelKey: 'replay-model', manualReasoningEffort: 'high', selectionMode: 'manual', selectionTier: 'speed', selectionReasoningEffort: '', open: true, running: true, executing: true, inputQueuePaused: false, createdAt: events[0].createdAt! }
  if (events.some(event => event.kind === 'run_finished')) session.executing = false
  const messages: Message[] = options.messages || [events.find(event => event.kind === 'message')!.data]
  const artifactMap = new Map<string, Artifact>()
  events.filter(event => event.kind === 'artifact').forEach(event => artifactMap.set(event.data.id, event.data))
  const artifacts = [...artifactMap.values()]
  const jobs: Job[] = options.jobs || []
  const outputContents = new Map(Object.entries(options.outputContents || {}))
  const outputFailures = new Map(Object.entries(options.outputFailures || {}))
  const outputGates = new Map<string, Promise<void>>()
  function holdOutput(path: string) {
    let release!: () => void
    outputGates.set(path, new Promise<void>(resolve => { release = resolve }))
    return () => { outputGates.delete(path); release() }
  }
  const historyPages: string[] = []
  const outputReads: string[] = []
  const mainOutputReads: string[] = []
  const nodeReads: number[] = []
  const turnReads: string[] = []
  const overviewReads: string[] = []
  const unexpected: string[] = []
  let sseRequests = 0
  const deliveries = new Set<(checkpoint?: boolean) => void>()
  const checkpoint = () => {
    const { session: current, throughSequence } = coreSnapshot(session, messages, events)
    const latestRun = [...messages].reverse().find(message => message.role === 'user' && !message.contextSummary)?.runId
    const finished = [...events].reverse().find(event => event.kind === 'run_finished' && event.data.runId === latestRun)
    current.latestRunStatus = current.executing ? 'running' : finished?.data.status || ''
    return { session: current, throughSequence }
  }
  // Match the server's display projection: final checklists share the
  // terminal reply cursor, including runs without a final text message.
  const displayMessages = () => {
    const displayed = new Map(messages.map(message => [message.sequence, { ...message }]))
    for (const finished of events.filter(event => event.kind === 'run_finished')) {
      const checklist = [...events].reverse().find(event => event.kind === 'checklist' && event.sequence < finished.sequence
        && event.data.runId === finished.data.runId && !event.data.depth && (!event.data.taskId || event.data.taskId === 'main'))
      const items = checklist?.data.items || checklist?.data.checklist?.items
      if (!items?.length) continue
      const reply = displayed.get(finished.sequence) || finished.data.message || {
        id: `checklist-summary/${finished.sequence}`, sequence: finished.sequence, role: 'assistant', runId: finished.data.runId,
        content: '', createdAt: finished.createdAt || '',
      }
      displayed.set(finished.sequence, { ...reply, checklist: { items, status: finished.data.status, updatedSequence: checklist!.sequence, ...(checklist?.createdAt ? { updatedAt: checklist.createdAt } : {}) } })
    }
    return [...displayed.values()].sort((a, b) => a.sequence - b.sequence)
  }
  // Keep the mock transport open, just like the runtime. A finite fulfilled
  // response would continually exercise recovery during unrelated layout tests.
  streamHandlers.set(origin, (request, response) => {
    let after = Number(new URL(request.url!, origin).searchParams.get('after'))
    response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' })
    const deliver = (sync = false) => {
      for (const event of events) if (event.sequence > after) {
        response.write(`id: ${event.sequence}\nevent: record\ndata: ${JSON.stringify({ ...event, afterSequence: after })}\n\n`)
        after = event.sequence
      }
      if (sync) response.write(`event: sync\ndata: ${JSON.stringify(checkpoint())}\n\n`)
    }
    deliveries.add(deliver)
    const timer = setInterval(() => deliver(true), 15_000)
    response.on('close', () => { clearInterval(timer); deliveries.delete(deliver) })
    deliver(true)
  })
  const append = (kind: string, data: object) => {
    const event: StreamEvent = { sequence: events.at(-1)!.sequence + 1, kind, createdAt: new Date().toISOString(), data: { runId, ...data } }
    events.push(event)
    if (kind === 'job' || kind === 'artifact') {
      const target = kind === 'job' ? jobs : artifacts
      const item = event.data as Job | Artifact
      const index = target.findIndex(value => value.id === item.id)
      if (index < 0) target.push(item as Job & Artifact)
      else target[index] = item as Job & Artifact
    }
    if (kind === 'job_output') {
      const growth = event.data as { id: string; bytes: number }
      const job = jobs.find(item => item.id === growth.id)
      if (job) job.bytes = Math.max(job.bytes, growth.bytes)
    }
    for (const deliver of deliveries) deliver(kind === 'run_finished')
    return event
  }
  await installMockStateStream(page)
  await page.route('**/api/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.slice(4)
    const reply = (json: unknown) => route.fulfill({ json })
    if (request.method() === 'POST' && path === '/activity') return route.fulfill({ status: 204 })
    if (request.method() === 'POST' && path === `/sessions/${session.id}/read`) {
      session.readSequence = Math.max(session.readSequence || 0, request.postDataJSON().sequence)
      return reply({ readSequence: session.readSequence })
    }
    if (request.method() === 'POST' && path.match(/\/jobs\/[^/]+\/stop$/)) {
      const job = jobs.find(item => path.includes(`/jobs/${item.id}/`))
      if (!job) return route.fulfill({ status: 404 })
      append('job', { ...job, status: 'interrupted', complete: false, terminationReason: 'stopped', signal: 'SIGKILL', finishedAt: new Date().toISOString() })
      return route.fulfill({ status: 202 })
    }
    if (request.method() !== 'GET') {
      unexpected.push(`${request.method()} ${path}`)
      return route.fulfill({ status: 500, body: 'Replay fixture forbids mutations and model requests' })
    }
    if (path === '/preferences') return reply({ userId: 'replay-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: 'replay-model', reasoningByModelId: { 'gpt-6-astra': 'high' }, feedbackInvitationSeen: true })
    if (path === '/agent-settings') return reply({ instructions: '', instructionsPath: '/home/test/.config/vista/AGENTS.md', delegationDepth: 1, maxDelegationDepth: 8 })
    if (path === '/server-config') return reply({
      version: 'replay:1', revision: 'a'.repeat(64), config: { port: 5186 }, runningPort: 5186, source: 'default',
      schema: { type: 'object', additionalProperties: false, required: ['port'], properties: { port: { type: 'integer', minimum: 1, maximum: 65535 } } },
    })
    if (path === '/model-catalog') return reply({ revision: 'a'.repeat(40), modelCount: 1, providerCount: 1 })
    if (path === '/user-agent') return reply({ preset: 'vista', custom: '', value: 'Vista/test', presets: [{ id: 'vista', name: 'Vista', value: 'Vista/test' }], maxLength: 512 })
    if (path === '/models') return reply({ default: 'replay-model', models: [{ key: 'replay-model', name: 'Replay', provider: 'openai-chat', id: 'gpt-6-astra', tools: true, reasoningOptions: ['high'] }] })
    if (path === '/status') return reply({ state: 'running', sessions: { total: 1, open: 1, closed: 0, running: session.executing ? 1 : 0, workers: 1 }, workerMetrics: [{ sessionId: session.id, pid: 1, memory: { heapBytes: 1000000, rssBytes: 4000000, sampledAt: session.createdAt } }] })
    if (path === '/sessions') {
      if (!url.searchParams.has('page')) return reply([session])
      const items = url.searchParams.get('open') === 'false' ? [] : [session]
      return reply({ items, page: 1, pageSize: Number(url.searchParams.get('pageSize') || 10), total: items.length, version: 'replay' })
    }
    if (path.endsWith('/snapshot')) {
      historyPages.push('') // The initial recent page is now loaded with the core snapshot.
      const recent = events.slice(-100)
      const root = [...events].reverse().find(event => !event.data.depth && ['model_request', 'model_delta', 'model_reasoning_delta', 'model_result', 'tool', 'run_finished'].includes(event.kind))
      if (root && root.sequence < (recent[0]?.sequence || Infinity)) recent.unshift(root)
      const latestRun = [...messages].reverse().find(message => message.role === 'user' && !message.contextSummary)?.runId
      const checklist = [...events].reverse().find(event => event.kind === 'checklist' && event.data.runId === latestRun && !event.data.depth && (!event.data.taskId || event.data.taskId === 'main'))
      if (checklist && !recent.some(event => event.sequence === checklist.sequence)) recent.push(checklist)
      recent.sort((a, b) => a.sequence - b.sequence)
      const runtimeStart = [...events].reverse().find(event => event.kind === 'run_started')
      return reply({ ...checkpoint(), ...(runtimeStart ? { currentRun: { id: runtimeStart.data.runId, startedAt: runtimeStart.createdAt } } : {}), messages: displayMessages(), events: recent, hasOlder: false, hasOlderEvents: events.length >= 100 })
    }
    if (path === '/model-selection/stats') return reply({ speedScope: 'global', qualityScope: 'user', speedWindowDays: 30, qualityWindowSize: 200, models: [] })
    if (path.endsWith('/messages')) return reply(displayMessages())
    if (path.endsWith('/artifacts')) return reply(artifacts.slice(-200))
    if (path.endsWith('/jobs')) return reply(jobs)
    if (path.match(/\/jobs\/[^/]+$/)) {
      const job = jobs.find(item => path.endsWith(`/jobs/${item.id}`))
      return job ? reply(job) : route.fulfill({ status: 404 })
    }
    if (path.endsWith('/inputs')) return reply([])
    if (path.endsWith('/context')) return reply(null)
    if (path.endsWith('/context-usage')) return reply({ tokens: 1200, sequence: 0 })
    if (path.endsWith('/feedback')) return reply({ feedback: {} })
    if (path.endsWith('/overview')) {
      if (options.historyDelayMs) await new Promise(done => setTimeout(done, options.historyDelayMs))
      overviewReads.push(path)
      const requestedRun = path.split('/runs/')[1]?.split('/')[0]
      const after = Number(url.searchParams.get('after'))
      const through = Number(url.searchParams.get('through'))
      return reply(overviewOf(events.filter(event => event.data.runId === requestedRun && event.sequence > after && (!through || event.sequence <= through))))
    }
    if (path.endsWith('/main-outputs')) {
      if (options.indexDelayMs) await new Promise(done => setTimeout(done, options.indexDelayMs))
      const requestedRun = path.split('/runs/')[1]?.split('/')[0]
      const outputs = new Map<string, { runId: string; taskId: string; turn: number; sequence: number; lastSequence: number; createdAt: string; bytes: number; complete: boolean }>()
      for (const event of events) {
        if (event.data.runId !== requestedRun || event.kind !== 'model_delta' || event.data.depth || event.data.taskId && event.data.taskId !== 'main' || !event.data.content) continue
        const key = String(event.data.turn)
        const output = outputs.get(key) || { runId, taskId: 'main', turn: event.data.turn, sequence: event.sequence, lastSequence: event.sequence, createdAt: event.createdAt || '', bytes: 0,
          complete: events.some(item => item.kind === 'run_finished' || item.kind === 'model_result' && !item.data.depth && item.data.turn === event.data.turn) }
        output.bytes += Buffer.byteLength(event.data.content)
        output.lastSequence = event.sequence
        outputs.set(key, output)
      }
      const before = Number(url.searchParams.get('before'))
      return reply([...outputs.values()].filter(output => !before || output.sequence < before).slice(-100))
    }
    if (path.endsWith('/event-page')) {
      if (!url.searchParams.has('after')) historyPages.push(url.search)
      const before = Number(url.searchParams.get('before'))
      const after = Number(url.searchParams.get('after'))
      const through = Number(url.searchParams.get('through'))
      const page = before ? events.filter(event => event.sequence < before).slice(-100) : url.searchParams.has('after') ? events.filter(event => event.sequence > after && (!through || event.sequence <= through)).slice(0, 100) : events.slice(-100)
      return reply(url.searchParams.get('compact') === '1' ? page.map(compactToolEvent) : page)
    }
    const exactEvent = path.match(/\/runs\/[^/]+\/events\/(\d+)$/)
    if (exactEvent) {
      const sequence = Number(exactEvent[1])
      nodeReads.push(sequence)
      const event = events.find(item => item.sequence === sequence && item.data.runId === runId)
      return event ? reply(event) : route.fulfill({ status: 404, body: 'Missing replay event' })
    }
    const turnEvents = path.match(/\/runs\/[^/]+\/turns\/(\d+)\/events$/)
    if (turnEvents) {
      turnReads.push(path + url.search)
      const turn = Number(turnEvents[1])
      const before = Number(url.searchParams.get('before'))
      const page = events.filter(event => event.data.runId === runId && event.data.turn === turn && (!before || event.sequence < before)).slice(-100)
      return reply(url.searchParams.get('compact') === '1' ? page.map(compactToolEvent) : page)
    }
    if (path.endsWith('/events')) {
      sseRequests++
      return route.continue()
    }
    if (path.endsWith('/output') || path.endsWith('/model-output')) {
      const mainDeltas = events.filter(event => event.kind === 'model_delta' && !event.data.depth && (!event.data.taskId || event.data.taskId === 'main') && event.data.turn === Number(url.searchParams.get('turn')))
      const progressRead = mainDeltas.length > 0 && !messages.some(message => message.role === 'assistant' && message.outputTurn === Number(url.searchParams.get('turn')))
      ;(progressRead ? mainOutputReads : outputReads).push(path + url.search)
      const rangedJob = path.includes('/jobs/') && url.searchParams.has('limit')
      if (!rangedJob && (url.searchParams.get('full') !== '1' || url.searchParams.has('offset'))) {
        unexpected.push(`Paged output ${path}${url.search}`)
        return route.fulfill({ status: 400, body: 'Full output snapshots require full=1 and no offset' })
      }
      const turn = Number(url.searchParams.get('turn'))
      const artifact = artifacts.find(item => path.includes(`/artifacts/${item.id}/`))
      const job = jobs.find(item => path.includes(`/jobs/${item.id}/`))
      const key = path + (turn ? `?turn=${turn}` : '')
      const remainingFailures = outputFailures.get(key) || 0
      await outputGates.get(key)
      if (options.outputDelayMs) await new Promise(done => setTimeout(done, options.outputDelayMs))
      if (remainingFailures) {
        outputFailures.set(key, remainingFailures - 1)
        return route.fulfill({ status: 503, body: 'Synthetic output unavailable' })
      }
      const content = outputContents.get(key) || (mainDeltas.length ? mainDeltas.map(event => event.data.content).join('') : fullOutputText(turn ? `Child report ${turn}` : `Tool output ${artifact?.id || job?.id}`))
      const total = Buffer.byteLength(content)
      if (rangedJob) {
        const offset = Number(url.searchParams.get('offset'))
        const limit = Number(url.searchParams.get('limit'))
        const data = Buffer.from(content)
        let start = Math.min(offset, total), end = Math.min(offset + limit, total)
        while (start < total && (data[start] & 0xc0) === 0x80) start++
        while (end < total && (data[end] & 0xc0) === 0x80) end++
        return reply({ content: data.subarray(start, end).toString(), offset: start, nextOffset: end, availableBytes: total, complete: !['starting', 'running'].includes(job?.status || '') })
      }
      return reply({ content, offset: 0, nextOffset: total, availableBytes: total, lastSequence: mainDeltas.at(-1)?.sequence || events.at(-1)!.sequence, complete: job?.complete ?? true })
    }
    unexpected.push(`${request.method()} ${path}`)
    return route.fulfill({ status: 404, body: 'Unknown replay API' })
  })
  await page.goto(`${origin}/sessions/${session.id}`)
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  return { events, runId, session, messages, artifacts, jobs, outputContents, outputFailures, holdOutput, historyPages, outputReads, mainOutputReads, nodeReads, turnReads, overviewReads, unexpected, append, get sseRequests() { return sseRequests } }
}

function stepNode(title: Locator) { return title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]') }

test('runtime continuation keeps full streamed text through model completion and reload @responsive', async ({ page, mockOrigin }) => {
  const runId = 'runtime-no-user'
  const source: StreamEvent[] = [{ sequence: 1, kind: 'run_started', createdAt: '2026-01-01T12:00:00Z', data: { runId, runtime: true } }]
  source.push({ sequence: 2, kind: 'model_request', createdAt: source[0].createdAt, data: { runId, turn: 1, provider: 'openai-chat', id: 'gpt-6-astra' } })
  for (let index = 0; index < 130; index++) source.push({ sequence: index + 3, kind: 'model_delta', createdAt: source[0].createdAt, data: { runId, turn: 1, content: `Finding-${index}. ` } })
  const fixture = await mockExecution(page, mockOrigin, source, { messages: [] })
  await expect(page.locator('.messages')).toContainText('Finding-0.')
  await expect(page.locator('.messages')).toContainText('Finding-129.')
  fixture.append('model_result', { turn: 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'stop' } })
  await expect(page.locator('.messages')).toContainText('Finding-0.')
  await expect(page.locator('.message-row.user')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('.messages')).toContainText('Finding-0.')
  await expect(page.locator('.messages')).toContainText('Finding-129.')
  await expect(page.locator('.message-row.user')).toHaveCount(0)
  expect(fixture.unexpected).toEqual([])
})

test('patch diffs and path/content searches expand inline @responsive', async ({ page, mockOrigin }) => {
  const runId='file-capabilities'
  const source: StreamEvent[]=[]
  const add=(kind:string,data:object)=>source.push({sequence:source.length+1,kind,createdAt:'2026-01-01T12:00:00Z',data:{runId,taskId:'main',...data}})
  add('message',{id:'files-user',sequence:1,role:'user',content:'Update the matching files.',createdAt:'2026-01-01T12:00:00Z'})
  const outputs=[
    ['patch','apply_patch',{files:[{diff:{path:'/project/new.txt',created:true,unified:'--- /dev/null\n+++ new.txt\n@@ -0,0 +1 @@\n+Added <safe>\n'}},{diff:{path:'/project/old.txt',created:false,deleted:true,unified:'--- old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-Removed\n'}}]}],
    ['find','find_files',{paths:['/project/new.txt'],truncated:false}],
    ['search','search',{matches:[{path:'/project/new.txt',line:1,text:'Before',context:true},{path:'/project/new.txt',line:2,text:'Found'}],count:1,filesScanned:2,truncated:false}],
  ] as const
  for(const [id,name,body] of outputs) {
    const tool={callId:id,name,summary:'/project'}
    add('tool',{turn:1,status:'started',tool})
    add('tool',{turn:1,status:'succeeded',tool,content:JSON.stringify(body)})
  }
  const fixture=await mockExecution(page,mockOrigin,source)
  const trace=page.locator(`.run-trace[data-run-id="${runId}"]`)
  for(const [id] of outputs) await readFull(nodeTitle(trace,runId,`tool/main/${id}`))
  await expect(trace.locator('.file-diff')).toHaveCount(2)
  await expect(trace.locator('.file-diff').first()).toContainText('Added <safe>')
  await expect(trace.locator('.file-diff').last()).toContainText('File deleted')
  await expect(trace.locator('.trace-search-results')).toHaveCount(2)
  await expect(trace.locator('.trace-search-context')).toHaveText('/project/new.txt:1Before')
  await assertNoHorizontalOverflow(page)
  expect(fixture.unexpected).toEqual([])
})
async function readFull(title: Locator) { await stepNode(title).locator('button.ui-thought-chain-node-header').click() }
async function scrollPosition(page: Page) { return page.locator('.messages').evaluate(element => element.scrollTop) }
async function distanceToLatest(page: Page) { return page.locator('.messages').evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight) }
function nodeTitle(trace: Locator, runId: string, key: string) { return trace.locator(`[id="trace-${runId}-0-${encodeURIComponent(key)}"]`) }
// Output can arrive before its opening motion finishes. Geometry assertions
// must inspect the settled row, while the motion tests inspect intermediate frames.
async function settledHeight(node: Locator) {
  return node.evaluate(element => element.getAnimations({ subtree: true }).some(animation =>
    animation.playState === 'running' && animation.effect?.getTiming().iterations !== Infinity,
  ) ? 0 : element.getBoundingClientRect().height)
}
// Seek through the same continuous scrollbar a user can drag. Event sequence
// identifies the target; no product-only jump/debug API is introduced for tests.
async function scrollToTraceStep(page: Page, task: Locator, runId: string, key: string, from: 'start' | 'end' = 'start') {
  const target = buildRunTrace(replaySources.get(page) || [], { runId, active: true }).nodes.find(node => node.key === key)
  expect(target, `Fixture contains step ${key}`).toBeTruthy()
  const title = nodeTitle(task, runId, key)
  await scrollToConversationEntry(page, `[id="trace-${runId}-0-${encodeURIComponent(key)}"]`, target!.sequence, from)
  return title
}

async function assertSessionTerminology(page: Page) {
  const text = await page.locator('body').evaluate(element => {
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT)
    const visible: string[] = []
    while (walker.nextNode()) {
      const parent = walker.currentNode.parentElement
      if (parent && !parent.closest('script,style,.trace-debug-body') && parent.checkVisibility()) visible.push(walker.currentNode.textContent || '')
    }
    return visible.join(' ')
  })
  expect(text, 'Visible session labels must use session terminology; debug JSON keeps internal identifiers').not.toMatch(/\bMain\s+Agent\b|\bSubagent\b|主\s*Agent|子\s*Agent/i)
}
async function assertNoHorizontalOverflow(page: Page) {
  const overflow = await page.locator('.run-trace').first().evaluate(element => {
    const box = element.getBoundingClientRect()
    return { left: box.left, right: box.right, viewport: innerWidth, internal: element.scrollWidth - element.clientWidth,
      overflowing: [...element.querySelectorAll('*')].filter(child => child.clientWidth > 0 && child.scrollWidth > child.clientWidth + 2)
        .slice(0, 10).map(child => ({ class: child.getAttribute('class'), width: child.clientWidth, scrollWidth: child.scrollWidth })),
    }
  })
  expect(overflow.left).toBeGreaterThanOrEqual(-1)
  expect(overflow.right).toBeLessThanOrEqual(overflow.viewport + 1)
  expect(overflow.internal, JSON.stringify(overflow.overflowing)).toBeLessThanOrEqual(2)
}

for (const theme of ['light', 'dark']) test(`execution messages appear without opening a panel (${theme})`, async ({ page, mockOrigin }, testInfo) => {
  await page.addInitScript(theme => localStorage.setItem('vista-appearance', theme), theme)
  const fixture = await mockExecution(page, mockOrigin, independentWindowEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await expect(trace.locator('.trace-steps')).toHaveAttribute('data-step-count', '24')
  await expect(trace.locator('.ui-think, [data-part="trigger"]')).toHaveCount(0)
  await expect(trace.locator('pre.trace-output')).toHaveCount(0)
  await expect.poll(() => trace.locator('button.ui-thought-chain-node-header[aria-expanded="false"]').count()).toBeGreaterThan(0)
  expect(await trace.locator('.trace-step-row').count()).toBeLessThan(24)
  const first = await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/main-1')
  await expect(page.locator('.conversation-execution')).toBeVisible()
  await expect(page.locator('.conversation-execution-elapsed')).not.toBeEmpty()
  await assertNoHorizontalOverflow(page)
  expect(fixture.outputReads).toEqual([])
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath(`inline-execution-${theme}.png`), animations: 'disabled' })
})

async function observeScrollLoading(page: Page) {
  return page.evaluate(() => {
    const scroller = document.querySelector<HTMLElement>('.messages')!
    const samples: object[] = []
    let locked = false
    new MutationObserver(() => {
      const next = scroller.classList.contains('history-scroll-locked')
      if (next && !locked) {
        const bounds = scroller.getBoundingClientRect(), loader = document.querySelector('.history-loading')!
        const first = [...scroller.querySelectorAll<HTMLElement>('[data-timeline-key]')].find(row => row.getBoundingClientRect().bottom > bounds.top && row.getBoundingClientRect().top < bounds.bottom)
        const wheel = new WheelEvent('wheel', { deltaY: 800, bubbles: true, cancelable: true })
        const key = new KeyboardEvent('keydown', { key: 'PageUp', bubbles: true, cancelable: true })
        const touch = new TouchEvent('touchmove', { bubbles: true, cancelable: true })
        scroller.dispatchEvent(wheel); scroller.dispatchEvent(key); scroller.dispatchEvent(touch)
        samples.push({ contentVisible: getComputedStyle(scroller.querySelector('.message-space')!).visibility === 'visible',
          text: first?.innerText.trim().slice(0, 100), first: first?.dataset.timelineKey, rowTop: first ? first.getBoundingClientRect().top - bounds.top : 0,
          rows: [...scroller.querySelectorAll<HTMLElement>('[data-timeline-key]')].map(row => ({ key: row.dataset.timelineKey, top: row.getBoundingClientRect().top - bounds.top })),
          top: loader.getBoundingClientRect().top - bounds.top, height: loader.getBoundingClientRect().height,
          loaders: document.querySelectorAll('.history-loading').length, width: scroller.clientWidth,
          wheelBlocked: wheel.defaultPrevented, keyBlocked: key.defaultPrevented, touchBlocked: touch.defaultPrevented })
        document.body.dataset.scrollLoads = JSON.stringify(samples)
      }
      locked = next
    }).observe(scroller, { attributes: true, attributeFilter: ['class'] })
    const bounds = scroller.getBoundingClientRect()
    const first = [...scroller.querySelectorAll<HTMLElement>('[data-timeline-key]')].find(row => row.getBoundingClientRect().bottom > bounds.top && row.getBoundingClientRect().top < bounds.bottom)
    return { first: first?.dataset.timelineKey, rowTop: first ? first.getBoundingClientRect().top - bounds.top : 0 }
  })
}
async function assertTopScrollLoading(page: Page, origin?: { first?: string; rowTop: number }) {
  const samples = JSON.parse(await page.locator('body').getAttribute('data-scroll-loads') || '[]')
  expect(samples.length).toBeGreaterThan(0)
  if (origin) {
    // A row's fractional bottom edge can cross the viewport by less than a
    // pixel. Compare the original readable row rather than the new edge row.
    const retained = samples[0].rows.find((row: { key: string }) => row.key === origin.first)
    expect(retained, 'Loading retains the previously readable row').toBeTruthy()
    expect(Math.abs(retained.top - origin.rowTop), `Loading retains its screen position: ${JSON.stringify({ origin, retained })}`).toBeLessThanOrEqual(3)
  }
  for (const sample of samples) {
    expect(sample.contentVisible).toBe(true)
    expect(sample.text).toBeTruthy()
    expect(sample.loaders).toBe(1)
    expect(Math.abs(sample.top)).toBeLessThanOrEqual(2)
    expect(sample.height).toBeLessThanOrEqual(60)
    expect(sample.wheelBlocked && sample.keyBlocked && sample.touchBlocked).toBe(true)
  }
}

// Playwright hides scrollbars by default. Give only this regression a browser
// with native scrollbars; worker launch options cannot be changed by describe.use.
const nativeScrollbarTest = test.extend({ launchOptions: {
  executablePath: process.env.VISTA_CHROME,
  ignoreDefaultArgs: ['--hide-scrollbars'],
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking'],
} })
nativeScrollbarTest.describe('native conversation scrollbar', () => {
  nativeScrollbarTest('keeps readable content and locks an unprepared native-thumb destination behind a top loader', async ({ page, mockOrigin }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop', 'Native desktop scrollbar interaction')
    const fixture = await mockExecution(page, mockOrigin, deterministicEvents(true))
    const scroller = page.locator('.messages')
    await expect(scroller).toHaveAttribute('aria-busy', 'false')
    await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
    const origin = await observeScrollLoading(page)
    const bounds = await scroller.evaluate(element => {
      const box = element.getBoundingClientRect(), gutter = (element as HTMLElement).offsetWidth - element.clientWidth
      return { top: box.top, bottom: box.bottom, right: box.right, gutter, viewport: element.clientHeight, height: element.scrollHeight, width: element.clientWidth }
    })
    expect(bounds.gutter).toBeGreaterThan(0)
    const track = bounds.viewport - 2 * bounds.gutter
    const thumb = Math.max(bounds.gutter, track * bounds.viewport / bounds.height)
    const x = bounds.right - bounds.gutter / 2
    const y = (fraction: number) => bounds.top + bounds.gutter + thumb / 2 + (track - thumb) * fraction
    await page.mouse.move(x, y(1))
    await page.mouse.down()
    try { await page.mouse.move(x, y(.4)) } finally { await page.mouse.up() }
    await expect.poll(async () => JSON.parse(await page.locator('body').getAttribute('data-scroll-loads') || '[]').length).toBeGreaterThan(0)
    await expect(scroller).toHaveAttribute('aria-busy', 'false')
    await assertTopScrollLoading(page, origin)
    expect(await scroller.evaluate(element => element.clientWidth)).toBe(bounds.width)
    expect(await distanceToLatest(page)).toBeGreaterThan(bounds.viewport)
    const reply: Message = { id: 'native-new-reply', sequence: fixture.events.at(-1)!.sequence + 1, runId: fixture.runId, role: 'assistant', createdAt: '2026-01-01T13:00:00Z', content: 'New reply after loading the native-thumb destination.' }
    fixture.messages.push(reply)
    fixture.append('message', reply)
    await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
    await expect(page.getByText(reply.content, { exact: true })).toBeVisible()
    await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
    expect(await scroller.locator('.timeline-row').count()).toBeLessThan(30)
    expect(fixture.unexpected).toEqual([])
  })
  for (const longMessages of [false, true]) nativeScrollbarTest(`dragging the native thumb keeps expanded child history stable and reaches both ends${longMessages ? ' with earlier messages' : ''}`, async ({ page, mockOrigin }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop', 'Native desktop scrollbar interaction')
    test.setTimeout(longMessages ? 150_000 : 90_000)
    const events = deterministicEvents(true)
    const messages: Message[] | undefined = longMessages ? [events[0].data, ...Array.from({ length: 110 }, (_, index): Message => ({
      id: `scrollbar-later-${index}`, runId: `scrollbar-later-run-${index}`, role: 'assistant', sequence: 10000 + index,
      content: `Later conversation reply ${index + 1}.`, modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T14:00:00Z',
    }))] : undefined
    const fixture = await mockExecution(page, mockOrigin, events, { messages })
    const scroller = page.locator('.messages')
    const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
    if (longMessages) await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/call-410')
    await scroller.dispatchEvent('wheel', { deltaY: -100 })
    // Setup may position the viewport; every tested drag below uses only the mouse.
    await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
    await page.waitForTimeout(600)
    const measure = () => scroller.evaluate(element => {
      const bounds = element.getBoundingClientRect()
      const remaining = element.scrollHeight - element.clientHeight
      return {
        top: bounds.top, bottom: bounds.bottom, left: bounds.left, right: bounds.right,
        gutter: (element as HTMLElement).offsetWidth - element.clientWidth,
        scrollTop: element.scrollTop, scrollHeight: element.scrollHeight, viewport: element.clientHeight,
        distanceToBottom: remaining - element.scrollTop, progress: element.scrollTop / remaining,
      }
    })
    const records: object[] = []
    async function dragTo(progress: number, label: string) {
      const samples = []
      // A cold destination briefly interrupts the native drag. After it is
      // ready, retry against the measured track just as a user can re-grab it.
      for (let attempt = 0; attempt < 8; attempt++) {
        await expect(scroller).toHaveAttribute('aria-busy', 'false')
        const before = await measure()
        expect(before.gutter, 'The browser must expose a real native scrollbar').toBeGreaterThan(0)
        const trackHeight = before.viewport - 2 * before.gutter
        const thumbHeight = Math.max(before.gutter, trackHeight * before.viewport / before.scrollHeight)
        const x = before.right - before.gutter / 2
        const startY = before.top + before.gutter + thumbHeight / 2 + (trackHeight - thumbHeight) * before.progress
        const endY = progress === 1 ? before.bottom + before.gutter : progress === 0 ? before.top - before.gutter
          : before.top + before.gutter + thumbHeight / 2 + (trackHeight - thumbHeight) * progress
        await page.mouse.move(x, startY)
        await page.mouse.down()
        try { await page.mouse.move(x, endY) } finally { await page.mouse.up() }
        await expect(scroller).toHaveAttribute('aria-busy', 'false')
        await page.waitForTimeout(220)
        const released = await measure()
        samples.push({ before, released })
        expect(released.scrollHeight).toBeGreaterThan(released.viewport)
        if (progress === 1 ? released.distanceToBottom <= 3 : progress === 0 ? released.scrollTop <= 3 : Math.abs(released.progress - progress) <= .03) break
      }
      const final = await measure()
      records.push({ label, samples, final })
      writeFileSync(testInfo.outputPath('native-scrollbar-positions.json'), JSON.stringify(records, null, 2))
      await page.screenshot({ path: testInfo.outputPath(`${label}-released.png`) })
      if (progress === 1) expect(final.distanceToBottom).toBeLessThanOrEqual(3)
      else if (progress === 0) expect(final.scrollTop).toBeLessThanOrEqual(3)
      else expect(Math.abs(final.progress - progress)).toBeLessThanOrEqual(.03)
    }
    await dragTo(1, 'bottom')
    await expect(trace, 'Execution data remains cached while its rows are virtualized').toHaveCount(1)
    await dragTo(.5, 'middle')
    await dragTo(0, 'top')
    const beforeWheel = await measure()
    await page.mouse.move((beforeWheel.left + beforeWheel.right) / 2, (beforeWheel.top + beforeWheel.bottom) / 2)
    await page.mouse.wheel(0, 700)
    await expect.poll(() => scrollPosition(page), 'Wheel scrolling continues after releasing the thumb').toBeGreaterThan(beforeWheel.scrollTop + 100)
    if (longMessages) {
      await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
      await expect(page.getByText('Later conversation reply 110.', { exact: true })).toBeVisible()
    }
    expect(fixture.unexpected).toEqual([])
    await testInfo.attach('native-scrollbar-positions', { body: JSON.stringify(records, null, 2), contentType: 'application/json' })
  })
})

test('interleaved main and child activity stays ordered and does not disturb earlier reading', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockExecution(page, mockOrigin, independentWindowEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await expect(trace.locator('.trace-steps')).toHaveAttribute('data-step-count', '24')
  const first = await scrollToTraceStep(page, trace, fixture.runId, 'tool/child-a/child-a-1')
  await trace.dispatchEvent('click')
  const top = (await first.boundingBox())!.y
  const reads = fixture.historyPages.length
  for (const [index, taskId] of ['main', 'child-a', 'main', 'child-b'].entries()) {
    fixture.append('tool', { taskId, status: 'succeeded', tool: { callId: `interleaved-${index}`, name: 'read_file', summary: `src/${index}.ts` }, content: JSON.stringify({ content: `Live content ${index}` }) })
  }
  await expect(trace.locator('.trace-steps')).toHaveAttribute('data-step-count', '28')
  expect(await trace.locator('.trace-step-row').count()).toBeLessThanOrEqual(28)
  await expect(trace.locator('pre.trace-output')).toHaveCount(0)
  await expect(page.locator('.conversation-execution time')).toHaveAttribute('datetime', fixture.events.at(-1)!.createdAt!)
  expect(Math.abs((await first.boundingBox())!.y - top)).toBeLessThanOrEqual(3)
  expect(fixture.historyPages.length).toBe(reads)
  expect(fixture.outputReads).toEqual([])
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('interleaved-execution.png'), animations: 'disabled' })
})

test('long chronological execution keeps bounded DOM and loads full outputs only on request', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(90_000)
  const fixture = await mockExecution(page, mockOrigin, deterministicEvents(true))
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await expect.poll(() => fixture.historyPages.length).toBe(1 + Math.ceil(fixture.events.length / 100))
  await expect(trace.locator('.trace-loading')).toHaveCount(0)
  const steps = trace.locator('.trace-steps')
  expect(await steps.locator('.trace-step-row').count()).toBeLessThanOrEqual(50)
  expect(Number(await steps.getAttribute('data-step-count'))).toBeGreaterThan(500)
  expect(fixture.outputReads).toEqual([])
  await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/call-20')
  const title = await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/call-310')
  const node = stepNode(title)
  await readFull(title)
  const expected = fullOutputText('Tool output virtual-middle-output')
  await expect(node.locator('.tool-output-entry pre')).toHaveText(expected)
  expect(fixture.outputReads.filter(path => path.includes('virtual-middle-output'))).toHaveLength(1)
  await readFull(title)
  await expect(node.locator('.trace-step-details')).toHaveCount(0)
  await readFull(title)
  await expect(node.locator('.tool-output-entry pre')).toHaveText(expected)
  await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/call-410', 'end')
  await scrollToTraceStep(page, trace, fixture.runId, 'tool/main/call-310')
  await expect(node.locator('.tool-output-entry pre')).toHaveText(expected)
  expect(fixture.outputReads.filter(path => path.includes('virtual-middle-output'))).toHaveLength(1)
  expect(fixture.nodeReads).toEqual([])
  expect(fixture.unexpected).toEqual([])
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('long-chronological-execution.png'), animations: 'disabled' })
})

test('new execution steps preserve the reading position and expanded outputs remain cached @responsive', async ({ page, mockOrigin }) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const firstOutput = fullOutputText('First paged step')
  for (let step = 1; step <= 100; step++) events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'tool', createdAt: '2026-01-01T12:00:03Z',
    data: { runId, taskId: 'main', turn: 1, status: 'succeeded', tool: { callId: `paged-${step}`, name: 'read_file', summary: `src/paged-${step}.ts` }, content: JSON.stringify({ content: step === 1 ? firstOutput : `Step ${step}.` }) } })
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'model_request', data: { runId, taskId: 'main', turn: 2 } })
  const fixture = await mockExecution(page, mockOrigin, events)
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const steps = trace.locator('.trace-steps')
  await expect(page.locator('.messages')).toHaveAttribute('aria-busy', 'false')
  const first = await scrollToTraceStep(page, trace, runId, 'tool/main/paged-1')
  await readFull(first)
  await expect(stepNode(first).locator('pre.trace-output')).toHaveText(firstOutput)
  const anchor = await scrollToTraceStep(page, trace, runId, 'tool/main/paged-70', 'end')
  const before = { top: (await anchor.boundingBox())!.y, scrollTop: await scrollPosition(page), height: await page.locator('.messages').evaluate(element => element.scrollHeight) }
  fixture.append('tool', { taskId: 'main', turn: 1, status: 'succeeded', tool: { callId: 'paged-101', name: 'read_file', summary: 'src/paged-101.ts' }, content: 'Step 101.' })
  await expect(steps).toHaveAttribute('data-step-count', '101')
  expect(Math.abs((await anchor.boundingBox())!.y - before.top)).toBeLessThanOrEqual(3)
  expect(Math.abs(await scrollPosition(page) - before.scrollTop)).toBeLessThanOrEqual(3)
  expect(await page.locator('.messages').evaluate(element => element.scrollHeight)).toBeGreaterThanOrEqual(before.height)
  await scrollToTraceStep(page, trace, runId, 'tool/main/paged-1')
  await expect(stepNode(first).locator('pre.trace-output')).toHaveText(firstOutput)
  expect(fixture.nodeReads).toEqual([3])
  expect(fixture.unexpected).toEqual([])
})

test('expanded execution history and output restore after their virtual rows are recycled @responsive', async ({ page, mockOrigin }) => {
  test.setTimeout(60_000)
  const events = deterministicEvents(true)
  const runId = events[0].data.runId as string
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'run_finished', createdAt: '2026-01-01T13:00:00Z', data: { runId, status: 'succeeded' } })
  const messages: Message[] = [events[0].data, ...Array.from({ length: 60 }, (_, index): Message => ({
    id: `later-message-${index}`, runId: `later-run-${index}`, role: 'assistant', sequence: 10000 + index,
    content: `Independent later conversation reply ${index + 1}.`, createdAt: '2026-01-01T14:00:00Z',
  }))]
  const fixture = await mockExecution(page, mockOrigin, events, { messages })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  async function earlierPage() {
    await scrollToTraceStep(page, trace, runId, 'tool/main/call-310')
  }
  await expect(page.getByText('Independent later conversation reply 60.', { exact: true })).toBeVisible()
  await earlierPage()
  const title = await scrollToTraceStep(page, trace, runId, 'tool/main/call-310')
  await readFull(title)
  const expected = fullOutputText('Tool output virtual-middle-output')
  await expect(stepNode(title).locator('.tool-output-entry pre')).toHaveText(expected)
  const reads = { history: fixture.historyPages.length, output: fixture.outputReads.length, overview: fixture.overviewReads.filter(path => path.includes(`/runs/${runId}/`)).length }
  await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
  await expect(trace.locator('.trace-step-row')).toHaveCount(0)
  await expect(page.getByText('Independent later conversation reply 60.', { exact: true })).toBeVisible()
  await earlierPage()
  await scrollToTraceStep(page, trace, runId, 'tool/main/call-310')
  await expect(stepNode(title).locator('.tool-output-entry pre')).toHaveText(expected)
  expect({ history: fixture.historyPages.length, output: fixture.outputReads.length, overview: fixture.overviewReads.filter(path => path.includes(`/runs/${runId}/`)).length }).toEqual(reads)
  expect(await trace.locator('.trace-step-row').count()).toBeLessThanOrEqual(50)
  expect(fixture.unexpected).toEqual([])
})

test('continuous history prepares scoped batches and keeps mounted DOM bounded', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  await page.addInitScript(() => {
    const counts = { maxRows: 0 }
    ;(window as Window & { mountedCounts?: typeof counts }).mountedCounts = counts
    new MutationObserver(() => { counts.maxRows = Math.max(counts.maxRows, document.querySelectorAll('.message-row').length) }).observe(document, { childList: true, subtree: true })
  })
  const messages: Message[] = [], events: StreamEvent[] = []
  for (let index = 0; index < 40; index++) {
    const runId = `history-${index}`, sequence = index * 4 + 1, createdAt = '2026-01-01T12:00:00Z'
    const user: Message = { id: `user-${index}`, sequence, runId, role: 'user', content: `Task ${index + 1}`, createdAt }
    const reply: Message = { id: `reply-${index}`, sequence: sequence + 3, runId, role: 'assistant', content: `Completed task ${index + 1}.`, createdAt }
    messages.push(user, reply)
    events.push({ sequence, kind: 'message', createdAt, data: user },
      { sequence: sequence + 1, kind: 'tool', createdAt, data: { runId, status: 'succeeded', taskId: 'main', tool: { callId: `read-${index}`, name: 'read_file', summary: 'Read source' }, content: 'Source preview' } },
      { sequence: sequence + 3, kind: 'run_finished', createdAt, data: { runId, status: 'succeeded', message: reply } })
  }
  const fixture = await mockExecution(page, mockOrigin, events, { messages })
  await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
  await expect(page.getByText('Completed task 40.', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => (window as Window & { mountedCounts?: { maxRows: number } }).mountedCounts!.maxRows)).toBeLessThan(40)
  expect(new Set(fixture.overviewReads).size, 'Opening a history page must not read every offscreen execution').toBeLessThan(40)
  await scrollToConversationEntry(page, '[data-display-key="user-0"]', 1)
  await expect(page.getByText('Task 1', { exact: true })).toBeVisible()
  expect(await page.locator('.timeline-row').count()).toBeLessThan(40)
  expect(fixture.unexpected).toEqual([])
})

test('opening a short conversation keeps following when content grows before a queued scroll event', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  const runId = 'short-history-follow', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 8 }, (_, index) => ({
    id: `short-follow-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `Retained message ${index + 1}.\n\n` + 'Retained conversation paragraph.\n\n'.repeat(5), createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })), { messages })
  const scroller = page.locator('.messages')
  await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  for (let stage = 0; stage < 3; stage++) {
    await scroller.evaluate(element => {
      // A previous scroll-to-bottom can dispatch its scroll event after a
      // delayed execution body has grown, but before ResizeObserver runs.
      element.scrollTo({ top: element.scrollHeight })
      const late = document.createElement('div')
      late.style.height = '350px'
      late.textContent = 'Delayed execution content.'
      element.querySelector('.message-row:last-child .markdown')!.append(late)
      element.dispatchEvent(new Event('scroll'))
    })
    await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  }
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(element => { element.scrollTop = 0 })
  await expect(page.getByText('Retained message 1.', { exact: true })).toBeVisible()
  await scroller.evaluate(element => {
    const late = document.createElement('div')
    late.style.height = '350px'
    element.querySelector('.message-row:last-child .markdown')!.append(late)
    element.dispatchEvent(new Event('scroll'))
  })
  await page.waitForTimeout(200)
  expect(await scrollPosition(page)).toBeLessThanOrEqual(3)
  expect(fixture.unexpected).toEqual([])
})

test('jump to latest prepares pending output and reaches its actual bottom', { tag: '@responsive' }, async ({ page, mockOrigin }, testInfo) => {
  const runId = 'jump-latest', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 60 }, (_, index) => ({
    id: `jump-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `History message ${index + 1}.\n\n` + 'Previously retained history.\n\n'.repeat(index % 7 + 1), createdAt,
  }))
  const content = fullOutputText('Latest reply')
  const path = `/sessions/execution-session/runs/${runId}/model-output?turn=61`
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })),
    { messages, outputContents: { [path]: content } })
  const release = fixture.holdOutput(path)
  const scroller = page.locator('.messages')
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(element => { element.scrollTop = 0 })
  const reply: Message = { id: 'latest-jump-reply', sequence: 61, runId, role: 'assistant', outputTurn: 61,
    outputBytes: Buffer.byteLength(content), content: 'Latest reply preview.', createdAt }
  fixture.messages.push(reply)
  fixture.append('message', reply)
  const jump = page.getByRole('button', { name: 'Jump to latest', exact: true })
  await expect(jump).toBeVisible()
  await jump.click()
  await expect.poll(() => fixture.outputReads.some(read => read.startsWith(path))).toBe(true)
  release()
  const ending = page.getByText('Latest reply ending — 完整输出 ✅.', { exact: true })
  await expect(ending).toBeVisible()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  // Stay at the actual bottom after all nested/outer measurements settle.
  await page.waitForTimeout(200)
  expect(await distanceToLatest(page)).toBeLessThanOrEqual(3)
  await expect(jump).toHaveCount(0)
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(element => { element.scrollTop = 0 })
  const viewport = page.viewportSize()!
  await page.setViewportSize({ ...viewport, width: Math.max(320, viewport.width - 160) })
  await expect(jump).toBeVisible()
  await jump.click()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await page.waitForTimeout(200)
  expect(await distanceToLatest(page)).toBeLessThanOrEqual(3)
  await expect(ending).toBeVisible()
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('jump-latest-hydrated.png'), animations: 'disabled' })
})

test('layout notifications between input and scrolling cannot restore bottom following', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  const runId = 'deferred-input', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 20 }, (_, index) => ({
    id: `deferred-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `Retained paragraph ${index + 1}.\n\n` + 'Read without being pulled to the bottom.\n\n'.repeat(4), createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })), { messages })
  const scroller = page.locator('.messages')
  await expect(page.getByText('Retained paragraph 20.', { exact: true })).toBeVisible()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  // A wheel handler updates virtualizer options before Chromium applies the
  // movement. Let those Vue/layout notifications run at the old bottom.
  await scroller.evaluate(async element => {
    element.dispatchEvent(new WheelEvent('wheel', { deltaY: -300 }))
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    element.scrollTop = (element.scrollHeight - element.clientHeight) * .6
  })
  const jump = page.getByRole('button', { name: 'Jump to latest', exact: true })
  await expect(jump).toBeVisible()
  await page.waitForTimeout(250)
  const before = await scrollPosition(page)
  const reply: Message = { id: 'deferred-reply', sequence: 21, runId, role: 'assistant', content: 'Reply received while browsing.', createdAt }
  fixture.messages.push(reply)
  fixture.append('message', reply)
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', '21')
  await expect(jump).toBeVisible()
  expect(Math.abs(await scrollPosition(page) - before)).toBeLessThanOrEqual(3)
  await jump.click()
  await expect(page.getByText(reply.content, { exact: true })).toBeVisible()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  expect(fixture.unexpected).toEqual([])
})

test('jump to latest survives offset corrections without a total height change', { tag: '@responsive' }, async ({ page, mockOrigin }, testInfo) => {
  const runId = 'jump-measurement', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 60 }, (_, index) => ({
    id: `measurement-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `History ${index + 1}.\n\n` + 'A paragraph whose height can change after loading.\n\n'.repeat(4), createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })), { messages })
  const scroller = page.locator('.messages')
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(element => { element.scrollTop = element.scrollHeight * .35 })
  const jump = page.getByRole('button', { name: 'Jump to latest', exact: true })
  await expect(jump).toBeVisible()
  await page.waitForTimeout(200)
  // Let rows on both sides of the viewport finish their first measurement
  // before moving paragraphs between them during the click.
  await scroller.evaluate(element => { element.scrollTop = element.scrollHeight * .6 })
  await page.waitForTimeout(250)
  await scroller.evaluate(element => {
    const top = element.getBoundingClientRect().top
    const rows = [...element.querySelectorAll<HTMLElement>('.message-row.assistant')]
    if (!rows.some(row => row.getBoundingClientRect().bottom < top)) {
      const first = rows[0]!
      element.scrollTop += first.getBoundingClientRect().bottom - top + 10
    }
  })
  await page.waitForTimeout(250)
  await jump.evaluate(button => {
    button.addEventListener('click', () => {
      const scroller = document.querySelector<HTMLElement>('.messages')!
      const top = scroller.getBoundingClientRect().top
      const rows = [...scroller.querySelectorAll<HTMLElement>('.message-row.assistant')]
      const above = rows.filter(row => row.getBoundingClientRect().bottom < top).at(-1)?.querySelector('.markdown')
      const below = rows.find(row => row.getBoundingClientRect().top >= top)?.querySelector('.markdown')
      if (!above || !below) throw new Error('Expected history rows on both sides of the viewport')
      // Simulate late content measurements which cancel in aggregate. The
      // outer container does not resize, but above-viewport compensation
      // must not cancel the user's journey to the bottom.
      const before = above.getBoundingClientRect().height + below.getBoundingClientRect().height
      for (const paragraph of [...below.querySelectorAll('p')].slice(-3)) above.append(paragraph)
      scroller.dataset.measurementShift = String(above.getBoundingClientRect().height + below.getBoundingClientRect().height - before)
    }, { once: true, capture: true })
  })
  await jump.click()
  await expect(scroller).toHaveAttribute('data-measurement-shift', '0')
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await page.waitForTimeout(200)
  expect(await distanceToLatest(page)).toBeLessThanOrEqual(3)
  await expect(page.getByText('History 60.', { exact: true })).toBeVisible()
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('jump-latest-measurement.png'), animations: 'disabled' })
})

for (const input of ['wheel', 'keyboard', 'touch'] as const) test(`manual scrolling cancels a jump before a new reply changes the target (${input})`, { tag: '@responsive' }, async ({ page, mockOrigin, isMobile }) => {
  test.skip(input === 'touch' && !isMobile, 'Native touch scrolling uses a touch viewport')
  const touch = input === 'touch' ? await page.context().newCDPSession(page) : null
  const runId = 'interrupted-jump', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 30 }, (_, index) => ({
    id: `interrupt-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `Retained message ${index + 1}.\n\n` + 'Retained reading position.\n\n'.repeat(6), createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })), { messages })
  const scroller = page.locator('.messages')
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  // Measure the history first, so the next jump remains a smooth journey
  // which the user can interrupt rather than a resize-triggered snap.
  for (const fraction of [.7, .4, .1, 0]) {
    await scroller.evaluate((element, fraction) => { element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction }, fraction)
    await page.waitForTimeout(50)
  }
  await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
  await expect.poll(() => scrollPosition(page)).toBeGreaterThan(10)
  const box = (await scroller.boundingBox())!
  if (input === 'wheel') {
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.wheel(0, -200)
  } else if (input === 'keyboard') {
    await scroller.evaluate(element => { (element as HTMLElement).tabIndex = 0; (element as HTMLElement).focus({ preventScroll: true }) })
    await page.keyboard.press('Home')
  } else {
    const x = box.x + box.width / 2, y = box.y + box.height * .25
    const send = (type: 'touchStart' | 'touchMove' | 'touchEnd', offset = 0) => touch!.send('Input.dispatchTouchEvent', {
      type, touchPoints: type === 'touchEnd' ? [] : [{ x, y: y + offset, id: 1, radiusX: 1, radiusY: 1 }],
    })
    await send('touchStart')
    for (let step = 1; step <= 5; step++) {
      await send('touchMove', Math.min(180, box.height * .5) * step / 5)
      await page.waitForTimeout(20)
    }
    await page.waitForTimeout(150)
    await send('touchEnd')
    await touch!.detach()
  }
  await expect.poll(() => distanceToLatest(page)).toBeGreaterThan(box.height)
  await page.waitForTimeout(350)
  const anchor = await scroller.evaluate(element => {
    const top = element.getBoundingClientRect().top
    const row = [...element.querySelectorAll<HTMLElement>('.message-row')].find(row => row.getBoundingClientRect().bottom > top)!
    return { key: row.dataset.displayKey!, top: row.getBoundingClientRect().top }
  })
  const reply: Message = { id: 'after-interrupted-jump', sequence: 31, runId, role: 'assistant', content: 'A newer reply while reading history.', createdAt }
  fixture.messages.push(reply)
  fixture.append('message', reply)
  await page.waitForTimeout(350)
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', '31')
  await page.waitForTimeout(200)
  const top = await scroller.locator(`[data-display-key="${anchor.key}"]`).evaluate(element => element.getBoundingClientRect().top)
  expect(Math.abs(top - anchor.top)).toBeLessThanOrEqual(3)
  await expect(page.getByRole('button', { name: 'Jump to latest', exact: true })).toBeVisible()
  expect(fixture.unexpected).toEqual([])
})

test('jump to latest skips unrelated off-page executions @responsive', async ({ page, mockOrigin }) => {
  const runId = 'off-page-execution', createdAt = '2026-01-01T12:00:00Z'
  const user: Message = { id: 'off-page-user', sequence: 31, runId, role: 'user', content: 'Inspect source files.', createdAt }
  const events: StreamEvent[] = [{ sequence: 31, kind: 'message', createdAt, data: user },
    { sequence: 32, kind: 'tool', createdAt, data: { runId, taskId: 'main', status: 'succeeded', tool: { callId: 'off-page-read', name: 'read_file', summary: 'src/file.ts' }, content: 'Source preview.' } },
    { sequence: 33, kind: 'run_finished', createdAt, data: { runId, status: 'succeeded' } }]
  const history = (prefix: string, start: number) => Array.from({ length: 30 }, (_, index): Message => ({
    id: `${prefix}-${index}`, sequence: start + index, runId: `${prefix}-run-${index}`, role: 'assistant', content: `${prefix} reply ${index + 1}.`, createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [...history('Earlier', 1), user, ...history('Latest', 1000)] })
  await expect(page.getByText('Latest reply 30.', { exact: true })).toBeVisible()
  await page.locator('.messages').dispatchEvent('wheel', { deltaY: -100 })
  await page.locator('.messages').evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  await page.waitForTimeout(250)
  await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
  await expect(page.getByText('Latest reply 30.', { exact: true })).toBeVisible()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  expect(fixture.unexpected).toEqual([])
})

test('receiving a new reply retains the continuous history and reading position', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  const runId = 'reading-run', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 20 }, (_, index) => ({
    id: `reading-${index}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user',
    content: `Reading message ${index + 1}.\n\n` + 'Keep this paragraph in view while another reply arrives.\n'.repeat(6), createdAt,
  }))
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })), { messages })
  const scroller = page.locator('.messages')
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', '20')
  await expect(scroller).toHaveAttribute('aria-busy', 'false')
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(async element => {
    element.scrollTop = (element.scrollHeight - element.clientHeight) * .45
    element.dispatchEvent(new Event('scroll'))
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  })
  // A held gesture may show estimated placeholders. Capture actual readable
  // content after it settles; native-scrollbar coverage checks the held phase.
  await expect(scroller.locator('.timeline-placeholder')).toHaveCount(0)
  const anchor = await scroller.evaluate(element => {
    const viewport = element.getBoundingClientRect()
    const row = [...element.querySelectorAll<HTMLElement>('.message-row')].find(row => row.getBoundingClientRect().bottom > viewport.top)!
    return { key: row.dataset.displayKey!, top: row.getBoundingClientRect().top }
  })
  const reply: Message = { id: 'reading-20', sequence: 21, runId, role: 'assistant', content: 'A new reply after the threshold.', createdAt }
  fixture.messages.push(reply)
  fixture.session.executing = false
  fixture.append('run_finished', { runId, status: 'succeeded', message: reply })
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', '21')
  const retained = page.locator('.message-row')
  await expect.poll(() => retained.evaluateAll((rows, key) => rows.some(row => (row as HTMLElement).dataset.displayKey === key), anchor.key)).toBe(true)
  const top = await scroller.evaluate((element, key) => [...element.querySelectorAll<HTMLElement>('.message-row')].find(row => row.dataset.displayKey === key)!.getBoundingClientRect().top, anchor.key)
  expect(Math.abs(top - anchor.top), 'A new reply must preserve the history page being read').toBeLessThanOrEqual(3)
  expect(fixture.unexpected).toEqual([])
})

test('rapid scrolling keeps readable content while a top loader locks and prepares its destination @responsive', async ({ page, mockOrigin }, testInfo) => {
  const runId = 'viewport-loading', createdAt = '2026-01-01T12:00:00Z'
  const messages: Message[] = Array.from({ length: 20 }, (_, index) => ({
    id: `viewport-${index}`, sequence: index * 11 + 1, runId, role: index ? 'assistant' : 'user', createdAt,
    content: `Retained message ${index + 1}.\n\n` + 'A complete paragraph to read at the destination.\n\n'.repeat(12),
  }))
  const events: StreamEvent[] = messages.flatMap(message => [{ sequence: message.sequence, kind: 'message', createdAt, data: message },
    ...Array.from({ length: 10 }, (_, index) => ({ sequence: message.sequence + index + 1, kind: 'tool', createdAt,
      data: { runId, taskId: 'main', status: 'succeeded', tool: { callId: `viewport-tool-${message.id}-${index}`, name: 'read_file', summary: `src/file-${message.id}-${index}.ts` }, content: 'Complete source output.' } })),
  ])
  const fixture = await mockExecution(page, mockOrigin, events, { messages })
  const scroller = page.locator('.messages'), loader = page.locator('.history-loading')
  await expect(scroller).toHaveAttribute('aria-busy', 'false')
  const width = await scroller.evaluate(element => element.clientWidth)
  const origin = await observeScrollLoading(page)
  const reply: Message = { id: 'viewport-new', sequence: events.at(-1)!.sequence + 1, runId, role: 'assistant', createdAt, content: 'Reply received during rapid scrolling.' }
  for (const fraction of [.15, .6, .3]) {
    await scroller.dispatchEvent('wheel', { deltaY: -100 })
    await scroller.evaluate(async (element, fraction) => {
      element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction
      element.dispatchEvent(new Event('scroll'))
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    }, fraction)
    if (fraction === .15) { fixture.messages.push(reply); fixture.append('message', reply) }
    await expect(scroller).toHaveAttribute('aria-busy', 'false')
    await expect(loader).toHaveCount(0)
    await expect(scroller.locator('.message-space')).toBeVisible()
    expect(await scroller.evaluate(element => element.clientWidth)).toBe(width)
  }
  await assertTopScrollLoading(page, origin)
  await expect(scroller.locator('.timeline-placeholder')).toHaveCount(0)
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', '21')
  await page.screenshot({ path: testInfo.outputPath('prepared-scroll-destination.png'), animations: 'disabled' })
  await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
  await expect(page.getByText(reply.content, { exact: true })).toBeVisible()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await expect(loader).toHaveCount(0)
  // Following a new live reply must not blank the prepared conversation.
  await page.evaluate(() => {
    const observer = new MutationObserver(records => {
      if (records.some(record => [...record.addedNodes].some(node => node instanceof Element && (node.matches('.history-loading') || node.querySelector('.history-loading'))))) document.body.dataset.unexpectedHistoryLoader = 'true'
    })
    observer.observe(document.querySelector('.conversation-history')!, { childList: true, subtree: true })
  })
  const latest: Message = { ...reply, id: 'viewport-live', sequence: reply.sequence + 1, content: 'A live reply keeps the conversation visible.' }
  fixture.messages.push(latest)
  fixture.append('message', latest)
  await expect(page.getByText(latest.content, { exact: true })).toBeVisible()
  await expect(page.locator('body')).not.toHaveAttribute('data-unexpected-history-loader', 'true')
  expect(fixture.unexpected).toEqual([])
})

test('earlier history uses one loading indicator and a failed body read exposes Retry', async ({ page, mockOrigin }) => {
  const runId = 'earlier-loading', createdAt = '2026-01-01T12:00:00Z'
  const full = fullOutputText('Earlier body')
  const messages: Message[] = Array.from({ length: 60 }, (_, index) => ({
    id: `earlier-${index + 1}`, sequence: index + 1, runId, role: index ? 'assistant' : 'user', createdAt,
    content: `Retained message ${index + 1}.\n\n` + 'A retained paragraph.\n\n'.repeat(3),
    ...(index === 39 ? { outputTurn: 40, outputBytes: Buffer.byteLength(full) } : {}),
  }))
  const path = `/sessions/execution-session/runs/${runId}/model-output?turn=40`
  const fixture = await mockExecution(page, mockOrigin, messages.map(message => ({ sequence: message.sequence, kind: 'message', createdAt, data: message })),
    { messages, outputContents: { [path]: full }, outputFailures: { [path]: 1 } })
  const release = fixture.holdOutput(path)
  const scroller = page.locator('.messages'), loader = page.locator('.history-loading')
  await expect(scroller).toHaveAttribute('aria-busy', 'false')
  try {
    await scroller.dispatchEvent('wheel', { deltaY: -100 })
    await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
    await expect.poll(() => fixture.outputReads.some(read => read.startsWith(path))).toBe(true)
    await expect(loader).toHaveCount(1)
    await expect(loader).toBeVisible()
    release()
    await expect(loader).toHaveCount(0)
    await expect(scroller).toHaveAttribute('aria-busy', 'false')
    await scrollToConversationEntry(page, '[data-display-key="earlier-40"]', 40)
    const row = scroller.locator('[data-display-key="earlier-40"]')
    await expect(row.locator('.message-output-error')).toBeVisible()
    await row.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(row.locator('.message-output-error')).toHaveCount(0)
    await expect(loader).toHaveCount(0)
    expect(fixture.unexpected).toEqual([])
  } finally { release() }
})

test('historical full text is prepared before display and virtual recycling does not refetch it @responsive', async ({ page, mockOrigin }) => {
  const events = hydrationEvents()
  const runId = events[0].data.runId as string
  const full = fullOutputText('Prepared historical reply')
  const reply: Message = { id: 'prepared-reply', sequence: events.at(-1)!.sequence + 1, runId, role: 'assistant', content: 'Short preview.', outputTurn: 3, outputBytes: Buffer.byteLength(full), createdAt: '2026-01-01T12:04:00Z' }
  events.push({ sequence: reply.sequence, kind: 'run_finished', data: { runId, status: 'succeeded', message: reply } })
  const path = `/sessions/execution-session/runs/${runId}/model-output?turn=3`
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [events[0].data, reply], outputContents: { [path]: full }, outputDelayMs: 500, historyDelayMs: 250, indexDelayMs: 350 })
  await expect(page.locator('.history-loading')).toBeVisible()
  await expect(page.locator('.message-row').first()).not.toBeVisible()
  await expect(page.locator('.messages')).toHaveAttribute('aria-busy', 'false')
  await expect(page.getByText('Prepared historical reply ending — 完整输出 ✅.', { exact: true })).toBeVisible()
  const reads = { history: fixture.historyPages.length, outputs: fixture.outputReads.length, overview: fixture.overviewReads.length }
  const heights: number[] = []
  for (const fraction of [0, .25, .5, .75, 1, .5, 0, 1]) {
    await page.locator('.messages').dispatchEvent('wheel', { deltaY: fraction ? 100 : -100 })
    heights.push(await page.locator('.messages').evaluate((element, fraction) => { element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction; return element.scrollHeight }, fraction))
    await page.waitForTimeout(60)
  }
  expect(heights.every(height => height > 0)).toBe(true)
  expect(await page.locator('.timeline-row').count()).toBeLessThan(30)
  expect({ history: fixture.historyPages.length, outputs: fixture.outputReads.length, overview: fixture.overviewReads.length }).toEqual(reads)
  expect(fixture.unexpected).toEqual([])
})

test('current action, timing and Todo stay above the composer through live work and reload', { tag: '@smoke' }, async ({ page, mockOrigin }, testInfo) => {
  const source = independentWindowEvents()
  const runId = source[0].data.runId as string
  source.push({ sequence: source.at(-1)!.sequence + 1, kind: 'checklist', createdAt: source.at(-1)!.createdAt, data: { runId, items: initialChecklist } })
  const fixture = await mockExecution(page, mockOrigin, source)
  const dock = page.locator('.conversation-execution')
  const checklist = page.locator('.conversation-checklist')
  const assertDock = async () => {
    await expect(dock).toBeVisible()
    await expect(checklist).toContainText('Analyze duplicate images')
    expect(await dock.evaluate(element => !!element.closest('.messages'))).toBe(false)
    const geometry = await dock.evaluate(element => ({ bottom: element.getBoundingClientRect().bottom, height: innerHeight, sender: document.querySelector('.sender-card')!.getBoundingClientRect().top }))
    expect(geometry.bottom).toBeLessThanOrEqual(geometry.sender)
    expect(geometry.bottom).toBeLessThan(geometry.height)
  }
  await assertDock()
  fixture.append('tool', { taskId: 'main', status: 'started', tool: { callId: 'long-action', name: 'shell', summary: 'Build project', input: { command: 'make build' } } })
  await expect(dock).toContainText('make build')
  await expect(dock.locator('time')).toHaveAttribute('datetime', fixture.events.at(-1)!.createdAt!)
  await page.locator('.messages').evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  await assertDock()
  await page.screenshot({ path: testInfo.outputPath('current-execution-dock.png'), animations: 'disabled' })
  await page.reload()
  await expect(dock).toContainText('make build')
  await assertDock()
  fixture.session.executing = false
  fixture.append('run_finished', { status: 'succeeded' })
  await expect(dock).toHaveCount(0)
  await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
  await expect(page.locator('.messages [data-checklist-summary]')).toHaveCount(1)
  await expect(page.locator('.checklist-summary-text')).toContainText('incomplete: Analyze duplicate images')
  await expect(checklist).toHaveCount(0)
  await expect(page.locator('.run-trace')).toHaveAttribute('data-run-status', 'succeeded')
  await page.reload()
  await expect(dock).toHaveCount(0)
  await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
  await expect(page.locator('.messages [data-checklist-summary]')).toHaveCount(1)
  await expect(page.locator('.checklist-summary-text')).toContainText('incomplete: Analyze duplicate images')
  await expect(checklist).toHaveCount(0)
  expect(fixture.unexpected).toEqual([])
})

test('stopping a run shows interrupted Todo immediately and after reload without changing saved statuses', async ({ page, mockOrigin }, testInfo) => {
  await page.addInitScript(() => {
    localStorage.setItem('vista-debug-mode', 'true')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (text: string) => { (window as Window & { copiedText?: string }).copiedText = text } } })
  })
  const events = agentIdentityEvents()
  const runId = events[0].data.runId as string
  const savedItems: ChecklistItem[] = [...initialChecklist,
    { text: 'Optional follow-up', status: 'pending' },
    { text: 'Waiting for source access', status: 'blocked' },
  ]
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'checklist', createdAt: '2026-01-01T12:01:00Z', data: { runId, items: savedItems } })
  const fixture = await mockExecution(page, mockOrigin, events)
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const overview = trace
  const checklist = page.locator('.conversation-checklist')
  const header = checklist
  await expect(overview).toHaveAttribute('data-run-status', 'active')
  await expect(checklist.locator('[data-checklist-status="in_progress"]')).toHaveCount(2)
  await expect(header).toContainText('1/5 checklist items complete')

  fixture.session.executing = false
  fixture.append('agent_task', { taskId: 'main', task: { id: 'main', title: 'Main task', status: 'canceled' } })
  fixture.append('run_finished', { status: 'canceled' })
  const assertStoppedChecklist = async () => {
    await expect(overview).toHaveAttribute('data-run-status', 'canceled')
    await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
    await expect(page.locator('.messages [data-checklist-summary]')).toHaveCount(1)
    await expect(trace.locator('.trace-step-row')).toHaveCount(4)
    await expect(checklist).toHaveCount(0)
    const summary = page.locator('.checklist-summary-text')
    await expect(summary).toHaveText('Tasks completed: 1/5\n1. done: Check message composer\n2. interrupted: Analyze duplicate images\n3. interrupted: Check mobile navigation\n4. pending: Optional follow-up\n5. blocked: Waiting for source access')
    await expect(summary.locator('svg, .checklist, [data-checklist-status]')).toHaveCount(0)
    await expect(page.locator('[data-checklist-summary] time')).toHaveAttribute('datetime', fixture.events.at(-1)!.createdAt!)
  }
  await assertStoppedChecklist()
  await page.locator('.messages [data-checklist-summary]').getByRole('button', { name: 'Copy reply', exact: true }).click()
  const copied = await page.evaluate(() => (window as Window & { copiedText?: string }).copiedText)
  expect(copied).toBe(await page.locator('.checklist-summary-text').innerText())
  expect(copied).toContain('1. done: Check message composer')
  expect(copied).toContain('2. interrupted: Analyze duplicate images')
  for (const item of savedItems) expect(copied).toContain(item.text)
  await page.locator('.messages [data-checklist-summary]').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('stopped-todo.png'), animations: 'disabled' })
  await trace.getByRole('button', { name: 'Debug details', exact: true }).click()
  const diagnostics = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await expect(diagnostics).toBeVisible()
  const diagnosticEvents = JSON.parse(await diagnostics.locator('.trace-debug-body').innerText()).events as StreamEvent[]
  expect(diagnosticEvents.find(event => event.kind === 'checklist')!.data.items, 'Only visible statuses change; the source event remains intact').toEqual(savedItems)
  await page.keyboard.press('Escape')
  await page.reload()
  await assertStoppedChecklist()
  expect(fixture.events.find(event => event.kind === 'checklist')!.data.items).toEqual(savedItems)
  expect(fixture.unexpected).toEqual([])
  await page.locator('.messages [data-checklist-summary]').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('stopped-todo-reloaded.png'), animations: 'disabled' })
})

test('new runs clear the composer checklist without changing earlier saved plans', async ({ page, mockOrigin }) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const oldItems = [{ text: 'Earlier unfinished work', status: 'in_progress' }]
  const oldReply: Message = { id: 'old-reply', runId, sequence: 6, role: 'assistant', content: 'Earlier execution failed.', failure: true, createdAt: events[0].createdAt! }
  const checklistTool = { callId: 'old-checklist', name: 'update_checklist', summary: 'Plan earlier work' }
  events.push({ sequence: 3, kind: 'tool', data: { runId, status: 'started', tool: checklistTool } })
  events.push({ sequence: 4, kind: 'checklist', data: { runId, items: oldItems } })
  events.push({ sequence: 5, kind: 'tool', data: { runId, status: 'succeeded', tool: checklistTool, content: 'Checklist updated.' } })
  events.push({ sequence: 6, kind: 'run_finished', data: { runId, status: 'failed', message: oldReply } })
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [events[0].data, oldReply] })
  const oldTrace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const newRun = 'run-without-checklist'
  const newTrace = page.locator(`.run-trace[data-run-id="${newRun}"]`)
  await expect(page.locator('.checklist-summary-text')).toContainText('1. incomplete: Earlier unfinished work')
  await expect(page.locator('.conversation-checklist')).toHaveCount(0)
  const user: Message = { id: 'new-user', runId: newRun, sequence: 7, role: 'user', content: 'A different task', createdAt: new Date().toISOString() }
  fixture.messages.push(user)
  fixture.session.executing = true
  fixture.append('message', user)
  fixture.append('model_request', { runId: newRun, turn: 1, taskId: 'main', provider: 'openai-chat', id: 'gpt-6-astra' })
  const tool = { callId: 'new-read', name: 'read_file', summary: 'Read another file' }
  fixture.append('tool', { runId: newRun, turn: 1, taskId: 'main', status: 'started', tool })
  const assertSeparate = async () => {
    await expect(newTrace).toBeVisible()
    await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
    await expect(page.locator('.messages [data-checklist-summary]')).toHaveCount(1)
    await expect(oldTrace.locator('.checklist')).toHaveCount(0)
    await expect(newTrace.locator('.checklist')).toHaveCount(0)
  }
  await assertSeparate()
  await page.reload()
  await assertSeparate()
  const newItems: ChecklistItem[] = [{ text: 'Verify the new task', status: 'in_progress' }]
  fixture.append('checklist', { runId: newRun, items: newItems })
  await expect(page.locator('.conversation-column > .conversation-checklist')).toContainText('Verify the new task')
  await expect(page.locator('.conversation-column > .conversation-checklist')).not.toContainText('Earlier unfinished work')
  fixture.append('checklist', { runId: newRun, items: [] })
  await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
  fixture.append('checklist', { runId: newRun, items: [{ ...newItems[0], status: 'done' }] })
  const checklistTime = fixture.events.at(-1)!.createdAt!
  fixture.append('tool', { runId: newRun, turn: 1, taskId: 'main', status: 'succeeded', tool, content: 'File read.' })
  const reply: Message = { id: 'new-reply', runId: newRun, sequence: events.at(-1)!.sequence + 1, role: 'assistant', content: 'New task completed.', createdAt: new Date().toISOString() }
  fixture.messages.push(reply)
  fixture.session.executing = false
  fixture.append('run_finished', { runId: newRun, status: 'succeeded', message: reply })
  await expect(newTrace).toHaveAttribute('data-run-status', 'succeeded')
  await expect(page.locator('.checklist-summary-text').last()).toHaveText('Tasks completed: 1/1\n1. done: Verify the new task')
  await expect(page.locator('[data-checklist-summary]').last().locator('time')).toHaveAttribute('datetime', checklistTime)
  const assertChecklistSpacing = async () => {
    const summary = page.locator('.checklist-message').last()
    await summary.scrollIntoViewIfNeeded()
    const gap = await summary.evaluate(element => {
      const row = element.closest('.message-row')!
      const previous = row.previousElementSibling!
      const content = previous.querySelector('article:last-child') || previous
      return element.getBoundingClientRect().top - content.getBoundingClientRect().bottom
    })
    expect(gap, 'The final checklist is a separate message with visible space above it').toBeGreaterThanOrEqual(20)
  }
  const assertChecklistOrder = async () => {
    expect(await page.locator('[data-checklist-summary]').last().evaluate((summary, replyId) => {
      const reply = document.querySelector(`[data-display-key="${replyId}"]`)
      return !!reply && !!(summary.compareDocumentPosition(reply) & Node.DOCUMENT_POSITION_FOLLOWING)
    }, reply.id), 'The completed checklist precedes the later final reply').toBe(true)
  }
  await assertChecklistOrder()
  await assertChecklistSpacing()
  await page.reload()
  await expect(page.locator('.checklist-summary-text').last()).toHaveText('Tasks completed: 1/1\n1. done: Verify the new task')
  await expect(page.locator('[data-checklist-summary]').last().locator('time')).toHaveAttribute('datetime', checklistTime)
  await assertChecklistOrder()
  await assertChecklistSpacing()
  await expect(page.locator('.conversation-column > .conversation-checklist')).toHaveCount(0)
  await expect(page.locator('.messages [data-checklist-summary]')).toHaveCount(2)
  expect(fixture.events.find(event => event.kind === 'checklist')!.data.items).toEqual(oldItems)
  expect(fixture.unexpected).toEqual([])
})

test('child messages retain their stable identities, full assignments and delegation @responsive', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockExecution(page, mockOrigin, agentIdentityEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await expect(trace.locator('.trace-step-row')).toHaveCount(4)
  expect(await trace.locator('.trace-step-row').evaluateAll(rows => rows.map(row => row.getAttribute('data-task-id')))).toEqual(['main', 'child-a', 'child-b', 'grandchild-a'])
  const identities = [
    { id: 'child-a', number: 1, depth: 1, parent: 'Main session' },
    { id: 'child-b', number: 2, depth: 1, parent: 'Main session' },
    { id: 'grandchild-a', number: 3, depth: 2, parent: 'Child session 1' },
  ]
  for (const identity of identities) {
    const row = trace.locator(`.trace-step-row[data-task-id="${identity.id}"]`)
    const heading = row.locator('.trace-agent-message-heading')
    await expect(heading).toHaveAttribute('data-agent-number', String(identity.number))
    await expect(heading).toHaveAttribute('data-agent-depth', String(identity.depth))
    await expect(heading.locator('.trace-agent-role')).toHaveText(`Child session ${identity.number}`)
    await expect(heading.locator('.trace-agent-delegator')).toHaveText(`Delegated by ${identity.parent}`)
    const assignment = heading.locator('.trace-agent-assignment')
    await expect(assignment).not.toHaveAttribute('open', '')
    await expect(assignment.locator('summary')).toContainText('Inspect the shared source folder')
    await expect(assignment.locator('.trace-agent-purpose')).not.toBeVisible()
    await assignment.locator('summary').click()
    await expect(assignment.locator('.trace-agent-purpose')).toBeVisible()
    await expect(assignment.locator('.trace-agent-purpose')).toContainText('完整委派说明结束。')
    await assignment.locator('summary').click()
    await readFull(row.locator('.ui-thought-chain-node-header'))
    await expect(row).toContainText(`Independent result from ${identity.id}.`)
  }
  await expect(trace.locator('.ui-think')).toHaveCount(0)
  await assertSessionTerminology(page)
  await trace.locator('.trace-agent-message-heading').first().scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('inline-agent-identities.png'), animations: 'disabled' })
})

test('webfetch artifacts preserve large page text including JSON fields after refresh', async ({ page, mockOrigin }) => {
  const runId = 'run-webfetch-output'
  const full = JSON.stringify({ content: '网页正文 中文 🙂.\n'.repeat(12000), artifactId: 'this field belongs to the fetched page', ending: 'FINAL WEB PAGE EVIDENCE' })
  expect(Buffer.byteLength(full)).toBeGreaterThan(128 * 1024)
  const events: StreamEvent[] = []
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId, taskId: 'main', ...data } })
  }
  add('message', { id: 'fetch-user', role: 'user', sequence: 1, content: 'Read the large web page.', createdAt: '2026-01-01T12:00:00Z' })
  add('model_request', { turn: 1, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  const tool = { callId: 'fetch-page', name: 'webfetch', summary: 'https://example.com/page.json', outputRef: { artifactId: 'webfetch-artifact', bytes: Buffer.byteLength(full) } }
  add('tool', { turn: 1, status: 'started', tool, content: '' })
  add('artifact', { id: 'webfetch-artifact', callId: tool.callId, status: 'succeeded', bytes: tool.outputRef.bytes, complete: true })
  add('tool', { turn: 1, status: 'succeeded', tool, content: JSON.stringify({ content: full.slice(0, 8192), complete: true, truncated: true, artifactId: 'webfetch-artifact', nextOffset: 8192 }) })
  add('model_result', { turn: 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  const fullPath = '/sessions/execution-session/artifacts/webfetch-artifact/output'
  const fixture = await mockExecution(page, mockOrigin, events, { outputContents: { [fullPath]: full } })
  for (let pass = 0; pass < 2; pass++) {
    if (pass) await page.reload()
    const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
    await readFull(nodeTitle(trace, runId, 'tool/main/fetch-page'))
    const output = trace.locator('[data-output-id="webfetch-artifact"] pre.trace-output')
    await expect(output).toHaveCount(1)
    await expect.poll(() => output.textContent()).toBe(full)
    expect(fixture.outputReads.filter(path => path === fullPath + '?full=1')).toHaveLength(pass + 1)
    expect(await output.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(2)
  }
  expect(fixture.unexpected).toEqual([])
})

for (const theme of ['light', 'dark']) test(`write_file diffs load on expansion, preserve line changes and render bounded rows after refresh (${theme})`, async ({ page, mockOrigin }, testInfo) => {
  await page.addInitScript(theme => localStorage.setItem('vista-appearance', theme), theme)
  const runId = 'run-write-diff'
  const unified = '--- source.ts\n+++ source.ts\n@@ -1,2 +1,4000 @@\n unchanged\n-previous 中文🙂\n' + Array.from({ length: 3999 }, (_, index) => `+${index === 0 ? '<script>window.diffExecuted = true</script>' : `new line ${index + 2}`}`).join('\n') + '\n'
  const path = '/synthetic/source.ts'
  const full = JSON.stringify({ path, sha256: 'saved-hash', diff: { path, created: false, unified } })
  const events: StreamEvent[] = []
  const add = (kind: string, data: object) => {
    const sequence = events.length + 1
    events.push({ sequence, kind, createdAt: new Date(Date.UTC(2026, 0, 1, 12) + sequence * 1000).toISOString(), data: { runId, taskId: 'main', ...data } })
  }
  add('message', { id: 'write-user', role: 'user', sequence: 1, content: 'Update the source file.', createdAt: '2026-01-01T12:00:00Z' })
  add('model_request', { turn: 1, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
  const tool = { callId: 'write-source', name: 'write_file', summary: path, outputRef: { artifactId: 'write-diff', bytes: Buffer.byteLength(full) } }
  add('tool', { turn: 1, status: 'started', tool, content: '' })
  add('artifact', { id: 'write-diff', callId: tool.callId, status: 'succeeded', bytes: tool.outputRef.bytes, complete: true })
  add('tool', { turn: 1, status: 'succeeded', tool, content: JSON.stringify({ path, sha256: 'saved-hash', artifactId: 'write-diff', bytes: tool.outputRef.bytes }) })
  add('model_result', { turn: 1, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  const fullPath = '/sessions/execution-session/artifacts/write-diff/output'
  const fixture = await mockExecution(page, mockOrigin, events, { outputContents: { [fullPath]: full } })
  for (let pass = 0; pass < 2; pass++) {
    if (pass) await page.reload()
    const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
    expect(fixture.outputReads).toHaveLength(pass)
    await readFull(nodeTitle(trace, runId, 'tool/main/write-source'))
    const diff = trace.locator('.file-diff')
    await expect(diff.locator('.file-diff-heading')).toContainText('File updated')
    await expect(diff.locator('.file-diff-remove')).toContainText('previous 中文🙂')
    await expect(diff.locator('.file-diff-add').first()).toContainText('<script>window.diffExecuted = true</script>')
    expect(await page.evaluate(() => (window as any).diffExecuted)).toBeUndefined()
    expect(await diff.locator('.file-diff-line').count()).toBeLessThan(50)
    expect(fixture.outputReads.filter(path => path === fullPath + '?full=1')).toHaveLength(pass + 1)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(2)
    await diff.locator('.file-diff-viewport').evaluate(element => { element.scrollTop = element.scrollHeight })
    await expect(diff.locator('.file-diff-line').last()).toContainText('new line 4000')
    await diff.locator('.file-diff-viewport').evaluate(element => { element.scrollTop = 0 })
    await expect(diff.locator('.file-diff-remove')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`write-file-diff-${pass}.png`), animations: 'disabled' })
  }
  const partialTool = { callId: 'write-diff-unavailable', name: 'write_file', summary: path, outputRef: { artifactId: 'partial-write-diff', bytes: 4 } }
  fixture.append('tool', { status: 'started', tool: partialTool, content: '' })
  fixture.append('artifact', { id: 'partial-write-diff', callId: partialTool.callId, status: 'failed', bytes: 4, complete: false, error: 'Diff storage failed' })
  fixture.append('tool', { status: 'succeeded', tool: partialTool, content: JSON.stringify({ path, sha256: 'committed-hash', diff: { path, created: true, unified: '', omitted: 'unavailable' } }) })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  await readFull(nodeTitle(trace, runId, 'tool/main/write-diff-unavailable'))
  await expect(trace.locator('.file-diff').last()).toContainText('The file was written, but the diff could not be saved.')
  expect(fixture.outputReads.some(path => path.includes('partial-write-diff'))).toBe(false)
  expect(fixture.unexpected).toEqual([])
})

test('execution tool results render once, keep useful summaries, and move metadata to one debug dialog', async ({ page, mockOrigin }, testInfo) => {
  await page.addInitScript(() => localStorage.setItem('vista-debug-mode', 'true'))
  const source = cleanToolOutputEvents()
  const runId = source.events[0].data.runId as string
  const fullPath = '/sessions/execution-session/artifacts/artifact-full/output'
  const fixture = await mockExecution(page, mockOrigin, source.events, { outputContents: {
    [fullPath]: source.full,
    '/sessions/execution-session/artifacts/artifact-error/output': '/synthetic\n',
    '/sessions/execution-session/artifacts/artifact-search/output': source.searchFull,
  } })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const summary = trace
  const diagnosticsButton = summary.getByRole('button', { name: 'Debug details', exact: true })
  await expect(diagnosticsButton).toHaveCount(1)
  await diagnosticsButton.click()
  const closedDiagnostics = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await expect(closedDiagnostics).toBeVisible()
  await expect(trace.locator('.trace-step-row')).not.toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(closedDiagnostics).not.toBeVisible()
  await expect(trace.locator('.trace-steps')).toBeVisible()
  const fullTitle = nodeTitle(trace, runId, 'tool/main/shell-full')
  await expect(stepNode(fullTitle).locator('.trace-input-preview')).toHaveText('cat src/example.ts')
  await expect(stepNode(fullTitle).locator('button.ui-thought-chain-node-header')).toHaveAttribute('title', 'cat src/example.ts')
  await readFull(fullTitle)
  const fullNode = fullTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
  await expect(fullNode.locator('pre.trace-output')).toHaveCount(1)
  await expect(fullNode.locator('pre.trace-output')).toHaveText(source.full)
  expect(fixture.outputReads.filter(path => path.includes('artifact-full'))).toEqual([fullPath + '?full=1'])

  const failedTitle = nodeTitle(trace, runId, 'tool/main/shell-error')
  await readFull(failedTitle)
  const failedNode = failedTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
  await expect(failedNode.locator('p.tool-output-error')).toHaveCount(1)
  await expect(failedNode.locator('p.tool-output-error')).toHaveText(source.error)
  await expect(failedNode.locator('pre.trace-output')).toHaveText('/synthetic\n')
  await readFull(nodeTitle(trace, runId, 'tool/main/source-search'))
  await expect(trace.locator('.trace-search-results li')).toHaveCount(1)
  await expect(trace.locator('.trace-search-results')).toContainText('src/example.ts:2')
  await expect(trace.locator('.trace-search-results')).toContainText('Search output preserved.')
  await expect(trace).not.toContainText('Structured search preview replaced by its full result.')
  await readFull(nodeTitle(trace, runId, 'tool/main/source-read'))
  await expect(trace.locator('pre.trace-output').filter({ hasText: 'Source read once.' })).toHaveCount(1)

  for (const [callId, text] of [['empty-skills', 'No skills found'], ['checklist-updated', 'Checklist updated · 1 items'], ['delegate-started', 'Child session started']]) {
    const title = nodeTitle(trace, runId, `tool/main/${callId}`)
    const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
    await readFull(title)
    await expect(node.getByText(text, { exact: true })).toHaveCount(1)
    await expect(node.locator('.ui-thought-chain-node-collapse-icon')).toHaveCount(1)
    await expect(node.locator('.trace-read-full')).toHaveCount(0)
  }
  await expect(trace.locator('details.trace-details, details.tool-output-technical')).toHaveCount(0)
  await expect(trace.getByText('Technical details', { exact: true })).toHaveCount(0)
  const innerScroll = await trace.locator('pre.trace-output').evaluateAll(elements => elements.map(element => ({
    height: element.clientHeight, scrollHeight: element.scrollHeight, overflowY: getComputedStyle(element).overflowY,
  })))
  for (const output of innerScroll) {
    expect(output.scrollHeight).toBeLessThanOrEqual(output.height + 2)
    expect(output.overflowY).not.toBe('scroll')
  }
  await diagnosticsButton.click()
  const debug = page.getByRole('dialog', { name: 'Debug details' })
  await expect(debug).toBeVisible()
  await expect(debug).toContainText('shell-full')
  await expect(debug).toContainText('cat src/example.ts')
  await page.keyboard.press('Escape')
  await expect(debug).not.toBeVisible()
  await expect(fullNode.locator('pre.trace-output'), 'Opening and closing debug details preserves the expanded execution output').toHaveText(source.full)
  await readFull(fullTitle)
  await fullTitle.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('clean-tool-results.png'), animations: 'disabled' })
  expect(fixture.unexpected).toEqual([])
})

test('reasoning headings and long commands stay readable without hiding execution messages @responsive', async ({ page, mockOrigin }, testInfo) => {
  const source = cleanToolOutputEvents()
  const command = "python3 - <<'PY'\n" + Array.from({ length: 8 }, (_, index) => `print('Synthetic command line ${index + 1}')`).join('\n') + '\nPY'
  for (const event of source.events) if (event.kind === 'tool' && event.data.tool.callId === 'shell-full') event.data.tool.input.command = command
  const fixture = await mockExecution(page, mockOrigin, source.events)
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const title = nodeTitle(trace, fixture.runId, 'tool/main/shell-full')
  const node = stepNode(title)
  const header = node.locator('button.ui-thought-chain-node-header')
  await title.scrollIntoViewIfNeeded()
  await expect(node.locator('.trace-input-full')).not.toBeVisible()
  const preview = node.locator('.trace-input-preview')
  const previewHeight = await node.locator('.ui-thought-chain-node-title').evaluate(element => ({ height: element.getBoundingClientRect().height, line: parseFloat(getComputedStyle(element).lineHeight) }))
  expect(previewHeight.height).toBeLessThanOrEqual(previewHeight.line * 2 + 1)
  const alignment = await header.evaluate(element => {
    const icon = element.querySelector('.ui-thought-chain-node-icon svg')!.getBoundingClientRect()
    const title = element.querySelector('.ui-thought-chain-node-title')!
    const lineCenter = title.getBoundingClientRect().top + parseFloat(getComputedStyle(title).lineHeight) / 2
    return Math.abs((icon.top + icon.bottom) / 2 - lineCenter)
  })
  expect(alignment, 'Status icons align with the first title line even when the target wraps').toBeLessThanOrEqual(2)
  await expect(header).toHaveAttribute('aria-expanded', 'false')
  expect(fixture.outputReads).toEqual([])
  await header.click()
  await expect(node.locator('.trace-input-full')).toHaveText(command)
  await expect(node.locator('.trace-input-full')).toBeVisible()
  await header.click()
  await expect(node.locator('.trace-input-full')).not.toBeVisible()
  await assertNoHorizontalOverflow(page)
  expect(fixture.outputReads).toHaveLength(1)

  const fragments = ['**Inspecting ', 'source**', '**核对测试**', '**Finishing**']
  for (const content of fragments) fixture.append('model_reasoning_delta', { taskId: 'main', turn: 2, content })
  const reasoning = trace.locator('.trace-reasoning').last()
  await reasoning.scrollIntoViewIfNeeded()
  await expect(reasoning.locator('strong')).toHaveCount(3)
  await expect(reasoning.locator('br')).toHaveCount(2)
  await expect(reasoning).not.toContainText('****')
  const headings = await reasoning.locator('strong').evaluateAll(elements => elements.map(element => element.getBoundingClientRect().top))
  expect(headings[1]).toBeGreaterThan(headings[0])
  expect(headings[2]).toBeGreaterThan(headings[1])
  expect(fixture.events.filter(event => event.kind === 'model_reasoning_delta').map(event => event.data.content)).toEqual(fragments)
  await expect(trace.locator('.trace-step-row')).toHaveCount(8)
  await assertNoHorizontalOverflow(page)
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('readable-reasoning-and-command.png'), animations: 'disabled' })
})

test('tool output failure falls back to one inline preview and Retry replaces it with the full result', async ({ page, mockOrigin }, testInfo) => {
  const source = cleanToolOutputEvents()
  const runId = source.events[0].data.runId as string
  const fullPath = '/sessions/execution-session/artifacts/artifact-full/output'
  const fixture = await mockExecution(page, mockOrigin, source.events, { outputContents: { [fullPath]: source.full }, outputFailures: { [fullPath]: 1 } })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const title = nodeTitle(trace, runId, 'tool/main/shell-full')
  await readFull(title)
  const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
  await expect(node.locator('.trace-output-load-error')).toBeVisible()
  await expect(node.locator('pre.trace-output')).toHaveCount(1)
  await expect(node.locator('pre.trace-output')).toHaveText(source.full.slice(0, 1024))
  await node.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(node.locator('.trace-output-load-error')).toHaveCount(0)
  await expect(node.locator('pre.trace-output')).toHaveCount(1)
  await expect(node.locator('pre.trace-output')).toHaveText(source.full)
  expect(fixture.outputReads).toEqual([fullPath + '?full=1', fullPath + '?full=1'])
  await readFull(title)
  await expect(node.locator('.trace-step-details')).toHaveCount(0)
  await readFull(title)
  await expect(node.locator('pre.trace-output'), 'Reopening a recovered tool reuses the successful full snapshot').toHaveText(source.full)
  expect(fixture.outputReads).toHaveLength(2)
  expect(fixture.unexpected).toEqual([])
  await title.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('tool-output-retry.png'), animations: 'disabled' })
})

test('grouped search results keep full paths and permission warnings after refresh @responsive', async ({ page, mockOrigin }) => {
  const events = hydrationEvents()
  const searched = events.find(event => event.kind === 'tool' && event.data.tool.callId === 'search-title' && event.data.status === 'succeeded')!
  const original = JSON.parse(searched.data.content)
  searched.data.content = JSON.stringify({ root: '/project', files: original.matches.map((match: any) => ({ path: match.path, matches: [{ line: match.line, text: match.text }] })), count: 2, filesScanned: 7, truncated: true,
    skippedDirectories: 1, warnings: [{ path: '/project/locked', error: 'permission denied' }] })
  const fixture = await mockExecution(page, mockOrigin, events)
  for (const reload of [false, true]) {
    if (reload) await page.reload()
    const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
    const title = nodeTitle(trace, fixture.runId, 'tool/main/search-title')
    await readFull(title)
    const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
    await expect(node.locator('.trace-search-results li')).toHaveCount(2)
    await expect(node.locator('.trace-search-results')).toContainText('/project/src/title.test.ts:24')
    await expect(node).toContainText('/project/locked: permission denied')
    await expect(node).toContainText('The tool returned a partial list of matches.')
    await expect(node.locator('.trace-result-summary').first()).toHaveText('2 matches · 7 files scanned')
  }
})

test('truncated file and search steps hydrate on expansion, and jump to latest resumes following', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockExecution(page, mockOrigin, hydrationEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const readEvent = fixture.events.find(event => event.kind === 'tool' && event.data.tool.callId === 'read-source' && event.data.status === 'succeeded')!
  const searchEvent = fixture.events.find(event => event.kind === 'tool' && event.data.tool.callId === 'search-title' && event.data.status === 'succeeded')!
  await expect(trace).toHaveAttribute('data-run-status', 'active')
  expect(fixture.nodeReads).toEqual([])
  const readTitle = nodeTitle(trace, fixture.runId, 'tool/main/read-source')
  const searchTitle = nodeTitle(trace, fixture.runId, 'tool/main/search-title')
  await readFull(readTitle)
  const readNode = readTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
  await expect(readNode.locator('pre.trace-output').first()).toContainText('Hydrated file ending.')
  await expect.poll(() => fixture.nodeReads).toEqual([readEvent.sequence])
  await expect(readNode.locator('details.trace-details')).toHaveCount(0)
  await expect(readNode.locator('.trace-read-full')).toHaveCount(0)
  await readTitle.click()
  expect(fixture.nodeReads).toEqual([readEvent.sequence])
  await readFull(searchTitle)
  const searchNode = searchTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," ui-thought-chain-node ")][1]')
  await expect(searchNode.locator('.trace-search-results li')).toHaveCount(2)
  await expect(searchNode.locator('.trace-search-results')).toContainText('src/title.test.ts:24')
  await expect(searchNode.locator('.trace-result-summary')).toHaveText('2 matches · 7 files scanned')
  await expect.poll(() => fixture.nodeReads).toEqual([readEvent.sequence, searchEvent.sequence])
  await expect(searchNode.locator('details.trace-details')).toHaveCount(0)
  expect(fixture.outputReads).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('hydrated-steps.png'), animations: 'disabled' })

  const scroller = page.locator('.messages')
  await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  const jump = page.getByRole('button', { name: 'Jump to latest', exact: true })
  await expect(jump).toBeVisible()
  await expect(jump).toHaveText('Jump to latest')
  const location = await jump.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight, reachable: !!hit && element.contains(hit) }
  })
  expect(location.left).toBeGreaterThanOrEqual(0)
  expect(location.right).toBeLessThanOrEqual(location.width)
  expect(location.top).toBeGreaterThanOrEqual(0)
  expect(location.bottom).toBeLessThanOrEqual(location.height)
  expect(location.reachable).toBe(true)
  const inspectedScroll = await scrollPosition(page)
  fixture.append('model_delta', { turn: 3, taskId: 'main', depth: 0, content: 'New finding while reviewing source.\n\n' })
  await expect(page.locator('.live-response')).toContainText('New finding while reviewing source.')
  expect(Math.abs(await scrollPosition(page) - inspectedScroll)).toBeLessThanOrEqual(3)
  await jump.click()
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  await expect(jump).toHaveCount(0)
  fixture.append('model_delta', { turn: 3, taskId: 'main', depth: 0, content: Array.from({ length: 12 }, (_, index) => `Following resumed ${index + 1}.\n\n`).join('') })
  await expect(page.locator('.live-response')).toContainText('Following resumed 12.')
  await expect.poll(() => distanceToLatest(page)).toBeLessThanOrEqual(3)
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('following-resumed.png'), animations: 'disabled' })
})

test('usage totals stay cumulative and details use timestamps without model-call navigation', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents()
  const runId = events[0].data.runId as string
  const results: ModelResultView[] = Array.from({ length: 24 }, (_, index) => ({
    sequence: 10000 + index, runId, turn: index + 1, taskId: 'main', depth: 0, createdAt: new Date(Date.UTC(2026, 0, 1, 12, 0, index)).toISOString(), provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls', reasoningEffort: 'high', durationMs: 1000,
    usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, reasoningTokens: 3 },
  }))
  results.push({ ...results[0], sequence: 10025, turn: 0 })
  const fixture = await mockExecution(page, mockOrigin, events)
  fixture.session.executing = false
  const assistant: Message = { id: 'usage-assistant', runId, role: 'assistant', sequence: results.at(-1)!.sequence + 1, outputTurn: 3, content: 'Synthetic source review completed.', modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: new Date().toISOString(), modelResults: results }
  fixture.messages.push(assistant)
  fixture.append('run_finished', { status: 'succeeded', message: assistant }).sequence = assistant.sequence
  await page.reload()
  const usage = page.locator('article.assistant .message-model-results')
  await expect(usage).toContainText('input 2.5K')
  await expect(usage.locator('.token-usage')).toHaveAttribute('title', /input 2,500/)
  await expect(usage).toContainText('output 500')
  await expect(usage).toContainText('TPS 20.0')
  await usage.getByRole('button', { name: 'Model usage details', exact: true }).click()
  const details = page.locator('.usage-details')
  await expect(details).toBeVisible()
  await expect(page.locator('.messages')).toHaveClass(/usage-scroll-locked/)
  const totals = (await details.locator('.usage-summary').textContent())!
  await expect(details.locator('.usage-summary-heading')).not.toContainText('Child sessions')
  await expect(details.locator('tbody tr')).toHaveCount(20)
  await expect(details.locator('thead th')).toHaveCount(7)
  await expect(details.locator('tbody button')).toHaveCount(0)
  await expect(details.locator('tbody tr').first().locator('td').first()).not.toHaveText('—')
  await details.getByRole('button', { name: 'Next', exact: true }).click()
  await expect(details.locator('tbody tr')).toHaveCount(5)
  await expect(details.locator('.usage-summary')).toHaveText(totals)
  await page.keyboard.press('Escape')
  await expect(page.locator('.messages')).not.toHaveClass(/usage-scroll-locked/)
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('usage-time-details.png'), animations: 'disabled' })
})

test('large final reply loads one full snapshot automatically and Copy includes every section', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const content = fullOutputText('Main reply')
  const assistant: Message = { id: 'full-assistant', runId, role: 'assistant', sequence: 3, outputTurn: 7,
    outputBytes: Buffer.byteLength(content), content: '# Main reply\n\nOpening preview — 中文 🙂.',
    modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T12:00:04Z' }
  events.push({ sequence: 4, kind: 'run_finished', createdAt: assistant.createdAt, data: { runId, status: 'succeeded', message: assistant } })
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (text: string) => { (window as Window & { copiedText?: string }).copiedText = text } } })
  })
  const outputPath = `/sessions/execution-session/runs/${runId}/model-output?turn=7`
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [events[0].data, assistant], outputContents: { [outputPath]: content }, outputDelayMs: 700 })
  const reply = page.locator('article.assistant')
  await expect(page.locator('.history-loading')).toBeVisible()
  await expect(reply).not.toBeVisible()
  await expect(reply.locator('.markdown').getByRole('heading', { level: 2 })).toHaveText(['Opening section', 'Middle section', 'Final section'])
  await expect(reply.locator('.markdown')).toContainText('Main reply ending — 完整输出 ✅.')
  await expect(reply.locator('.markdown')).not.toContainText('\uFFFD')
  await expect(reply.getByRole('button', { name: /Read full answer|Read output|Read next page|Read from start|Return to preview|Refresh output/ })).toHaveCount(0)
  expect(fixture.outputReads).toEqual([outputPath + '&full=1'])
  const copy = reply.getByRole('button', { name: 'Copy reply', exact: true })
  await copy.scrollIntoViewIfNeeded()
  await copy.click()
  await expect.poll(() => page.evaluate(() => (window as Window & { copiedText?: string }).copiedText)).toBe(content)
  expect(fixture.outputReads).toEqual([outputPath + '&full=1'])
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('full-main-reply.png'), animations: 'disabled' })
})

test('a completed model-only run keeps the final reply once without an empty process entry', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const content = 'Unique main final reply — 主任务最终回答只显示一次 ✅.'
  const assistant: Message = { id: 'unique-main-reply', runId, role: 'assistant', sequence: 7, outputTurn: 2,
    content, modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T12:00:07Z' }
  events.push(
    { sequence: 3, kind: 'model_request', createdAt: '2026-01-01T12:00:03Z', data: { runId, taskId: 'main', turn: 2, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' } },
    { sequence: 4, kind: 'model_delta', createdAt: '2026-01-01T12:00:04Z', data: { runId, taskId: 'main', turn: 2, depth: 0, content } },
    { sequence: 5, kind: 'model_result', createdAt: '2026-01-01T12:00:05Z', data: { runId, taskId: 'main', turn: 2, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'completed' } } },
    { sequence: 6, kind: 'agent_task', createdAt: '2026-01-01T12:00:06Z', data: { runId, taskId: 'main', task: { id: 'main', title: 'Main task', status: 'succeeded', report: { runId, turn: 2, bytes: Buffer.byteLength(content) } } } },
    { sequence: 8, kind: 'run_finished', createdAt: '2026-01-01T12:00:08Z', data: { runId, status: 'succeeded', message: assistant } },
  )
  const outputPath = `/sessions/execution-session/runs/${runId}/model-output?turn=2`
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [events[0].data, assistant], outputContents: { [outputPath]: content } })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  for (const reload of [false, true]) {
    if (reload) await page.reload()
    await expect(page.locator('article.assistant .markdown')).toHaveText(content)
    await expect(trace).toHaveCount(0)
    await expect(page.getByText(content, { exact: true })).toHaveCount(1)
  }
  expect(fixture.historyPages).toHaveLength(2)
  expect(fixture.historyPages.every(query => !new URLSearchParams(query).has('compact'))).toBe(true)
  expect(fixture.outputReads).toEqual([])
  expect(fixture.unexpected).toEqual([])
  await page.locator('article.assistant').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('main-reply-once.png'), animations: 'disabled' })
})

test('background process monitor reads bounded live ranges and leaves trace as an action record', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const initial = fullOutputText('Running job')
  const job: Job = { id: 'job-live', runId, callId: 'live-command', taskId: 'main', directory: '/synthetic', command: 'synthetic command', status: 'running', bytes: Buffer.byteLength(initial), exitCode: -1, complete: false, startedAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3600000).toISOString() }
  const tool = { callId: job.callId, name: 'start_background', summary: 'Synthetic running command' }
  events.push({ sequence: 3, kind: 'tool', createdAt: '2026-01-01T12:00:03Z', data: { runId, turn: 1, taskId: 'main', status: 'started', tool, content: '' } })
  events.push({ sequence: 4, kind: 'tool', createdAt: '2026-01-01T12:00:04Z', data: { runId, turn: 1, taskId: 'main', status: 'succeeded', tool, content: JSON.stringify({ jobId: job.id, status: 'running' }) } })
  const outputPath = `/sessions/execution-session/jobs/${job.id}/output`
  const fixture = await mockExecution(page, mockOrigin, events, { jobs: [job], outputContents: { [outputPath]: initial } })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  expect(fixture.outputReads).toEqual([])
  // The independent entry works without opening the execution trace.
  await page.getByRole('button', { name: 'Background processes 1', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Background processes', exact: true })
  const output = dialog.locator('.process-output-viewport')
  await expect(output).toContainText('Running job ending')
  await expect(dialog).toContainText('Expires')
  const updated = initial + '\nLive update ending — 新输出 🙂.\n'
  fixture.outputContents.set(outputPath, updated)
  fixture.append('job_output', { id: job.id, bytes: Buffer.byteLength(updated) })
  await expect(output).toContainText('Live update ending')
  expect(fixture.outputReads.every(path => path.includes('limit=16384') && !path.includes('full=1'))).toBe(true)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  const closedReads = fixture.outputReads.length
  await page.waitForTimeout(1300)
  expect(fixture.outputReads).toHaveLength(closedReads)
  await readFull(nodeTitle(trace, runId, 'tool/main/live-command'))
  await expect(trace.getByRole('button', { name: 'View process', exact: true })).toBeVisible()
  await expect(trace.locator('pre.trace-output')).toHaveCount(0)
  await expect(trace.getByRole('button', { name: 'Stop process' })).toHaveCount(0)
  expect(fixture.outputReads).toHaveLength(closedReads)
  await trace.getByRole('button', { name: 'View process', exact: true }).click()
  await dialog.getByRole('button', { name: 'Stop process', exact: true }).click()
  await expect(dialog.locator('.process-state')).toHaveText('Stopped')
  await expect(dialog).toContainText('SIGKILL')
  await expect(output).toContainText('Live update ending')
  await page.reload()
  await page.getByRole('button', { name: 'Background processes 0', exact: true }).click()
  await expect(output).toContainText('Live update ending')
  await expect(dialog.locator('.process-state')).toHaveText('Stopped')
  expect(fixture.unexpected).toEqual([])
  await expect.poll(async () => (await output.boundingBox())!.height).toBeGreaterThan(65)
  const bounds = await output.boundingBox()
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(page.viewportSize()!.height)
  await page.screenshot({ path: testInfo.outputPath('background-process.png'), animations: 'disabled' })
})

test('multi-megabyte process logs use virtual blocks, pause follow and abort reads when closed', async ({ page, mockOrigin }, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.addInitScript(() => localStorage.setItem('vista-appearance', 'dark'))
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const content = Array.from({ length: 40000 }, (_, i) => `Log ${i.toString().padStart(5, '0')} 中文🙂 ${'x'.repeat(60)}\n`).join('') + 'INITIAL TAIL\n'
  const job: Job = { id: 'job-large', runId, directory: '/synthetic', command: 'long running process', status: 'running', bytes: Buffer.byteLength(content), exitCode: -1, complete: false }
  const outputPath = `/sessions/execution-session/jobs/${job.id}/output`
  const fixture = await mockExecution(page, mockOrigin, events, { jobs: [job], outputContents: { [outputPath]: content } })
  await page.getByRole('button', { name: 'Background processes 1', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Background processes', exact: true })
  const output = dialog.locator('.process-output-viewport')
  await expect(output).toContainText('INITIAL TAIL')
  expect(fixture.outputReads.length).toBeLessThan(12)
  expect(await output.locator('.process-output-block').count()).toBeLessThan(8)
  await output.evaluate(element => {
    element.dispatchEvent(new WheelEvent('wheel', { deltaY: -100 }))
    element.scrollTop = 0
    element.dispatchEvent(new Event('scroll'))
  })
  await expect(output).toContainText('Log 00000')
  await expect(dialog.getByRole('button', { name: 'Follow latest', exact: true })).toBeVisible()
  const updated = content + 'NEW TAIL\n'
  fixture.outputContents.set(outputPath, updated)
  fixture.append('job_output', { id: job.id, bytes: Buffer.byteLength(updated) })
  await page.waitForTimeout(1400)
  expect(await output.evaluate(element => element.scrollTop)).toBeLessThan(5)
  await dialog.getByRole('button', { name: 'Follow latest', exact: true }).click()
  await expect(output).toContainText('NEW TAIL')
  await expect(dialog.getByText('Following latest', { exact: true })).toBeVisible()
  // Repeated navigation never mounts the entire file.
  const firstReads = fixture.outputReads.filter(path => path.includes('offset=0&')).length
  for (const fraction of Array.from({ length: 36 }, (_, index) => (index + 1) / 38)) {
    await output.evaluate((element, fraction) => {
      element.dispatchEvent(new WheelEvent('wheel', { deltaY: -100 }))
      element.scrollTop = element.scrollHeight * fraction
      element.dispatchEvent(new Event('scroll'))
    }, fraction)
    await page.waitForTimeout(80)
    expect(await output.locator('.process-output-block').count()).toBeLessThan(8)
  }
  // Returning to evicted history reloads that range, without retaining all
  // previously visited text blocks in memory.
  await output.evaluate(element => {
    element.dispatchEvent(new WheelEvent('wheel', { deltaY: -100 }))
    element.scrollTop = 0
    element.dispatchEvent(new Event('scroll'))
  })
  await expect(output).toContainText('Log 00000')
  expect(fixture.outputReads.filter(path => path.includes('offset=0&')).length).toBeGreaterThan(firstReads)
  expect(fixture.outputReads.every(path => path.includes('limit=16384') && !path.includes('full=1'))).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('long-background-output.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  const reads = fixture.outputReads.length
  await page.waitForTimeout(1400)
  expect(fixture.outputReads).toHaveLength(reads)
  expect(fixture.unexpected).toEqual([])
})

test('switching monitored processes rejects late output and wraps long single lines', async ({ page, mockOrigin }) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const long = '🙂中x'.repeat(40000) + 'FIRST PROCESS TAIL'
  const jobs: Job[] = [
    { id: 'job-first', runId, command: 'first process', directory: '/first', bytes: Buffer.byteLength(long), status: 'running', complete: false, exitCode: -1 },
    { id: 'job-second', runId, command: 'second process', directory: '/second', bytes: 14, status: 'succeeded', complete: true, exitCode: 0 },
  ]
  const firstPath = `/sessions/execution-session/jobs/${jobs[0].id}/output`
  const secondPath = `/sessions/execution-session/jobs/${jobs[1].id}/output`
  const fixture = await mockExecution(page, mockOrigin, events, { jobs, outputContents: { [firstPath]: long, [secondPath]: 'SECOND OUTPUT' } })
  const release = fixture.holdOutput(firstPath)
  await page.getByRole('button', { name: 'Background processes 1', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Background processes', exact: true })
  await expect.poll(() => fixture.outputReads.length).toBeGreaterThan(0)
  await dialog.locator('.process-select').click()
  await page.getByText('Finished · second process', { exact: true }).click()
  const output = dialog.locator('.process-output-viewport')
  await expect(output).toContainText('SECOND OUTPUT')
  release()
  await page.waitForTimeout(200)
  await expect(output).toHaveText('SECOND OUTPUT')
  await dialog.locator('.process-select').click()
  await page.getByText('Running · first process', { exact: true }).click()
  await expect(output).toContainText('FIRST PROCESS TAIL')
  expect(await output.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1)
  expect(await output.locator('.process-output-block').count()).toBeLessThan(8)
  expect(fixture.unexpected).toEqual([])
})

test('requested report loading shows Retry after failure and keeps the recovered full snapshot', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const content = fullOutputText('Recovered child report')
  events.push({ sequence: 3, kind: 'agent_task', createdAt: '2026-01-01T12:00:03Z', data: { runId, taskId: 'report-child', parentTaskId: 'main', task: { id: 'report-child', parentId: 'main', title: 'Synthetic child report', status: 'succeeded', report: { runId, turn: 99, bytes: Buffer.byteLength(content) } } } })
  const outputPath = `/sessions/execution-session/runs/${runId}/model-output?turn=99`
  const fixture = await mockExecution(page, mockOrigin, events, { outputContents: { [outputPath]: content }, outputFailures: { [outputPath]: 1 }, outputDelayMs: 700 })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  expect(fixture.outputReads).toEqual([])
  expect(fixture.outputReads).toEqual([])
  const child = trace.locator('.trace-task[data-task-id="report-child"]')
  await child.getByRole('button', { name: 'Child session result', exact: true }).click()
  await expect(child.locator('.trace-report').getByText('Child session result', { exact: true })).toBeVisible()
  await expect(child.locator('.trace-output-loading')).toBeVisible()
  await expect(child.locator('.trace-output-load-error')).toBeVisible()
  const retry = child.getByRole('button', { name: 'Retry', exact: true })
  await expect(retry).toBeVisible()
  await expect(child.getByRole('button', { name: /Read output|Read next page|Read from start|Refresh output/ })).toHaveCount(0)
  expect(fixture.outputReads).toEqual([outputPath + '&full=1'])
  await retry.click()
  await expect(child.locator('.trace-report-body')).toContainText('Recovered child report ending — 完整输出 ✅.')
  await expect(child.locator('.trace-output-load-error')).toHaveCount(0)
  await expect(retry).toHaveCount(0)
  expect(fixture.outputReads).toEqual([outputPath + '&full=1', outputPath + '&full=1'])
  await child.getByRole('button', { name: 'Child session result', exact: true }).click()
  await child.getByRole('button', { name: 'Child session result', exact: true }).click()
  await expect(child.locator('.trace-report-body')).toContainText('Recovered child report ending — 完整输出 ✅.')
  expect(fixture.outputReads).toHaveLength(2)
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('full-report-retry.png'), animations: 'disabled' })
})

test('token usage uses compact K and M values with exact hover counts in English and Chinese', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const result: ModelResultView = { sequence: 3, runId, turn: 1, taskId: 'main', depth: 0,
    provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'completed', durationMs: 1000,
    usage: { inputTokens: 7934540, outputTokens: 25485, cacheReadTokens: 6478336, reasoningTokens: 16935 } }
  const assistant: Message = { id: 'compact-usage-assistant', runId, role: 'assistant', sequence: 4, outputTurn: 1,
    content: 'Synthetic token usage fixture.', modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T12:00:04Z', modelResults: [result] }
  events.push({ sequence: 5, kind: 'run_finished', createdAt: assistant.createdAt, data: { runId, status: 'succeeded', message: assistant } })
  const fixture = await mockExecution(page, mockOrigin, events, { messages: [events[0].data, assistant] })
  const compact = ['7.9M', '25.5K', '6.5M', '16.9K']
  const exact = ['7,934,540', '25,485', '6,478,336', '16,935']
  for (const language of ['en', 'zh-CN']) {
    await page.evaluate(value => localStorage.setItem('vista-language', value), language)
    await page.reload()
    const usage = page.locator('article.assistant .message-model-results')
    const footer = usage.locator('.token-usage')
    await expect(footer).toHaveText(language === 'en'
      ? '· input 7.9M · output 25.5K · cached 6.5M · reasoning 16.9K tokens'
      : '· 输入 7.9M · 输出 25.5K · 缓存 6.5M · 推理 16.9K 词元')
    await footer.hover()
    await expect(footer).toHaveAttribute('title', language === 'en'
      ? 'input 7,934,540 · output 25,485 · cached 6,478,336 · reasoning 16,935 tokens'
      : '输入 7,934,540 · 输出 25,485 · 缓存 6,478,336 · 推理 16,935 词元')
    await usage.getByRole('button', { name: language === 'en' ? 'Model usage details' : '模型用量详情', exact: true }).click()
    const details = page.locator('.usage-details')
    await expect(details.locator('.usage-summary dd')).toHaveText(compact)
    for (let index = 0; index < exact.length; index++) {
      await expect(details.locator('.usage-summary dd').nth(index)).toHaveAttribute('title', exact[index])
      const cell = details.locator('tbody td').nth(index + 2)
      await expect(cell).toHaveText(compact[index])
      await expect(cell).toHaveAttribute('title', exact[index])
    }
    await page.waitForTimeout(350)
    await page.screenshot({ path: testInfo.outputPath(`compact-token-usage-${language}.png`), animations: 'disabled' })
  }
  expect(fixture.unexpected).toEqual([])
})


test('debug mode defaults off and settings persist both diagnostic entrances across reloads', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockExecution(page, mockOrigin, agentIdentityEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const worker = page.locator('.workspace:not(.modal-page) > .conversation-header .worker-status-trigger')
  const context = page.locator('.conversation-header .context-compaction')
  const debug = trace.locator('.trace-debug-trigger')
  const main = trace.locator('.trace-task-group[data-task-id="main"]')
  const title = nodeTitle(main, fixture.runId, 'tool/main/read-main')
  const settings = page.locator('.settings-page:visible')
  const toggle = settings.getByRole('switch', { name: 'Debug mode', exact: true })
  const openAction = async (name: string) => {
    const mobile = page.locator('.mobile-sessions-trigger:visible')
    if (await mobile.count()) await mobile.click()
    await page.locator('.sidebar-navigation:visible').getByRole('button', { name: new RegExp(name + '$') }).click()
    if (name === 'Settings') await openSettingsSection(page, 'about')
  }
  const checkLayout = async (element: Locator) => {
    const layout = await element.evaluate(element => {
      const rect = element.getBoundingClientRect()
      return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewportWidth: innerWidth, viewportHeight: innerHeight, pageWidth: document.documentElement.scrollWidth, internalOverflow: element.scrollWidth - element.clientWidth }
    })
    expect(layout.left).toBeGreaterThanOrEqual(-1)
    expect(layout.right).toBeLessThanOrEqual(layout.viewportWidth + 1)
    expect(layout.top).toBeGreaterThanOrEqual(-1)
    expect(layout.bottom).toBeLessThanOrEqual(layout.viewportHeight + 1)
    expect(layout.pageWidth).toBeLessThanOrEqual(layout.viewportWidth + 2)
    expect(layout.internalOverflow).toBeLessThanOrEqual(2)
  }
  const checkManager = async (enabled: boolean) => {
    await openAction('Manage sessions')
    const manager = page.locator('.session-manager-page:visible')
    const stats = manager.locator('.session-manager-controls')
    await expect(stats.getByRole('radio')).toHaveCount(3)
    await expect(stats.locator('.session-manager-workers')).toHaveCount(enabled ? 1 : 0)
    await stats.scrollIntoViewIfNeeded()
    await checkLayout(stats)
    await manager.getByRole('button', { name: /^(?:Close session manager|Back to conversation)$/, exact: true }).click()
    await expect(manager).toHaveCount(0)
  }
  const checkEntrances = async (enabled: boolean) => {
    await expect(debug).toHaveCount(enabled ? 1 : 0)
    await expect(worker).toHaveCount(enabled ? 1 : 0)
    await expect(context).toBeVisible()
    await checkLayout(page.locator('.workspace:not(.modal-page) > .conversation-header .conversation-header-tools'))
  }
  await expect(trace).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('vista-debug-mode'))).toBeNull()
  await checkEntrances(false)
  await main.locator('button.ui-thought-chain-node-header').click()
  await expect(main.locator('pre.trace-output')).toHaveText('Independent result from main.')
  await checkManager(false)
  await openAction('Settings')
  await expect(page).toHaveURL(/\/settings\/about(?:\?.*)?$/)
  await expect(toggle).not.toBeChecked()
  await toggle.scrollIntoViewIfNeeded()
  await checkLayout(toggle)
  await toggle.click()
  await expect(toggle).toBeChecked()
  expect(await page.evaluate(() => localStorage.getItem('vista-debug-mode'))).toBe('true')
  await page.screenshot({ path: testInfo.outputPath('debug-mode-settings-enabled.png'), animations: 'disabled' })
  await closeSettings(page)
  await checkEntrances(true)
  if (testInfo.project.name !== 'desktop') await main.locator('button.ui-thought-chain-node-header').click()
  await expect(main.locator('pre.trace-output'), 'Debug preferences keep normal execution output expanded').toHaveText('Independent result from main.')
  await checkManager(true)
  await page.reload()
  await expect(trace).toBeVisible()
  await checkEntrances(true)
  await debug.click()
  const diagnostics = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await expect(diagnostics).toContainText('read-main')
  await page.keyboard.press('Escape')
  await expect(diagnostics).not.toBeVisible()
  await worker.click()
  await expect(page.locator('.worker-metrics-detail:visible')).toContainText('Worker heap')
  await page.keyboard.press('Escape')
  await expect(page.locator('.worker-metrics-detail:visible')).toHaveCount(0)
  await expect(title).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('debug-mode-session-enabled.png'), animations: 'disabled' })
  await openAction('Settings')
  await expect(toggle).toBeChecked()
  await toggle.click()
  await expect(toggle).not.toBeChecked()
  expect(await page.evaluate(() => localStorage.getItem('vista-debug-mode'))).toBe('false')
  await closeSettings(page)
  await checkEntrances(false)
  await expect(title).toBeVisible()
  await checkManager(false)
  await page.reload()
  await expect(trace).toBeVisible()
  await checkEntrances(false)
  await expect(title).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('debug-mode-session-disabled.png'), animations: 'disabled' })
  await openAction('Settings')
  await expect(toggle).not.toBeChecked()
  await toggle.scrollIntoViewIfNeeded()
  await checkLayout(toggle)
  expect(fixture.unexpected).toEqual([])
})

sourceTest('a failed execution-details render leaves the formal answer visible', async ({ page, mockOrigin }) => {
  await page.route('**/src/RunTrace.vue', route => route.fulfill({ contentType: 'text/javascript', body: 'export default { setup() { throw new Error("Injected execution rendering failure") } }' }))
  const source = hydrationEvents()
  const runId = source[0].data.runId
  const answer: Message = { id: 'formal-answer', sequence: source.at(-1)!.sequence + 1, runId, role: 'assistant', content: 'The saved formal answer remains readable.', createdAt: '' }
  source.push({ sequence: answer.sequence, kind: 'run_finished', data: { runId, status: 'succeeded', message: answer } })
  await mockExecution(page, mockOrigin, source, { messages: [source[0].data, answer] })
  await expect(page.locator('.conversation-render-notice').first()).toContainText('Execution details could not be displayed')
  await expect(page.locator('.messages article.assistant:not(.main-output) .markdown')).toHaveText(answer.content)
})

sourceTest('a failed Markdown renderer displays escaped plain text and retains copying', async ({ page, mockOrigin }) => {
  await page.route('**/src/ui/markdown.ts', route => route.fulfill({ contentType: 'text/javascript', body: 'export function renderMarkdown() { throw new Error("Injected Markdown rendering failure") }' }))
  const source = hydrationEvents()
  const answer: Message = { id: 'plain-answer', sequence: source.at(-1)!.sequence + 1, runId: source[0].data.runId, role: 'assistant', content: 'Saved <script>unsafe()</script> **answer**.', createdAt: '' }
  source.push({ sequence: answer.sequence, kind: 'run_finished', data: { runId: answer.runId, status: 'succeeded', message: answer } })
  await mockExecution(page, mockOrigin, source, { messages: [source[0].data, answer] })
  const reply = page.locator('.messages article.assistant:not(.main-output)')
  await expect(reply.locator('.message-plain-text')).toHaveText(answer.content)
  await expect(reply.locator('.markdown script')).toHaveCount(0)
  await expect(reply.getByRole('button', { name: 'Copy reply', exact: true })).toBeEnabled()
})
