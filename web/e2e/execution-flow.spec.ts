import { closeSettings, openSettingsSection } from './settings-navigation'
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { createServer } from 'vite'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { Artifact, ChecklistItem, Job, Message, ModelResultView, RunOverview, Session, StreamEvent } from '../src/client/types'

// Chromium's Unix socket needs a short profile directory, still inside this checkout.
const browserRuntime = resolve(import.meta.dirname, '../../.vista/b')
mkdirSync(browserRuntime, { recursive: true })
process.env.TMPDIR = browserRuntime

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({ root: resolve(import.meta.dirname, '..'), configFile: resolve(import.meta.dirname, '../vite.config.ts'), server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } } })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close() }
  }, { scope: 'worker' }],
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

type MockOptions = { messages?: Message[]; jobs?: Job[]; outputContents?: Record<string, string>; outputFailures?: Record<string, number>; outputDelayMs?: number }
async function mockExecution(page: Page, origin: string, source = replayEvents(), options: MockOptions = {}) {
  const events = source
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
  const eventPages: string[] = []
  const outputReads: string[] = []
  const mainOutputReads: string[] = []
  const nodeReads: number[] = []
  const turnReads: string[] = []
  const overviewReads: string[] = []
  const unexpected: string[] = []
  let sseRequests = 0
  const append = (kind: string, data: object) => {
    const event = { sequence: events.at(-1)!.sequence + 1, kind, createdAt: new Date().toISOString(), data: { runId, ...data } }
    events.push(event)
    if (kind === 'job' || kind === 'artifact') {
      const target = kind === 'job' ? jobs : artifacts
      const item = event.data as Job | Artifact
      const index = target.findIndex(value => value.id === item.id)
      if (index < 0) target.push(item as Job & Artifact)
      else target[index] = item as Job & Artifact
    }
    return event
  }
  await page.route('**/api/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.slice(4)
    const reply = (json: unknown) => route.fulfill({ json })
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
    if (path === '/model-catalog') return reply({ revision: 'a'.repeat(40), modelCount: 1, providerCount: 1 })
    if (path === '/models') return reply({ default: 'replay-model', models: [{ key: 'replay-model', name: 'Replay', provider: 'openai-chat', id: 'gpt-6-astra', tools: true, reasoningOptions: ['high'] }] })
    if (path === '/status') return reply({ state: 'running', sessions: { total: 1, open: 1, closed: 0, running: session.executing ? 1 : 0, workers: 1 }, workerMetrics: [{ sessionId: session.id, pid: 1, memory: { heapBytes: 1000000, rssBytes: 4000000, sampledAt: session.createdAt } }] })
    if (path === '/sessions') return reply([session])
    if (path === '/model-selection/stats') return reply({ speedScope: 'global', qualityScope: 'user', speedWindowDays: 30, qualityWindowSize: 200, models: [] })
    if (path.endsWith('/messages')) return reply(messages)
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
      overviewReads.push(path)
      const requestedRun = path.split('/runs/')[1]?.split('/')[0]
      const after = Number(url.searchParams.get('after'))
      const through = Number(url.searchParams.get('through'))
      return reply(overviewOf(events.filter(event => event.data.runId === requestedRun && event.sequence > after && (!through || event.sequence <= through))))
    }
    if (path.endsWith('/main-outputs')) {
      const outputs = new Map<string, { runId: string; taskId: string; turn: number; sequence: number; lastSequence: number; createdAt: string; bytes: number; complete: boolean }>()
      for (const event of events) {
        if (event.kind !== 'model_delta' || event.data.depth || event.data.taskId && event.data.taskId !== 'main' || !event.data.content) continue
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
      eventPages.push(url.search)
      const before = Number(url.searchParams.get('before'))
      const after = Number(url.searchParams.get('after'))
      const page = before ? events.filter(event => event.sequence < before).slice(-100) : after ? events.filter(event => event.sequence > after).slice(0, 100) : events.slice(-100)
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
      const after = Number(url.searchParams.get('after'))
      const body = events.filter(event => event.sequence > after).map(event => `id: ${event.sequence}\nevent: record\ndata: ${JSON.stringify(event)}\n\n`).join('')
      return route.fulfill({ contentType: 'text/event-stream', body })
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
  return { events, runId, session, messages, artifacts, jobs, outputContents, outputFailures, holdOutput, eventPages, outputReads, mainOutputReads, nodeReads, turnReads, overviewReads, unexpected, append, get sseRequests() { return sseRequests } }
}

function taskHeader(task: Locator) { return task.locator(':scope > .antd-think > .antd-think-status-wrapper') }
function processHeader(trace: Locator) { return trace.locator(':scope > .antd-think > .antd-think-status-wrapper') }
async function scrollPosition(page: Page) { return page.locator('.messages').evaluate(element => element.scrollTop) }
async function distanceToLatest(page: Page) { return page.locator('.messages').evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight) }
function nodeTitle(trace: Locator, runId: string, key: string) { return trace.locator(`[id="trace-${runId}-0-${encodeURIComponent(key)}"]`) }
// Move only the conversation scrollbar. Missing virtual rows must become reachable
// by scrolling the visible history, without calling component methods or controls.
async function scrollToTraceStep(page: Page, task: Locator, runId: string, key: string, from: 'start' | 'end' = 'start') {
  const steps = task.locator('.trace-steps')
  const titleId = `trace-${runId}-0-${encodeURIComponent(key)}`
  const result = await steps.evaluate(async (element, options) => {
    const scroller = element.closest<HTMLElement>('.messages')!
    const viewport = scroller.getBoundingClientRect()
    const bounds = element.getBoundingClientRect()
    scroller.scrollTop += options.from === 'start' ? bounds.top - viewport.top : bounds.bottom - viewport.bottom
    scroller.dispatchEvent(new Event('scroll'))
    let maxMounted = 0
    for (let attempt = 0; attempt < 400; attempt++) {
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      maxMounted = Math.max(maxMounted, element.querySelectorAll('.antd-thought-chain-node').length)
      if (element.querySelector(`[id="${options.titleId}"]`)) return { found: true, maxMounted, connected: element.isConnected, scrollTop: scroller.scrollTop }
      const previous = scroller.scrollTop
      scroller.scrollTop += (options.from === 'start' ? 1 : -1) * Math.max(60, scroller.clientHeight * .7)
      scroller.dispatchEvent(new Event('scroll'))
      if (Math.abs(scroller.scrollTop - previous) <= 1) break
    }
    return { found: false, maxMounted, connected: element.isConnected, scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight, viewportHeight: scroller.clientHeight, bounds: { top: bounds.top, bottom: bounds.bottom, height: bounds.height }, mounted: Array.from(element.querySelectorAll('.trace-step-row')).map(row => ({ index: row.getAttribute('data-index'), key: row.getAttribute('data-step-key') })) }
  }, { titleId, from })
  expect(result.maxMounted, 'Long session history keeps mounted step nodes bounded while scrolling').toBeLessThanOrEqual(40)
  if (!result.found) await test.info().attach('virtual-scroll-metrics', { body: JSON.stringify({ key, from, ...result }, null, 2), contentType: 'application/json' })
  expect(result.found, `Step ${key} must be reachable through the conversation scrollbar`).toBe(true)
  const title = nodeTitle(task, runId, key)
  await title.scrollIntoViewIfNeeded()
  await expect(title).toBeVisible()
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
  const overflow = await page.locator('.run-overview').evaluate(element => {
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

for (const longSteps of [false, true]) test(`expanding ${longSteps ? 'virtualized' : 'short'} execution details preserves a scrolled conversation`, async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const events = longSteps ? deterministicEvents(true) : independentWindowEvents()
  const runId = events[0].data.runId as string
  const messages: Message[] = [
    ...Array.from({ length: 12 }, (_, index): Message => ({
      id: `earlier-${index}`, runId: `earlier-run-${index}`, role: 'assistant', sequence: index - 20,
      content: `Earlier conversation reply ${index + 1}.\n\nThis reply puts the execution details below the start of the conversation.`,
      modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T11:00:00Z',
    })),
    events[0].data,
    ...Array.from({ length: 5 }, (_, index): Message => ({
      id: `later-${index}`, runId: `later-run-${index}`, role: 'assistant', sequence: 10000 + index,
      content: `Later conversation reply ${index + 1}.\n\nThere is more conversation below the inspected execution.`,
      modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T14:00:00Z',
    })),
  ]
  const fixture = await mockExecution(page, mockOrigin, events, { messages })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const scroller = page.locator('.messages')
  const positions: object[] = []
  async function clickWithoutJump(title: Locator, label: string) {
    await scroller.dispatchEvent('wheel', { deltaY: -100 })
    await title.scrollIntoViewIfNeeded()
    await title.evaluate(element => {
      const scroll = element.closest<HTMLElement>('.messages')!
      scroll.scrollTop += element.getBoundingClientRect().top - scroll.getBoundingClientRect().top - scroll.clientHeight * .3
      scroll.dispatchEvent(new Event('scroll'))
    })
    await page.waitForTimeout(350)
    const measure = () => title.evaluate(element => {
      const scroll = element.closest<HTMLElement>('.messages')!
      const viewport = scroll.getBoundingClientRect()
      const title = element.getBoundingClientRect()
      return { scrollTop: scroll.scrollTop, titleTop: title.top, titleBottom: title.bottom, viewportTop: viewport.top, viewportBottom: viewport.bottom }
    })
    const before = await measure()
    expect(before.scrollTop, `${label} is clicked after scrolling away from the conversation top`).toBeGreaterThan(300)
    expect(before.titleTop).toBeGreaterThanOrEqual(before.viewportTop)
    expect(before.titleBottom).toBeLessThanOrEqual(before.viewportBottom)
    await page.screenshot({ path: testInfo.outputPath(`${label}-before.png`), animations: 'disabled' })
    // In short landscape windows the floating jump button can cover the
    // header's center. Use its uncovered leading edge so Playwright's automatic
    // scrolling around that overlay is not mistaken for expansion movement.
    await title.click({ position: { x: 8, y: 8 } })
    await page.waitForTimeout(450)
    const after = await measure()
    positions.push({ label, before, after })
    writeFileSync(testInfo.outputPath('expansion-scroll-positions.json'), JSON.stringify(positions, null, 2))
    await page.screenshot({ path: testInfo.outputPath(`${label}-after.png`), animations: 'disabled' })
    expect.soft(Math.abs(after.scrollTop - before.scrollTop), `${label} preserves the conversation scroll offset`).toBeLessThanOrEqual(3)
    expect.soft(Math.abs(after.titleTop - before.titleTop), `${label} preserves the clicked title's viewport position`).toBeLessThanOrEqual(3)
  }
  await clickWithoutJump(processHeader(trace).getByRole('status'), 'execution-summary')
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  const childId = longSteps ? 'task-1' : 'child-a'
  const child = trace.locator(`.trace-task-group[data-task-id="${childId}"]`)
  await clickWithoutJump(taskHeader(child), 'child-session')
  const stepKey = longSteps ? 'tool/task-1/call-40' : 'tool/child-a/child-a-3'
  const title = longSteps ? await scrollToTraceStep(page, child, runId, stepKey) : nodeTitle(child, runId, stepKey)
  await clickWithoutJump(title, 'execution-step')
  await expect(title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]').locator('pre.trace-output')).toBeVisible()
  expect(fixture.unexpected).toEqual([])
  await testInfo.attach('expansion-scroll-positions', { body: JSON.stringify(positions, null, 2), contentType: 'application/json' })
})

// Playwright hides scrollbars by default. Give only this regression a browser
// with native scrollbars; worker launch options cannot be changed by describe.use.
const nativeScrollbarTest = test.extend({ launchOptions: {
  executablePath: process.env.VISTA_CHROME,
  ignoreDefaultArgs: ['--hide-scrollbars'],
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking'],
} })
nativeScrollbarTest.describe('native conversation scrollbar', () => {
  for (const longMessages of [false, true]) nativeScrollbarTest(`dragging the native thumb keeps expanded child history stable and reaches both ends${longMessages ? ' with virtualized messages' : ''}`, async ({ page, mockOrigin }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop', 'Native desktop scrollbar interaction')
    test.setTimeout(longMessages ? 150_000 : 90_000)
    const events = deterministicEvents(true)
    const messages: Message[] | undefined = longMessages ? [events[0].data, ...Array.from({ length: 110 }, (_, index): Message => ({
      id: `scrollbar-later-${index}`, runId: `scrollbar-later-run-${index}`, role: 'assistant', sequence: 10000 + index,
      content: `Later conversation reply ${index + 1}.`, modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T14:00:00Z',
    }))] : undefined
    const fixture = await mockExecution(page, mockOrigin, events, { messages })
    const scroller = page.locator('.messages')
    if (longMessages) {
      await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
      await scroller.dispatchEvent('wheel', { deltaY: -100 })
      await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
    }
    const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
    await processHeader(trace).getByRole('status').click()
    await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
    for (let index = 1; index <= 5; index++) {
      const child = trace.locator(`.trace-task-group[data-task-id="task-${index}"]`)
      await taskHeader(child).click()
      await expect(child.locator('.trace-steps')).toHaveCount(1)
    }
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
      const before = await measure()
      expect(before.gutter, 'The browser must expose a real native scrollbar').toBeGreaterThan(0)
      const trackHeight = before.viewport - 2 * before.gutter
      const thumbHeight = Math.max(before.gutter, trackHeight * before.viewport / before.scrollHeight)
      const x = before.right - before.gutter / 2
      const startY = before.top + before.gutter + thumbHeight / 2 + (trackHeight - thumbHeight) * before.progress
      const endY = progress === 1 ? before.bottom - before.gutter - 3 : progress === 0 ? before.top + before.gutter + 3
        : before.top + before.gutter + thumbHeight / 2 + (trackHeight - thumbHeight) * progress
      await page.mouse.move(x, startY)
      await page.mouse.down()
      const samples = [await measure()]
      for (let step = 1; step <= 24; step++) {
        await page.mouse.move(x, startY + (endY - startY) * step / 24)
        await page.waitForTimeout(20)
        samples.push(await measure())
      }
      await page.waitForTimeout(180)
      const held = await measure()
      samples.push(held)
      const anchor = progress === .5 ? await scroller.evaluate(element => {
        const viewport = element.getBoundingClientRect()
        const candidates = [...element.querySelectorAll<HTMLElement>('.trace-step-row [id], .trace-report p')]
        const visible = candidates.find(node => {
          const bounds = node.getBoundingClientRect()
          return bounds.top >= viewport.top + 10 && bounds.bottom <= viewport.bottom - 10 && bounds.height > 0
        })
        return visible ? {
          selector: visible.id ? `[id="${CSS.escape(visible.id)}"]` : '.trace-report p',
          index: visible.id ? 0 : [...element.querySelectorAll('.trace-report p')].indexOf(visible),
          top: visible.getBoundingClientRect().top,
        } : null
      }) : null
      await page.screenshot({ path: testInfo.outputPath(`${label}-held.png`) })
      await page.mouse.up()
      await page.waitForTimeout(400)
      const released = await measure()
      const releasedAnchorTop = anchor ? await scroller.locator(anchor.selector).nth(anchor.index).evaluate(element => element.getBoundingClientRect().top) : null
      records.push({ label, before, samples, held, released, anchor, releasedAnchorTop })
      writeFileSync(testInfo.outputPath('native-scrollbar-positions.json'), JSON.stringify(records, null, 2))
      await page.screenshot({ path: testInfo.outputPath(`${label}-released.png`) })
      const heights = samples.map(sample => sample.scrollHeight)
      expect(Math.max(...heights) - Math.min(...heights), `${label}: row measurements cannot change the native track height while held`).toBeLessThanOrEqual(1)
      if (progress === .5) {
        expect(anchor, 'The middle viewport contains a readable step or report paragraph').not.toBeNull()
        expect(Math.abs(releasedAnchorTop! - anchor!.top), 'Releasing in the middle keeps the visible content at its screen position').toBeLessThanOrEqual(3)
      }
      for (const [phase, position] of [['held', held], ['released', released]] as const) {
        if (progress === 1) expect(position.distanceToBottom, `${label}: ${phase} at the track bottom`).toBeLessThanOrEqual(3)
        else if (progress === 0) expect(position.scrollTop, `${label}: ${phase} at the track top`).toBeLessThanOrEqual(3)
        else expect(Math.abs(position.progress - progress), `${label}: ${phase} keeps the requested middle position`).toBeLessThanOrEqual(.03)
      }
    }
    await dragTo(1, 'bottom')
    if (longMessages) {
      await expect(trace, 'Dragging to later messages unmounts the outer execution row').toHaveCount(0)
      await expect(page.getByText('Later conversation reply 110.', { exact: true })).toBeVisible()
    }
    await dragTo(.5, 'middle')
    await dragTo(0, 'top')
    const beforeWheel = await measure()
    await page.mouse.move((beforeWheel.left + beforeWheel.right) / 2, (beforeWheel.top + beforeWheel.bottom) / 2)
    await page.mouse.wheel(0, 700)
    await expect.poll(() => scrollPosition(page), 'Wheel scrolling continues after releasing the thumb').toBeGreaterThan(beforeWheel.scrollTop + 100)
    expect(fixture.unexpected).toEqual([])
    await testInfo.attach('native-scrollbar-positions', { body: JSON.stringify(records, null, 2), contentType: 'application/json' })
  })
})

test('complete session steps and manual expansion remain independent during live delegated work', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockExecution(page, mockOrigin, independentWindowEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const taskIds = ['main', 'child-a', 'child-b', 'grandchild-a']
  const group = (taskId: string) => trace.locator(`.trace-task-group[data-task-id="${taskId}"]`)
  const title = (taskId: string, step: number) => nodeTitle(group(taskId), fixture.runId, `tool/${taskId}/${taskId}-${step}`)
  const body = (taskId: string, step: number) => title(taskId, step).locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]').locator('pre.trace-output')
  const titleIds = (taskId: string) => group(taskId).locator('.antd-thought-chain-node-title [id]').evaluateAll(elements => elements.map(element => element.id))
  await processHeader(trace).getByRole('status').click()
  for (const taskId of taskIds) {
    if (taskId !== 'main') await taskHeader(group(taskId)).click()
    await expect(group(taskId).locator('.antd-thought-chain-node')).toHaveCount(6)
    await title(taskId, 1).click()
    await expect(body(taskId, 1)).toHaveText(`Body from ${taskId} step 1.`)
  }
  // All 24 steps remain available without a manual history action.
  await expect(trace.getByRole('button', { name: 'Load earlier steps', exact: true })).toHaveCount(0)
  // A manually closed main step and unopened main steps must survive another
  // session's traffic too; testing only open nodes would miss the defect.
  await title('main', 2).click()
  await expect(body('main', 2)).toHaveText('Body from main step 2.')
  await title('main', 2).click()
  await expect(body('main', 2)).toHaveCount(0)
  await title('child-b', 2).click()
  await expect(body('child-b', 2)).toHaveText('Body from child-b step 2.')
  await title('child-b', 2).click()
  await expect(body('child-b', 2)).toHaveCount(0)
  const original = new Map(await Promise.all(taskIds.map(async taskId => [taskId, await titleIds(taskId)] as const)))
  const pagesBeforeUpdates = fixture.eventPages.length
  const appendSteps = (taskId: string, turn: number) => {
    const parentTaskId = taskId === 'main' ? undefined : 'main'
    const depth = taskId === 'main' ? 0 : 1
    fixture.append('model_request', { taskId, parentTaskId, depth, turn, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra' })
    for (let step = 7; step <= 31; step++) {
      const tool = { callId: `${taskId}-${step}`, name: 'read_file', summary: `src/${taskId}/step-${step}.ts` }
      fixture.append('tool', { taskId, parentTaskId, depth, turn, status: 'started', tool, content: '' })
      fixture.append('tool', { taskId, parentTaskId, depth, turn, status: 'succeeded', tool, content: JSON.stringify({ content: `Body from ${taskId} step ${step}.` }) })
    }
    fixture.append('model_result', { taskId, parentTaskId, depth, turn, modelResult: { provider: 'openai-chat', id: 'gpt-6-astra', finishReason: 'tool_calls' } })
  }
  appendSteps('child-a', 5)
  await expect(title('child-a', 31)).toHaveCount(1)
  for (const taskId of ['main', 'child-b', 'grandchild-a']) {
    await expect.poll(() => titleIds(taskId), `${taskId} keeps its complete steps`).toEqual(original.get(taskId))
    await expect(body(taskId, 1)).toHaveText(`Body from ${taskId} step 1.`)
  }
  await expect(body('main', 2)).toHaveCount(0)
  await expect(body('child-b', 2)).toHaveCount(0)
  await expect(body('child-a', 1)).toHaveText('Body from child-a step 1.')
  await expect(group('child-a').locator('.antd-thought-chain-node')).toHaveCount(31)
  await expect(title('child-a', 2)).toHaveCount(1)

  const childWindows = new Map(await Promise.all(['child-a', 'child-b', 'grandchild-a'].map(async taskId => [taskId, await titleIds(taskId)] as const)))
  appendSteps('main', 6)
  await expect(title('main', 31)).toHaveCount(1)
  for (const taskId of ['child-a', 'child-b', 'grandchild-a']) {
    await expect.poll(() => titleIds(taskId), `${taskId} is unaffected by the main session's continuation`).toEqual(childWindows.get(taskId))
    await expect(body(taskId, 1)).toHaveText(`Body from ${taskId} step 1.`)
  }
  await expect(body('main', 1)).toHaveText('Body from main step 1.')
  await expect(body('child-b', 2)).toHaveCount(0)
  expect(fixture.eventPages.length, 'Live work must not trigger automatic older-page loading').toBe(pagesBeforeUpdates)

  await expect(trace.getByRole('button', { name: 'Load earlier steps', exact: true })).toHaveCount(0)
  for (const taskId of ['main', 'child-a']) {
    await expect(group(taskId).locator('.antd-thought-chain-node')).toHaveCount(31)
    await expect(title(taskId, 2)).toHaveCount(1)
    await expect(title(taskId, 4)).toHaveCount(1)
    await expect(body(taskId, 1)).toHaveText(`Body from ${taskId} step 1.`)
  }
  await expect(body('main', 2)).toHaveCount(0)
  await expect(body('child-b', 2)).toHaveCount(0)
  for (const taskId of ['child-b', 'grandchild-a']) await expect.poll(() => titleIds(taskId)).toEqual(original.get(taskId))
  expect(fixture.eventPages.length, 'Revealing already-known earlier steps must not fetch history').toBe(pagesBeforeUpdates)
  expect(fixture.outputReads).toEqual([])
  expect(fixture.unexpected).toEqual([])
  await title('child-b', 1).scrollIntoViewIfNeeded()
  const readingSibling = { top: (await title('child-b', 1).boundingBox())!.y, scrollTop: await scrollPosition(page) }
  const parentContinuation = { callId: 'main-32', name: 'read_file', summary: 'src/main/step-32.ts' }
  fixture.append('tool', { taskId: 'main', depth: 0, turn: 6, status: 'succeeded', tool: parentContinuation, content: JSON.stringify({ content: 'Body from main step 32.' }) })
  await expect(title('main', 32)).toHaveCount(1)
  await expect(body('child-b', 1)).toHaveText('Body from child-b step 1.')
  const afterSibling = { top: (await title('child-b', 1).boundingBox())!.y, scrollTop: await scrollPosition(page) }
  writeFileSync(testInfo.outputPath('sibling-reading-position.json'), JSON.stringify({ before: readingSibling, after: afterSibling }, null, 2))
  expect(Math.abs(afterSibling.top - readingSibling.top), 'Parent activity keeps the sibling session step in the same screen position').toBeLessThanOrEqual(3)
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('independent-agent-step-windows.png'), animations: 'disabled' })
})

test('long complete execution steps scroll naturally with bounded DOM and preserve expanded output', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockExecution(page, mockOrigin, deterministicEvents(true))
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await processHeader(trace).getByRole('status').click()
  await expect.poll(() => fixture.eventPages.length).toBeGreaterThan(1)
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  expect(fixture.eventPages.length).toBe(1 + Math.ceil(fixture.events.length / 100))
  await expect(trace.getByRole('button', { name: 'Load earlier steps', exact: true })).toHaveCount(0)
  expect(fixture.outputReads, 'A full step summary history must not eagerly load detailed outputs').toEqual([])
  const mainTask = trace.locator('.trace-task[data-task-id="main"]')
  const mainSteps = mainTask.locator('.trace-steps')
  await expect(mainSteps).toHaveAttribute('data-virtualized', 'true')
  expect(Number(await mainSteps.getAttribute('data-step-count'))).toBeGreaterThanOrEqual(200)
  const mainFirst = await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-20')
  await expect(mainFirst).toContainText('Inspect source implementation')
  const mainMiddle = await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-310')
  const middleIndex = Number(await mainMiddle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," trace-step-row ")][1]').getAttribute('data-index'))
  expect(middleIndex).toBeGreaterThan(100)
  await mainMiddle.click()
  const middleNode = mainMiddle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  const expectedMiddle = fullOutputText('Tool output virtual-middle-output')
  await expect(middleNode.locator('.tool-output-entry pre')).toHaveText(expectedMiddle)
  expect((await middleNode.boundingBox())!.height, 'Expanded output contributes its full dynamic row height').toBeGreaterThan(1500)
  const middleOutputPath = '/sessions/execution-session/artifacts/virtual-middle-output/output?full=1'
  expect(fixture.outputReads.filter(path => path === middleOutputPath)).toHaveLength(1)
  const middlePosition = (await mainMiddle.boundingBox())!.y
  const countBeforeLiveStep = Number(await mainSteps.getAttribute('data-step-count'))
  const liveTool = { callId: 'call-live', name: 'read_file', summary: 'src/live-step.ts' }
  fixture.append('tool', { taskId: 'main', turn: 197, status: 'started', tool: liveTool, content: '' })
  fixture.append('tool', { taskId: 'main', turn: 197, status: 'succeeded', tool: liveTool, content: JSON.stringify({ content: 'New completed step while reviewing earlier output.' }) })
  await expect(mainSteps).toHaveAttribute('data-step-count', String(countBeforeLiveStep + 1))
  await expect(middleNode.locator('.tool-output-entry pre')).toHaveText(expectedMiddle)
  expect(Math.abs((await mainMiddle.boundingBox())!.y - middlePosition), 'New step activity preserves the virtual row being read').toBeLessThanOrEqual(3)
  await page.screenshot({ path: testInfo.outputPath('virtual-middle-full-output.png'), animations: 'disabled' })
  const mainLast = await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-410', 'end')
  await expect(mainLast).toContainText('Inspect source implementation')
  await expect(mainMiddle).toHaveCount(0)
  await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-310', 'end')
  await expect(middleNode.locator('.tool-output-entry pre')).toHaveText(expectedMiddle)
  expect(fixture.outputReads.filter(path => path === middleOutputPath), 'Remounting an expanded row reuses its full output snapshot').toHaveLength(1)
  await mainMiddle.click()
  await expect(middleNode.locator('.tool-output-entry pre')).toHaveCount(0)
  await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-410', 'end')
  await expect(mainFirst, 'The early failed step is virtualized away while reading later history').toHaveCount(0)
  await expect(mainSteps).toHaveAttribute('data-virtualized', 'true')
  await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-310')
  await expect(middleNode.locator('.tool-output-entry pre'), 'A manually closed row remains closed after scrolling away and back').toHaveCount(0)
  await scrollToTraceStep(page, mainTask, fixture.runId, 'tool/main/call-410', 'end')
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('virtual-scrolled-details.png'), animations: 'disabled' })
  expect(fixture.unexpected).toEqual([])
})

test('crossing the step virtualization threshold preserves the reading position and open output', async ({ page, mockOrigin }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop', 'The desktop case covers the shared measurement threshold')
  test.setTimeout(45_000)
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const firstOutput = fullOutputText('Threshold first step')
  for (let step = 1; step <= 100; step++) {
    const tool = { callId: `threshold-${step}`, name: 'read_file', summary: `src/threshold-${step}.ts` }
    events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'tool', createdAt: '2026-01-01T12:00:03Z', data: { runId, taskId: 'main', turn: 1, status: 'succeeded', tool, content: JSON.stringify({ content: step === 1 ? firstOutput : `Threshold step ${step}.` }) } })
  }
  // Keep the long first output out of the initial 100-event live page.
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'model_delta', createdAt: '2026-01-01T12:00:04Z', data: { runId, taskId: 'main', turn: 1, content: 'Working on the synthetic threshold task.' } })
  const fixture = await mockExecution(page, mockOrigin, events)
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const mainTask = trace.locator('.trace-task[data-task-id="main"]')
  const steps = mainTask.locator('.trace-steps')
  await processHeader(trace).getByRole('status').click()
  await expect(steps).toHaveAttribute('data-step-count', '100')
  await expect(steps).toHaveAttribute('data-virtualized', 'false')
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  const first = nodeTitle(mainTask, runId, 'tool/main/threshold-1')
  await first.click()
  const firstNode = first.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  await expect(firstNode.locator('pre.trace-output')).toHaveText(firstOutput)
  expect((await firstNode.boundingBox())!.height).toBeGreaterThan(1500)
  const anchor = nodeTitle(mainTask, runId, 'tool/main/threshold-70')
  await anchor.scrollIntoViewIfNeeded()
  const naturalRows = await steps.evaluate(element => {
    const rows = Array.from(element.querySelectorAll<HTMLElement>('.trace-step-row'))
    return { rootHeight: element.getBoundingClientRect().height, sumBounds: rows.reduce((sum, row) => sum + row.getBoundingClientRect().height, 0), sumOffsets: rows.reduce((sum, row) => sum + row.offsetHeight, 0), heights: rows.map(row => ({ index: row.getAttribute('data-index'), bounds: row.getBoundingClientRect().height, offset: row.offsetHeight })) }
  })
  const before = { top: (await anchor.boundingBox())!.y, scrollTop: await scrollPosition(page), naturalRows }
  await testInfo.attach('threshold-natural-layout', { body: JSON.stringify(before, null, 2), contentType: 'application/json' })
  const newTool = { callId: 'threshold-101', name: 'read_file', summary: 'src/threshold-101.ts' }
  fixture.append('tool', { taskId: 'main', turn: 1, status: 'succeeded', tool: newTool, content: JSON.stringify({ content: 'Threshold step 101.' }) })
  await expect(steps).toHaveAttribute('data-step-count', '101')
  await expect(steps).toHaveAttribute('data-virtualized', 'true')
  const thresholdTimeline = await steps.evaluate(async (element, titleId) => {
    const scroller = element.closest<HTMLElement>('.messages')!
    const samples: unknown[] = []
    for (let frame = 0; frame < 12; frame++) {
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      const title = element.querySelector(`[id="${titleId}"]`)
      samples.push({ titleTop: title?.getBoundingClientRect().top, scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight, viewportHeight: scroller.clientHeight, windowY: scrollY, rootTop: element.getBoundingClientRect().top, rootHeight: element.getBoundingClientRect().height, cssHeight: getComputedStyle(element).height, margin: element.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop, mounted: Array.from(element.querySelectorAll('.trace-step-row')).map(row => ({ index: row.getAttribute('data-index'), key: row.getAttribute('data-step-key'), top: row.getBoundingClientRect().top, height: row.getBoundingClientRect().height })) })
    }
    return samples
  }, `trace-${runId}-0-${encodeURIComponent('tool/main/threshold-70')}`)
  writeFileSync(testInfo.outputPath('threshold-layout.json'), JSON.stringify({ before, samples: thresholdTimeline }, null, 2))
  await testInfo.attach('threshold-reading-timeline', { body: JSON.stringify({ before, samples: thresholdTimeline }, null, 2), contentType: 'application/json' })
  await expect(anchor).toBeVisible()
  expect(Math.abs((await anchor.boundingBox())!.y - before.top), 'Crossing 100 steps does not shift the row being read').toBeLessThanOrEqual(3)
  expect(Math.abs(await scrollPosition(page) - before.scrollTop), 'Previously measured full output keeps the conversation scroll offset stable').toBeLessThanOrEqual(3)
  await scrollToTraceStep(page, mainTask, runId, 'tool/main/threshold-1')
  await expect(firstNode.locator('pre.trace-output')).toHaveText(firstOutput)
  expect(fixture.nodeReads, 'The already hydrated first step remains cached after threshold remount').toEqual([3])
  expect(fixture.unexpected).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('step-threshold-stable-reading.png'), animations: 'disabled' })
})

test('expanded execution history and output restore after the outer message list unmounts them', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const events = deterministicEvents(true)
  const runId = events[0].data.runId as string
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'run_finished', createdAt: '2026-01-01T13:00:00Z', data: { runId, status: 'succeeded' } })
  const messages: Message[] = [events[0].data, ...Array.from({ length: 110 }, (_, index): Message => ({
    id: `later-message-${index}`, runId: `later-run-${index}`, role: 'assistant', sequence: 10000 + index,
    content: `Independent later conversation reply ${index + 1}.`, modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: '2026-01-01T14:00:00Z',
  }))]
  const fixture = await mockExecution(page, mockOrigin, events, { messages })
  const scroller = page.locator('.messages')
  const mainProgressRows = new Set(events.filter(event => event.kind === 'model_delta' && !event.data.depth && (!event.data.taskId || event.data.taskId === 'main')).map(event => event.data.turn)).size
  await expect(page.locator('.message-space')).toHaveAttribute('data-count', String(messages.length + mainProgressRows))
  await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
  await scroller.dispatchEvent('wheel', { deltaY: -100 })
  await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  await expect(trace).toBeVisible()
  await processHeader(trace).getByRole('status').click()
  await expect.poll(() => fixture.eventPages.length).toBeGreaterThan(1)
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  const mainTask = trace.locator('.trace-task[data-task-id="main"]')
  const title = await scrollToTraceStep(page, mainTask, runId, 'tool/main/call-310')
  await title.click()
  const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  const expected = fullOutputText('Tool output virtual-middle-output')
  await expect(node.locator('.tool-output-entry pre')).toHaveText(expected)
  // Scrolling across other replies legitimately loads their first overviews.
  // Only rereading the inspected execution would violate its cache contract.
  const readCounts = () => ({ pages: fixture.eventPages.length,
    overviews: fixture.overviewReads.filter(path => path.includes(`/runs/${runId}/`)).length, outputs: fixture.outputReads.length })
  const reads = readCounts()
  await scroller.evaluate(element => { element.scrollTop = element.scrollHeight; element.dispatchEvent(new Event('scroll')) })
  await expect(trace, 'The outer message virtualizer really unmounts the inspected execution').toHaveCount(0)
  await expect(page.getByText('Independent later conversation reply 110.', { exact: true })).toBeVisible()
  await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  await expect(mainTask, 'The process keeps its user-controlled expanded state').toBeVisible()
  expect(Number(await mainTask.locator('.trace-steps').getAttribute('data-step-count')), 'Restored history retains all model and tool steps').toBeGreaterThanOrEqual(200)
  await expect(mainTask.locator('.trace-steps')).toHaveAttribute('data-virtualized', 'true')
  await scrollToTraceStep(page, mainTask, runId, 'tool/main/call-310')
  await expect(node.locator('.tool-output-entry pre'), 'The previously expanded output restores without another user click').toHaveText(expected)
  expect(readCounts(), 'Returning to a cached execution must not reread its history or output').toEqual(reads)
  expect(fixture.unexpected).toEqual([])
  const idle = await trace.evaluate(async element => {
    const notifications: string[] = []
    const capture = (event: ErrorEvent) => { if (/ResizeObserver/.test(event.message)) notifications.push(event.message) }
    addEventListener('error', capture)
    const samples: unknown[] = []
    for (let sample = 0; sample < 12; sample++) {
      await new Promise<void>(resolve => setTimeout(resolve, 160))
      const scroller = element.closest<HTMLElement>('.messages')!
      const virtual = element.querySelector('.trace-steps[data-virtualized="true"]')!
      const rect = virtual.getBoundingClientRect()
      samples.push({ top: rect.top, height: rect.height, scrollMargin: rect.top - scroller.getBoundingClientRect().top + scroller.scrollTop, scrollTop: scroller.scrollTop, total: scroller.scrollHeight, parentHeight: element.closest('.message-row')!.getBoundingClientRect().height, mounted: Array.from(virtual.querySelectorAll('.trace-step-row')).map(row => ({ index: row.getAttribute('data-index'), top: row.getBoundingClientRect().top, height: row.getBoundingClientRect().height })) })
    }
    removeEventListener('error', capture)
    return { notifications, samples }
  })
  writeFileSync(testInfo.outputPath('virtual-idle-layout.json'), JSON.stringify(idle, null, 2))
  await testInfo.attach('virtual-idle-layout', { body: JSON.stringify(idle, null, 2), contentType: 'application/json' })
  expect(idle.notifications, 'An idle restored execution must not keep producing observer loops').toEqual([])
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('message-virtualization-restored-execution.png'), animations: 'disabled' })
})

test('execution overview and complete task details survive live updates, finish, and reload', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockExecution(page, mockOrigin)
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const overview = trace.locator('.run-overview')
  const header = processHeader(trace)
  const checklist = trace.locator('.trace-checklist')
  const details = overview.locator(':scope > div > .antd-think-content')
  const assertChecklist = async (unfinishedStatus = 'in_progress') => {
    await expect(checklist, 'Each run has one checklist below its summary').toHaveCount(1)
    await expect(header.locator('.trace-checklist')).toHaveCount(1)
    await expect(details.locator('.trace-checklist'), 'The checklist is not repeated inside the execution details').toHaveCount(0)
    await expect(checklist.locator('[data-checklist-status]')).toHaveCount(initialChecklist.length)
    await expect(checklist.locator(`[data-checklist-status="${unfinishedStatus}"]`)).toHaveCount(2)
    await expect(checklist.locator('[data-checklist-status="done"]')).toHaveCount(1)
    for (const item of initialChecklist) await expect(checklist.getByText(item.text, { exact: true })).toBeVisible()
    const order = await trace.evaluate(element => {
      const summary = element.querySelector('.trace-summary')!.getBoundingClientRect()
      const checklist = element.querySelector('.trace-checklist')!.getBoundingClientRect()
      const details = element.querySelector(':scope > .antd-think > div > .antd-think-content')
      return { summaryBottom: summary.bottom, checklistTop: checklist.top, checklistBottom: checklist.bottom, detailsTop: details?.checkVisibility() ? details.getBoundingClientRect().top : null }
    })
    expect(order.checklistTop, 'Todo follows the execution summary').toBeGreaterThanOrEqual(order.summaryBottom - 1)
    if (order.detailsTop !== null) expect(order.detailsTop, 'Expanded details follow Todo').toBeGreaterThanOrEqual(order.checklistBottom - 1)
  }
  await expect(header, 'Each run has one summary toggle').toHaveCount(1)
  await expect(header.locator('.trace-overview-heading'), 'The summary is the toggle title').toHaveCount(1)
  await expect(trace.getByText('Execution process', { exact: true }), 'There is no separate execution-process row').toHaveCount(0)
  await expect(overview).toHaveAttribute('data-run-status', 'active')
  await expect(overview.locator('.trace-overview-heading')).toContainText('197 model calls')
  await expect(overview.locator('.trace-last-activity')).toBeVisible()
  await assertChecklist()
  await expect(header.locator('.trace-overview-heading')).toHaveAttribute('aria-expanded', 'false')
  await expect(header).toContainText('1/3 checklist items complete')
  await expect(trace.locator('.trace-task')).toHaveCount(0)
  await expect(trace.getByRole('button', { name: 'Read output', exact: true })).toHaveCount(0)
  expect(fixture.eventPages).toEqual([''])
  expect(fixture.outputReads).toEqual([])
  await checklist.scrollIntoViewIfNeeded()
  const checklistLayout = await checklist.evaluate(element => ({ left: element.getBoundingClientRect().left, right: element.getBoundingClientRect().right, width: innerWidth, overflow: element.scrollWidth - element.clientWidth }))
  expect(checklistLayout.left).toBeGreaterThanOrEqual(-1)
  expect(checklistLayout.right).toBeLessThanOrEqual(checklistLayout.width + 1)
  expect(checklistLayout.overflow).toBeLessThanOrEqual(2)
  await page.screenshot({ path: testInfo.outputPath('checklist-before-expansion.png'), animations: 'disabled' })
  await overview.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('overview.png'), animations: 'disabled' })

  await checklist.getByText(initialChecklist[0].text, { exact: true }).click()
  await expect(header.locator('.trace-overview-heading'), 'Clicking Todo expands the shared execution panel').toHaveAttribute('aria-expanded', 'true')
  await expect(trace.locator('.trace-task')).toHaveCount(6)
  await expect(overview.locator('.trace-last-activity time')).toHaveAttribute('datetime', fixture.events.at(-1)!.createdAt!)
  await assertChecklist()
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  await checklist.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('checklist-with-expanded-details.png'), animations: 'disabled' })
  await checklist.getByText(initialChecklist[1].text, { exact: true }).click()
  await expect(header.locator('.trace-overview-heading'), 'Clicking Todo again collapses the shared execution panel').toHaveAttribute('aria-expanded', 'false')
  await expect(trace.locator('.trace-task')).toHaveCount(0)
  await assertChecklist()
  await expect(overview).toBeVisible()
  await expect(header).toContainText('197 model calls')
  await overview.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('summary-collapsed-again.png'), animations: 'disabled' })
  await header.getByText('197 model calls', { exact: true }).click()
  await expect(trace.locator('.trace-task')).toHaveCount(6)
  await expect(trace.locator('.trace-task-group[data-task-id="task-5"]')).toHaveAttribute('data-task-status', 'succeeded')
  await expect(taskHeader(trace.locator('.trace-task-group[data-task-id="task-4"]'))).toContainText('Child session 4')
  await expect(trace.locator('.trace-task-group[data-task-id="task-2"]')).toHaveAttribute('data-task-status', 'failed')
  await expect.poll(() => fixture.eventPages.length).toBeGreaterThan(1)
  await expect(trace.locator('.trace-loading')).toHaveCount(0, { timeout: 15_000 })
  const initialHistoryPages = fixture.eventPages.length
  expect(initialHistoryPages).toBe(1 + Math.ceil(fixture.events.length / 100))
  expect(fixture.eventPages.slice(1).every(query => new URLSearchParams(query).get('compact') === '1')).toBe(true)
  await expect(trace.getByRole('button', { name: 'Load earlier steps', exact: true })).toHaveCount(0)
  await expect(trace.locator('.trace-task-group[data-agent-role="subagent"] .antd-thought-chain-node')).toHaveCount(0)
  const reportTask = trace.locator('.trace-task[data-task-id="task-4"]')
  await taskHeader(reportTask).click()
  await expect(reportTask.locator('.trace-report')).toBeVisible()
  expect(await reportTask.locator('.antd-thought-chain-node').count()).toBeLessThanOrEqual(100)
  const reportBody = reportTask.locator('.trace-report-body')
  await expect(reportBody.getByRole('heading', { level: 2 })).toHaveText(['Opening section', 'Middle section', 'Final section'])
  await expect(reportBody).toContainText('Child report 1004 ending — 完整输出 ✅.')
  await expect(reportTask.getByRole('button', { name: /Read output|Read next page|Read from start|Refresh output/ })).toHaveCount(0)
  expect(fixture.outputReads.filter(path => path.includes('model-output'))).toEqual([`/sessions/execution-session/runs/${fixture.runId}/model-output?turn=1004&full=1`])

  const nestedTask = trace.locator('.trace-task[data-task-id="task-3"]')
  await expect(nestedTask).toHaveAttribute('data-parent-task-id', 'task-2')
  await expect(nestedTask.locator('.trace-agent-delegator')).toHaveText('Delegated by Child session 2')
  await expect(taskHeader(nestedTask)).toHaveCount(1)
  await taskHeader(reportTask).click()

  // An expanded tool gets its full snapshot automatically; reopened reports reuse it.
  await taskHeader(reportTask).click()
  await expect(reportBody).toContainText('Child report 1004 ending — 完整输出 ✅.')
  expect(fixture.outputReads.filter(path => path.includes('model-output'))).toHaveLength(1)
  const toolEvent = [...fixture.events].reverse().find(event => event.kind === 'tool' && event.data.taskId === 'task-4' && event.data.tool?.outputRef?.artifactId && event.data.tool.outputRef.bytes > 4096)!
  const callId = toolEvent.data.tool.callId as string
  const toolTitle = reportTask.locator(`[id="trace-${fixture.runId}-0-${encodeURIComponent(`tool/task-4/${callId}`)}"]`)
  await expect(toolTitle).toHaveCount(1)
  await toolTitle.click()
  const toolNode = toolTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  const toolOutput = toolNode.locator('.tool-output-entry')
  await expect(toolOutput).toHaveCount(1)
  await expect(toolOutput.locator('details')).toHaveCount(0)
  const expectedTool = fullOutputText(`Tool output ${toolEvent.data.tool.outputRef.artifactId}`)
  await expect(toolOutput.locator('pre')).toHaveText(expectedTool)
  await expect(toolOutput.getByRole('button', { name: /Read output|Read next page|Read from start|Refresh output/ })).toHaveCount(0)
  const artifactReads = fixture.outputReads.filter(path => path.includes('/artifacts/'))
  expect(artifactReads).toEqual([`/sessions/execution-session/artifacts/${toolEvent.data.tool.outputRef.artifactId}/output?full=1`])
  await toolTitle.click()
  await toolTitle.click()
  await expect(toolOutput.locator('pre')).toHaveText(expectedTool)
  expect(fixture.outputReads.filter(path => path.includes('/artifacts/'))).toHaveLength(1)

  // Inspection disables automatic following. Unrelated SSE records keep this node open.
  await toolTitle.scrollIntoViewIfNeeded()
  await page.locator('.messages').evaluate(element => { element.scrollTop = Math.max(0, element.scrollTop - 30) })
  const captureLivePosition = () => toolTitle.evaluate(element => {
    const scroller = element.closest<HTMLElement>('.messages')!
    const trace = element.closest('.run-trace')!
    const header = trace.querySelector('.trace-process-header')!
    return { scrollTop: scroller.scrollTop, titleTop: element.getBoundingClientRect().top, headerHeight: header?.getBoundingClientRect().height, activityHeight: trace.querySelector('.trace-last-activity')?.getBoundingClientRect().height }
  })
  const beforeLivePosition = await captureLivePosition()
  const pagesBeforeUpdate = fixture.eventPages.length
  fixture.append('model_request', { taskId: 'main', depth: 0, turn: 198, provider: 'openai-chat', key: 'replay-model', id: 'gpt-6-astra', reasoningEffort: 'high' })
  fixture.append('model_delta', { taskId: 'main', depth: 0, turn: 198, content: 'New activity while inspecting an older tool. ' })
  await expect(overview.locator('.trace-overview-heading')).toContainText('198 model calls')
  await expect(toolOutput.locator('pre')).toBeVisible()
  const afterLivePosition = await captureLivePosition()
  writeFileSync(testInfo.outputPath('live-summary-reading-position.json'), JSON.stringify({ before: beforeLivePosition, after: afterLivePosition }, null, 2))
  // Wrapping activity text can change the height above this step; native
  // anchoring may adjust scrollTop while keeping the actual reading position.
  expect(Math.abs(afterLivePosition.titleTop - beforeLivePosition.titleTop)).toBeLessThanOrEqual(3)
  expect(fixture.eventPages.length).toBe(pagesBeforeUpdate)
  expect(fixture.sseRequests).toBeGreaterThan(1)

  const assistant: Message = { id: 'replay-assistant', runId: fixture.runId, role: 'assistant', sequence: fixture.events.at(-1)!.sequence + 2, outputTurn: 198, content: 'The source review is complete. The checklist remains as explicitly reported.', modelId: 'gpt-6-astra', modelKey: 'replay-model', createdAt: new Date().toISOString() }
  fixture.session.executing = false
  fixture.messages.push(assistant)
  fixture.append('agent_task', { taskId: 'main', task: { id: 'main', title: 'Main task', status: 'succeeded' } })
  const beforeFinish = (await toolTitle.boundingBox())!.y
  const captureReadingPosition = () => trace.evaluate(element => {
    const scroller = element.closest<HTMLElement>('.messages')!
    const viewport = scroller.getBoundingClientRect()
    const titles = [...element.querySelectorAll<HTMLElement>('.antd-thought-chain-node-title [id]')].map(title => ({ id: title.id, top: title.getBoundingClientRect().top, bottom: title.getBoundingClientRect().bottom })).filter(title => title.bottom > viewport.top && title.top < viewport.bottom)
    return { scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight, viewportTop: viewport.top, viewportBottom: viewport.bottom, titles, status: element.querySelector('.run-overview')?.getAttribute('data-run-status') }
  })
  const beforeReadingPosition = await captureReadingPosition()
  fixture.append('run_finished', { status: 'succeeded', message: assistant })
  await expect(overview).toHaveAttribute('data-run-status', 'succeeded')
  await expect(toolOutput.locator('pre')).toBeVisible()
  await expect(trace.locator(`[id="trace-${fixture.runId}-0-${encodeURIComponent('text/main/198')}"]`)).toHaveCount(0)
  await testInfo.attach('completion-reading-position', { body: JSON.stringify({ before: beforeReadingPosition, after: await captureReadingPosition() }, null, 2), contentType: 'application/json' })
  // Native scroll anchoring may change scrollTop when a preceding task gains a node.
  // The inspected step must stay in the same place on screen.
  expect(Math.abs((await toolTitle.boundingBox())!.y - beforeFinish)).toBeLessThanOrEqual(3)
  await expect(checklist.locator('[data-checklist-status="in_progress"]')).toHaveCount(0)
  await expect(checklist.locator('[data-checklist-status="incomplete"] > small')).toHaveText(['incomplete', 'incomplete'])
  await expect(checklist.locator('[data-checklist-status="incomplete"] .trace-checklist-icon svg[data-icon="minus"]')).toHaveCount(2)
  await expect(header).toContainText('1/3 checklist items complete')
  expect(fixture.events.find(event => event.kind === 'checklist')!.data.items).toEqual(initialChecklist)
  await page.screenshot({ path: testInfo.outputPath('inspection-finished.png'), animations: 'disabled' })

  const pagesBeforeReload = fixture.eventPages.length
  await page.reload()
  await expect(overview).toHaveAttribute('data-run-status', 'succeeded')
  await expect(overview.locator('.trace-overview-heading')).toContainText('198 model calls')
  await expect(trace.locator('.trace-task')).toHaveCount(0)
  await expect(header).toHaveCount(1)
  await expect(header.locator('.trace-overview-heading')).toHaveCount(1)
  await assertChecklist('incomplete')
  await expect(checklist.locator('[data-checklist-status="in_progress"]')).toHaveCount(0)
  await expect(header).toContainText('1/3 checklist items complete')
  expect(fixture.eventPages.length - pagesBeforeReload).toBe(1)
  expect(fixture.unexpected).toEqual([])
  await overview.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('reloaded.png'), animations: 'disabled' })
})

test('stopping a run shows interrupted Todo immediately and after reload without changing saved statuses', async ({ page, mockOrigin }, testInfo) => {
  await page.addInitScript(() => localStorage.setItem('vista-debug-mode', 'true'))
  const events = agentIdentityEvents()
  const runId = events[0].data.runId as string
  const savedItems: ChecklistItem[] = [...initialChecklist,
    { text: 'Optional follow-up', status: 'pending' },
    { text: 'Waiting for source access', status: 'blocked' },
  ]
  events.push({ sequence: events.at(-1)!.sequence + 1, kind: 'checklist', createdAt: '2026-01-01T12:01:00Z', data: { runId, items: savedItems } })
  const fixture = await mockExecution(page, mockOrigin, events)
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  const overview = trace.locator('.run-overview')
  const header = processHeader(trace)
  const checklist = trace.locator('.trace-checklist')
  await expect(overview).toHaveAttribute('data-run-status', 'active')
  await expect(checklist.locator('[data-checklist-status="in_progress"]')).toHaveCount(2)
  await expect(header).toContainText('1/5 checklist items complete')

  fixture.session.executing = false
  fixture.append('agent_task', { taskId: 'main', task: { id: 'main', title: 'Main task', status: 'canceled' } })
  fixture.append('run_finished', { status: 'canceled' })
  const assertStoppedChecklist = async () => {
    await expect(overview).toHaveAttribute('data-run-status', 'canceled')
    await expect(trace.locator('.trace-task'), 'Stopped Todo stays visible without opening the execution').toHaveCount(0)
    await expect(checklist.locator('[data-checklist-status="in_progress"]')).toHaveCount(0)
    await expect(checklist.locator('[data-checklist-status="interrupted"] > small')).toHaveText(['interrupted', 'interrupted'])
    await expect(checklist.locator('[data-checklist-status="interrupted"] .trace-checklist-icon svg[data-icon="stop"]')).toHaveCount(2)
    for (const status of ['done', 'pending', 'blocked']) await expect(checklist.locator(`[data-checklist-status="${status}"]`)).toHaveCount(1)
    for (const item of savedItems) {
      const row = checklist.locator('[data-checklist-status]').filter({ has: page.getByText(item.text, { exact: true }) })
      await expect(row.locator('.trace-checklist-icon svg'), 'Each Todo uses exactly one SVG icon').toHaveCount(1)
      await expect(row.locator('small')).toHaveText(item.status === 'in_progress' ? 'interrupted' : item.status)
    }
    await expect(header).toContainText('1/5 checklist items complete')
  }
  await assertStoppedChecklist()
  await overview.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('stopped-todo.png'), animations: 'disabled' })
  await header.getByRole('button', { name: 'Debug details', exact: true }).click()
  const diagnostics = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await expect(diagnostics).toBeVisible()
  const diagnosticEvents = JSON.parse(await diagnostics.locator('.trace-debug-body').innerText()).events as StreamEvent[]
  expect(diagnosticEvents.find(event => event.kind === 'checklist')!.data.items, 'Only visible statuses change; the source event remains intact').toEqual(savedItems)
  await page.keyboard.press('Escape')
  await page.reload()
  await assertStoppedChecklist()
  expect(fixture.events.find(event => event.kind === 'checklist')!.data.items).toEqual(savedItems)
  expect(fixture.unexpected).toEqual([])
  await overview.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('stopped-todo-reloaded.png'), animations: 'disabled' })
})

test('new runs keep old checklists on their original execution during updates and reloads', async ({ page, mockOrigin }) => {
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
  await expect(oldTrace.locator('.trace-checklist')).toContainText('Earlier unfinished work')
  const user: Message = { id: 'new-user', runId: newRun, sequence: 7, role: 'user', content: 'A different task', createdAt: new Date().toISOString() }
  fixture.messages.push(user)
  fixture.session.executing = true
  fixture.append('message', user)
  fixture.append('model_request', { runId: newRun, turn: 1, taskId: 'main', provider: 'openai-chat', id: 'gpt-6-astra' })
  const tool = { callId: 'new-read', name: 'read_file', summary: 'Read another file' }
  fixture.append('tool', { runId: newRun, turn: 1, taskId: 'main', status: 'started', tool })
  const assertSeparate = async () => {
    await expect(newTrace).toBeVisible()
    await expect(newTrace.locator('.trace-checklist')).toHaveCount(0)
    await expect(oldTrace.locator('.trace-checklist')).toContainText('Earlier unfinished work')
    await expect(oldTrace.locator('[data-checklist-status="incomplete"]')).toHaveCount(1)
  }
  await assertSeparate()
  await page.reload()
  await assertSeparate()
  fixture.append('tool', { runId: newRun, turn: 1, taskId: 'main', status: 'succeeded', tool, content: 'File read.' })
  const reply: Message = { id: 'new-reply', runId: newRun, sequence: events.at(-1)!.sequence + 1, role: 'assistant', content: 'New task completed.', createdAt: new Date().toISOString() }
  fixture.messages.push(reply)
  fixture.session.executing = false
  fixture.append('run_finished', { runId: newRun, status: 'succeeded', message: reply })
  await expect(newTrace.locator('.run-overview')).toHaveAttribute('data-run-status', 'succeeded')
  await assertSeparate()
  await page.reload()
  await assertSeparate()
  expect(fixture.events.find(event => event.kind === 'checklist')!.data.items).toEqual(oldItems)
  expect(fixture.unexpected).toEqual([])
})

test('main and nested subagents keep distinct identities, delegation, and independent results', async ({ page, mockOrigin, isMobile }, testInfo) => {
  test.setTimeout(45_000)
  const fixture = await mockExecution(page, mockOrigin, agentIdentityEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  await processHeader(trace).getByRole('status').click()
  await expect.poll(() => trace.locator('.antd-think-content:visible').evaluateAll(elements => elements.map(element => getComputedStyle(element).borderInlineStartWidth))).toEqual(['0px'])
  await expect(trace.locator('.trace-navigation')).toHaveCount(0)
  await expect(trace.locator('select, .ant-select')).toHaveCount(0)
  await expect(trace.getByRole('combobox')).toHaveCount(0)
  const agents = trace.locator('.trace-task-group')
  const expectedOrder = ['main', 'child-a', 'grandchild-a', 'child-b']
  await expect(agents).toHaveCount(4)
  await expect.poll(() => agents.evaluateAll(elements => elements.map(element => element.getAttribute('data-task-id')))).toEqual(expectedOrder)
  await expect(trace.locator('.run-task-summary')).toHaveCount(0)
  const identities = [
    { id: 'main', role: 'main', depth: 0, number: 0, label: 'Main session', parent: null },
    { id: 'child-a', role: 'subagent', depth: 1, number: 1, label: 'Child session 1', parent: 'Main session' },
    { id: 'grandchild-a', role: 'subagent', depth: 2, number: 3, label: 'Child session 3', parent: 'Child session 1' },
    { id: 'child-b', role: 'subagent', depth: 1, number: 2, label: 'Child session 2', parent: 'Main session' },
  ]
  for (const identity of identities) {
    const agent = trace.locator(`.trace-task-group[data-task-id="${identity.id}"]`)
    await expect(agent).toHaveAttribute('data-agent-role', identity.role)
    await expect(agent).toHaveAttribute('data-agent-depth', String(identity.depth))
    await expect(agent).toHaveAttribute('data-agent-number', String(identity.number))
    if (identity.parent) {
      await expect(agent.locator('.trace-agent-role')).toHaveText(identity.label)
      await expect(agent.locator('.trace-agent-role')).toBeVisible()
      await expect(agent.locator('.trace-agent-delegator')).toHaveText(`Delegated by ${identity.parent}`)
      await expect(agent.locator('.trace-agent-delegator')).toBeVisible()
    } else {
      await expect(agent.locator('.trace-agent-identity')).toHaveCount(0)
      await expect(agent.locator('.trace-agent-delegator')).toHaveCount(0)
    }
  }
  // Duplicate assignments do not become duplicate session identities. A nested
  // session keeps the same creation number even though DFS moves it before sibling 2.
  const instructions = fixture.events.find(event => event.kind === 'agent_task' && event.data.taskId === 'child-a')!.data.task.title as string
  expect(new TextEncoder().encode(instructions).length).toBeGreaterThan(300)
  expect(new TextEncoder().encode(instructions).length).toBeLessThanOrEqual(2048)
  expect(instructions.split('\n')).toHaveLength(5)
  await expect(trace.locator('.trace-agent-assignment')).toHaveCount(0)
  for (const identity of identities) {
    const agent = trace.locator(`.trace-task-group[data-task-id="${identity.id}"]`)
    if (identity.parent) {
      const purpose = taskHeader(agent).locator('.trace-agent-purpose')
      await expect(agent.locator('.trace-agent-purpose')).toHaveCount(1)
      await expect(purpose).toBeVisible()
      const summary = await purpose.textContent()
      expect(summary).toMatch(/^Inspect the shared source folder/)
      expect(summary).toMatch(/…$/)
      expect(summary!.length).toBeLessThanOrEqual(90)
      expect(summary).not.toContain('\n')
      await expect(taskHeader(agent)).not.toContainText(instructions)
      await purpose.click()
      await expect.poll(() => purpose.textContent()).toBe(instructions)
      await expect(purpose).toBeVisible()
      await expect(purpose).toContainText('完整委派说明结束。')
      await expect(purpose).toHaveCSS('white-space', 'pre-wrap')
      await expect(purpose).toHaveCSS('user-select', 'text')
      await expect(purpose).toHaveCSS('cursor', 'text')
      await purpose.click()
      await expect(purpose, 'Clicking expanded delegation text does not collapse the session').toHaveText(instructions)
      const contentBorders = await trace.locator('.antd-think-content:visible').evaluateAll(elements => elements.map(element => getComputedStyle(element).borderInlineStartWidth))
      expect(contentBorders.length).toBeGreaterThanOrEqual(2)
      expect(contentBorders.every(width => width === '0px'), 'Execution process and expanded child session Think content must have no inline border').toBe(true)
      await expect(agent.locator('.trace-agent-purpose')).toHaveCount(1)
      await expect(agent.locator('.trace-agent-assignment')).toHaveCount(0)
      await taskHeader(agent).locator('.trace-agent-role').click()
      await expect.poll(() => purpose.textContent()).toBe(summary)
      await expect(purpose).toBeVisible()
      await purpose.click()
      await expect.poll(() => purpose.textContent()).toBe(instructions)
      await expect(purpose).toBeVisible()
      if (identity.id === 'child-a' && !isMobile) {
        await purpose.scrollIntoViewIfNeeded()
        const bounds = (await purpose.boundingBox())!
        const lineHeight = await purpose.evaluate(element => parseFloat(getComputedStyle(element).lineHeight))
        await page.evaluate(() => window.getSelection()?.removeAllRanges())
        await page.mouse.move(bounds.x + 3, bounds.y + lineHeight / 2)
        await page.mouse.down()
        await page.mouse.move(bounds.x + Math.min(280, bounds.width - 8), bounds.y + lineHeight / 2, { steps: 12 })
        await page.mouse.up()
        const selected = await page.evaluate(() => window.getSelection()?.toString().trim() || '')
        expect(selected.length, 'A real mouse drag selects delegation text for copying').toBeGreaterThan(5)
        expect(instructions).toContain(selected)
        await expect(purpose, 'Selecting expanded delegation text keeps the session open').toHaveText(instructions)
        await testInfo.attach('selected-delegation-text', { body: selected, contentType: 'text/plain' })
        await assertNoHorizontalOverflow(page)
        await page.screenshot({ path: testInfo.outputPath('session-expanded-purpose.png'), animations: 'disabled' })
      }
    }
    const title = nodeTitle(agent, fixture.runId, `tool/${identity.id}/read-${identity.id}`)
    await title.click()
    const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
    await expect(node.locator(identity.id === 'grandchild-a' ? 'p.tool-output-error' : 'pre.trace-output').first()).toHaveText(`Independent result from ${identity.id}.`)
    for (const other of identities.filter(value => value.id !== identity.id)) {
      await expect(agent).not.toContainText(`Independent result from ${other.id}.`)
    }
  }
  await trace.locator('.trace-task-group[data-task-id="main"]').scrollIntoViewIfNeeded()
  await assertSessionTerminology(page)
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('agent-identities-and-results.png'), animations: 'disabled' })

  await expect.poll(() => agents.evaluateAll(elements => elements.map(element => element.getAttribute('data-task-id')))).toEqual(expectedOrder)
  for (const identity of identities) {
    await expect(nodeTitle(trace, fixture.runId, `tool/${identity.id}/read-${identity.id}`), 'All sessions and rounds remain directly available').toBeVisible()
  }
  await page.evaluate(() => localStorage.setItem('vista-language', 'zh-CN'))
  await page.reload()
  await processHeader(trace).getByRole('status').click()
  await expect(agents.locator('.trace-agent-role')).toHaveText(['子会话 1', '子会话 3', '子会话 2'])
  await expect(trace.locator('.trace-task-group[data-task-id="child-a"] .trace-agent-delegator')).toHaveText('由 主会话 委派')
  await expect(trace.locator('.trace-task-group[data-task-id="grandchild-a"] .trace-agent-delegator')).toHaveText('由 子会话 1 委派')
  await assertSessionTerminology(page)
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('session-identities-zh-CN.png'), animations: 'disabled' })
  // A newly delegated session can expose its purpose before its first model or
  // tool step arrives; the header must still provide the same expansion.
  fixture.append('agent_task', { taskId: 'child-empty', parentTaskId: 'main', task: { id: 'child-empty', parentId: 'main', title: instructions, status: 'started' } })
  const emptyChild = trace.locator('.trace-task-group[data-task-id="child-empty"]')
  await expect(emptyChild).toHaveCount(1)
  await expect(emptyChild.locator('.trace-agent-role')).toHaveText('子会话 4')
  await expect(emptyChild.locator('.antd-thought-chain-node')).toHaveCount(0)
  const emptyPurpose = taskHeader(emptyChild).locator('.trace-agent-purpose')
  const emptySummary = await emptyPurpose.textContent()
  expect(emptySummary).toMatch(/…$/)
  await emptyPurpose.click()
  await expect.poll(() => emptyPurpose.textContent()).toBe(instructions)
  await expect(emptyPurpose).toHaveCSS('white-space', 'pre-wrap')
  await expect(emptyChild.locator('.trace-empty')).toHaveCount(0)
  await expect(emptyChild.locator('.trace-agent-purpose')).toHaveCount(1)
  await expect(emptyChild.locator('.trace-agent-assignment')).toHaveCount(0)
  await emptyPurpose.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('session-no-steps-expanded-purpose.png'), animations: 'disabled' })
  await taskHeader(emptyChild).locator('.trace-agent-role').click()
  await expect.poll(() => emptyPurpose.textContent()).toBe(emptySummary)
  expect(fixture.unexpected).toEqual([])
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
    await processHeader(trace).getByRole('status').click()
    await nodeTitle(trace, runId, 'tool/main/fetch-page').click()
    const output = trace.locator('[data-output-id="webfetch-artifact"] pre.trace-output')
    await expect(output).toHaveCount(1)
    await expect.poll(() => output.textContent()).toBe(full)
    expect(fixture.outputReads.filter(path => path === fullPath + '?full=1')).toHaveLength(pass + 1)
    expect(await output.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(2)
  }
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
  const summary = processHeader(trace)
  const diagnosticsButton = summary.getByRole('button', { name: 'Debug details', exact: true })
  await expect(diagnosticsButton).toHaveCount(1)
  await diagnosticsButton.click()
  const closedDiagnostics = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await expect(closedDiagnostics).toBeVisible()
  await expect(trace.locator('.trace-task'), 'Debug details leave a collapsed summary closed').toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(closedDiagnostics).not.toBeVisible()
  await expect(summary.locator('.trace-overview-heading')).toBeVisible()
  await processHeader(trace).getByRole('status').click()
  await expect(taskHeader(trace.locator('.trace-task-group[data-task-id="main"]'))).toHaveCount(0)
  const fullTitle = nodeTitle(trace, runId, 'tool/main/shell-full')
  await expect(fullTitle).toContainText('cat src/example.ts')
  await expect(fullTitle).toHaveAttribute('title', 'cat src/example.ts')
  await fullTitle.click()
  const fullNode = fullTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  await expect(fullNode.locator('pre.trace-output')).toHaveCount(1)
  await expect(fullNode.locator('pre.trace-output')).toHaveText(source.full)
  expect(fixture.outputReads.filter(path => path.includes('artifact-full'))).toEqual([fullPath + '?full=1'])

  const failedTitle = nodeTitle(trace, runId, 'tool/main/shell-error')
  await failedTitle.click()
  const failedNode = failedTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  await expect(failedNode.locator('p.tool-output-error')).toHaveCount(1)
  await expect(failedNode.locator('p.tool-output-error')).toHaveText(source.error)
  await expect(failedNode.locator('pre.trace-output')).toHaveText('/synthetic\n')
  for (const callId of ['source-read', 'source-search']) await nodeTitle(trace, runId, `tool/main/${callId}`).click()
  await expect(trace.locator('.trace-search-results li')).toHaveCount(1)
  await expect(trace.locator('.trace-search-results')).toContainText('src/example.ts:2')
  await expect(trace.locator('.trace-search-results')).toContainText('Search output preserved.')
  await expect(trace).not.toContainText('Structured search preview replaced by its full result.')
  await expect(trace.locator('pre.trace-output').filter({ hasText: 'Source read once.' })).toHaveCount(1)

  for (const [callId, text] of [['empty-skills', 'No skills found'], ['checklist-updated', 'Checklist updated · 1 items']]) {
    const title = nodeTitle(trace, runId, `tool/main/${callId}`)
    const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
    await expect(node.locator('.antd-thought-chain-node-description')).toContainText(text)
    await expect(node.locator('.antd-thought-chain-node-collapse-icon')).toHaveCount(0)
    await expect(node.locator('.antd-thought-chain-node-content-box')).toHaveCount(0)
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
  await fullTitle.click()
  await nodeTitle(trace, runId, 'tool/main/source-search').click()
  await fullTitle.scrollIntoViewIfNeeded()
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('clean-tool-results.png'), animations: 'disabled' })
  expect(fixture.unexpected).toEqual([])
})

test('tool output failure falls back to one inline preview and Retry replaces it with the full result', async ({ page, mockOrigin }, testInfo) => {
  const source = cleanToolOutputEvents()
  const runId = source.events[0].data.runId as string
  const fullPath = '/sessions/execution-session/artifacts/artifact-full/output'
  const fixture = await mockExecution(page, mockOrigin, source.events, { outputContents: { [fullPath]: source.full }, outputFailures: { [fullPath]: 1 } })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  await processHeader(trace).getByRole('status').click()
  const title = nodeTitle(trace, runId, 'tool/main/shell-full')
  await title.click()
  const node = title.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  await expect(node.locator('.trace-output-load-error')).toBeVisible()
  await expect(node.locator('pre.trace-output')).toHaveCount(1)
  await expect(node.locator('pre.trace-output')).toHaveText(source.full.slice(0, 1024))
  await node.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(node.locator('.trace-output-load-error')).toHaveCount(0)
  await expect(node.locator('pre.trace-output')).toHaveCount(1)
  await expect(node.locator('pre.trace-output')).toHaveText(source.full)
  expect(fixture.outputReads).toEqual([fullPath + '?full=1', fullPath + '?full=1'])
  await title.click()
  await expect(node.locator('pre.trace-output')).toHaveCount(0)
  await title.click()
  await expect(node.locator('pre.trace-output'), 'Reopening a recovered tool reuses the successful full snapshot').toHaveText(source.full)
  expect(fixture.outputReads).toHaveLength(2)
  expect(fixture.unexpected).toEqual([])
  await title.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('tool-output-retry.png'), animations: 'disabled' })
})

test('truncated file and search steps hydrate on expansion, and jump to latest resumes following', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockExecution(page, mockOrigin, hydrationEvents())
  const trace = page.locator(`.run-trace[data-run-id="${fixture.runId}"]`)
  const readEvent = fixture.events.find(event => event.kind === 'tool' && event.data.tool.callId === 'read-source' && event.data.status === 'succeeded')!
  const searchEvent = fixture.events.find(event => event.kind === 'tool' && event.data.tool.callId === 'search-title' && event.data.status === 'succeeded')!
  await expect(trace.locator('.run-overview')).toHaveAttribute('data-run-status', 'active')
  expect(fixture.nodeReads).toEqual([])
  await processHeader(trace).getByRole('status').click()
  const readTitle = nodeTitle(trace, fixture.runId, 'tool/main/read-source')
  const searchTitle = nodeTitle(trace, fixture.runId, 'tool/main/search-title')
  await readTitle.click()
  const readNode = readTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
  await expect(readNode.locator('pre.trace-output').first()).toContainText('Hydrated file ending.')
  await expect.poll(() => fixture.nodeReads).toEqual([readEvent.sequence])
  await expect(readNode.locator('details.trace-details')).toHaveCount(0)
  await readTitle.click()
  await readTitle.click()
  expect(fixture.nodeReads).toEqual([readEvent.sequence])
  await searchTitle.click()
  const searchNode = searchTitle.locator('xpath=ancestor::div[contains(concat(" ",normalize-space(@class)," ")," antd-thought-chain-node ")][1]')
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
  await expect(reply.locator('.message-output-loading')).toBeVisible()
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
  expect(fixture.eventPages).toHaveLength(2)
  expect(fixture.eventPages.every(query => !new URLSearchParams(query).has('compact'))).toBe(true)
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
  await expect(output).toContainText('Live update ending')
  expect(fixture.outputReads.every(path => path.includes('limit=16384') && !path.includes('full=1'))).toBe(true)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  const closedReads = fixture.outputReads.length
  await page.waitForTimeout(1300)
  expect(fixture.outputReads).toHaveLength(closedReads)
  await processHeader(trace).getByRole('status').click()
  await nodeTitle(trace, runId, 'tool/main/live-command').click()
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
  fixture.outputContents.set(outputPath, content + 'NEW TAIL\n')
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

test('automatic report loading shows Retry after failure and keeps the recovered full snapshot', async ({ page, mockOrigin }, testInfo) => {
  const events = hydrationEvents().slice(0, 2)
  const runId = events[0].data.runId as string
  const content = fullOutputText('Recovered child report')
  events.push({ sequence: 3, kind: 'agent_task', createdAt: '2026-01-01T12:00:03Z', data: { runId, taskId: 'report-child', parentTaskId: 'main', task: { id: 'report-child', parentId: 'main', title: 'Synthetic child report', status: 'succeeded', report: { runId, turn: 99, bytes: Buffer.byteLength(content) } } } })
  const outputPath = `/sessions/execution-session/runs/${runId}/model-output?turn=99`
  const fixture = await mockExecution(page, mockOrigin, events, { outputContents: { [outputPath]: content }, outputFailures: { [outputPath]: 1 }, outputDelayMs: 700 })
  const trace = page.locator(`.run-trace[data-run-id="${runId}"]`)
  expect(fixture.outputReads).toEqual([])
  await processHeader(trace).getByRole('status').click()
  expect(fixture.outputReads).toEqual([])
  const child = trace.locator('.trace-task[data-task-id="report-child"]')
  await taskHeader(child).click()
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
  await taskHeader(child).click()
  await taskHeader(child).click()
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
  const context = page.locator('.sender-card .context-usage')
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
    const stats = manager.locator('.session-manager-stats')
    await expect(stats.locator(':scope > div')).toHaveCount(enabled ? 4 : 3)
    await expect(stats.getByText('Workers', { exact: true })).toHaveCount(enabled ? 1 : 0)
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
  await processHeader(trace).getByRole('status').click()
  await title.click()
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
  await processHeader(trace).getByRole('status').click()
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
  await processHeader(trace).getByRole('status').click()
  await expect(title).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('debug-mode-session-disabled.png'), animations: 'disabled' })
  await openAction('Settings')
  await expect(toggle).not.toBeChecked()
  await toggle.scrollIntoViewIfNeeded()
  await checkLayout(toggle)
  expect(fixture.unexpected).toEqual([])
})
