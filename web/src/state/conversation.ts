import type { Artifact, ContextNotice, Job, Message, ModelDraft, ModelInfo, ModelResultView, OutputView, QueuedInput, RunProgress, Session, StreamEvent } from '../client/types'
import { jobActive } from './jobs.ts'
import { reduceMainOutputEvent, type MainOutputRecord } from './mainOutputs.ts'

export const messageWindowLimit = 300

export type ReplyTraceSegment = {
  key: string
  runId: string
  anchorId: string
  after: number
  through?: number
  startedAt: string
  active: boolean
}

// One execution can publish several answers. Keep each answer's work on the
// user message that began it; additional steering before an answer stays in
// that segment. The key survives the arrival of the answer and virtualization.
export function replyTraceSegments(messages: readonly Message[], activeRunId = '', hasNewer = false): Map<string, ReplyTraceSegment> {
  const byRun = new Map<string, Message[]>()
  for (const message of messages) {
    if (!message.runId) continue
    const run = byRun.get(message.runId) || []
    run.push(message)
    byRun.set(message.runId, run)
  }
  const segments = new Map<string, ReplyTraceSegment>()
  for (const [runId, run] of byRun) {
    run.sort((a, b) => a.sequence - b.sequence)
    let after = 0
    let pending: Message[] = []
    function retain(reply?: Message, latest = false) {
      const anchor = pending.find(message => message.role === 'user') || pending[0]!
      // The server supplies the preceding reply even when it is off-page.
      const lower = reply?.resultsAfter ?? after
      const segment: ReplyTraceSegment = {
        key: `${runId}/${lower}`, runId, anchorId: anchor.id, after: lower,
        through: reply && (!latest || activeRunId === runId || hasNewer) ? reply.sequence : undefined,
        startedAt: anchor.role === 'user' ? anchor.createdAt : '',
        active: !reply && latest && activeRunId === runId,
      }
      for (const message of pending) segments.set(message.id, segment)
      pending = []
    }
    for (let index = 0; index < run.length; index++) {
      const message = run[index]!
      pending.push(message)
      if (message.role === 'assistant') {
        retain(message, index === run.length - 1)
        after = message.sequence
      }
    }
    if (pending.length) retain(undefined, true)
  }
  return segments
}

export type ConversationState = {
  selected: Session | null
  messages: Message[]
  processEvents: StreamEvent[]
  modelDrafts: ModelDraft[]
  mainOutputs: MainOutputRecord[]
  modelResults: ModelResultView[]
  modelOutput: Record<string, OutputView>
  contextNotices: ContextNotice[]
  queuedInputs: QueuedInput[]
  activeRunID: string
  activeRunSequence: number
  runProgress: RunProgress | null
  jobs: Job[]
  artifacts: Artifact[]
  modelByRun: Record<string, ModelInfo>
  lastEvent: number
  latestMessageSequence: number
  hasOlder: boolean
  hasNewer: boolean
  finishedRuns: Record<string, boolean>
}

export function createConversationState(selected: Session | null = null): ConversationState {
  return {
    selected, messages: [], processEvents: [], modelDrafts: [], mainOutputs: [], modelResults: [], modelOutput: {},
    contextNotices: [], queuedInputs: [], activeRunID: '', activeRunSequence: 0,
    runProgress: null, jobs: [], artifacts: [], modelByRun: {},
    lastEvent: 0, latestMessageSequence: 0, hasOlder: false, hasNewer: false, finishedRuns: {},
  }
}

// The database owns history. Keep full bodies only for the current message
// window; unloaded indexes can fetch their complete text again on visibility.
export function pruneConversationWindow(state: ConversationState): ConversationState {
  const runs = new Set(state.messages.map(message => message.runId))
  const messages = new Set(state.messages.map(message => message.id))
  const replies = new Set(state.messages.filter(message => message.outputTurn).map(message => `${message.runId}/${message.outputTurn}`))
  const first = state.hasOlder ? state.messages[0]?.sequence || 0 : 0
  const last = state.hasNewer ? state.messages.at(-1)?.sequence || 0 : Infinity
  const metadataRuns = new Set([...runs, state.activeRunID])
  return { ...state,
    mainOutputs: state.mainOutputs.filter(output => runs.has(output.runId)).map(output =>
      output.sequence >= first && output.sequence <= last || replies.has(`${output.runId}/${output.turn}`)
        ? output : { ...output, content: '', contentSequence: 0, loaded: false, deltas: [] }),
    modelOutput: Object.fromEntries(Object.entries(state.modelOutput).filter(([id]) => messages.has(id))),
    modelResults: state.modelResults.filter(result => runs.has(result.runId)),
    modelDrafts: state.modelDrafts.filter(draft => runs.has(draft.runId)),
    modelByRun: Object.fromEntries(Object.entries(state.modelByRun).filter(([id]) => metadataRuns.has(id))),
    finishedRuns: Object.fromEntries(Object.entries(state.finishedRuns).filter(([id]) => metadataRuns.has(id))),
  }
}

export function appendMessage(state: ConversationState, message: Message | null | undefined): ConversationState {
  if (!message) return state
  const next = { ...state, latestMessageSequence: Math.max(state.latestMessageSequence, message.sequence) }
  if (message.role === 'assistant' && next.selected) next.selected = { ...next.selected,
    latestReplySequence: Math.max(next.selected.latestReplySequence || 0, message.sequence) }
  if (message.role === 'assistant' && message.outputTurn) {
    next.mainOutputs = next.mainOutputs.map(output => output.runId === message.runId && output.turn === message.outputTurn ? { ...output, complete: true } : output)
    next.modelDrafts = next.modelDrafts.filter(draft => !(draft.runId === message.runId && !draft.depth && draft.turn === message.outputTurn))
  }
  if (next.hasNewer || next.messages.some(item => item.id === message.id)) return next
  const results = messageResults(message, next.modelResults)
  const messages = [...next.messages, message.role === 'assistant' ? { ...message, modelResults: results } : message].sort((a, b) => a.sequence - b.sequence)
  return pruneConversationWindow({ ...next, messages: messages.slice(-messageWindowLimit), hasOlder: next.hasOlder || messages.length > messageWindowLimit })
}

export function messageResults(message: Message, modelResults: readonly ModelResultView[]): ModelResultView[] {
  if (message.role !== 'assistant' || !message.runId) return []
  return runResults(message.runId, [message], modelResults)
    .filter(result => result.sequence > (message.resultsAfter || 0) && result.sequence <= message.sequence)
}

export function runResults(runId: string, messages: readonly Message[], modelResults: readonly ModelResultView[]): ModelResultView[] {
  if (!runId) return []
  const results = new Map<number, ModelResultView>()
  for (const message of messages) for (const result of message.modelResults || []) if (result.runId === runId) results.set(result.sequence, result)
  for (const result of modelResults) if (result.runId === runId) results.set(result.sequence, result)
  return [...results.values()].sort((a, b) => a.sequence - b.sequence)
}

// Events are ordered by the server's sequence, independently of model turn/depth.
// Return the original object for duplicate replay, so callers can skip side effects too.
export function reduceConversationEvent(state: ConversationState, event: StreamEvent): ConversationState {
  if (event.sequence <= state.lastEvent) return state
  let next: ConversationState = { ...state, lastEvent: event.sequence }
  const data = event.data
  const runId: string | undefined = data?.runId
  const depth = data?.depth || 0
  next.mainOutputs = reduceMainOutputEvent(state.mainOutputs, event, runId ? state.modelByRun[runId] : undefined)
  if (['model_request', 'model_delta', 'model_reasoning_delta', 'model_result', 'tool', 'source', 'agent_task', 'checklist', 'artifact', 'job', 'context_notice', 'run_finished'].includes(event.kind)) next.processEvents = [...state.processEvents, event].slice(-1500)
  if (event.kind === 'message' && data?.role === 'user' && runId && event.sequence >= state.activeRunSequence) {
    next.activeRunID = runId
    next.activeRunSequence = event.sequence
    next.runProgress = { runId, phase: 'starting' }
    if (next.selected) next.selected = { ...next.selected, pendingReply: undefined, executing: true, latestRunStatus: 'running' }
  } else if (runId && runId === next.activeRunID && !depth && !next.finishedRuns[runId]) {
    if (event.kind === 'model_request' && !data?.purpose) next.runProgress = { runId, phase: 'model' }
    else if (event.kind === 'model_reasoning_delta') next.runProgress = { runId, phase: 'reasoning' }
    else if (event.kind === 'model_delta') next.runProgress = { runId, phase: 'response' }
    else if (event.kind === 'model_result') next.runProgress = { runId, phase: 'processing' }
    else if (event.kind === 'tool') next.runProgress = data?.status === 'started' ? { runId, phase: 'tool', tool: data?.tool?.name } : { runId, phase: 'processing' }
  }
  if ((event.kind === 'session_title' || event.kind === 'session_renamed') && next.selected && typeof data?.title === 'string') next.selected = { ...next.selected, title: data.title }
  if (event.kind === 'message') next = appendMessage(next, data as Message)
  if (event.kind === 'run_finished' && runId) {
    next.finishedRuns = { ...next.finishedRuns, [runId]: true }
    if (runId === next.activeRunID) {
      next.runProgress = { runId, phase: 'finished' }
      if (next.selected) next.selected = { ...next.selected, pendingReply: data?.pendingReply || undefined, executing: false, latestRunStatus: data?.status }
    }
    next = appendMessage(next, data?.message as Message | null)
    next.modelDrafts = next.modelDrafts.map(draft => draft.runId === runId ? { ...draft, finished: true } : draft)
  }
  if (event.kind === 'model_delta' && runId && data?.turn > 0 && typeof data?.content === 'string' && !next.finishedRuns[runId]
    && (depth || !next.messages.some(message => message.role === 'assistant' && message.runId === runId && message.outputTurn === data.turn))) {
    const index = next.modelDrafts.findIndex(draft => draft.runId === runId && draft.turn === data.turn && draft.depth === depth)
    if (index >= 0) next.modelDrafts = next.modelDrafts.map((draft, i) => i === index ? { ...draft, content: (draft.content + data.content).slice(0, 8192) } : draft)
    else next.modelDrafts = [...next.modelDrafts, { runId, turn: data.turn, depth, sequence: event.sequence, content: data.content.slice(0, 8192), finished: false }].slice(-40)
  }
  if (event.kind === 'model_result' && data?.modelResult && runId) {
    const result = data.modelResult
    next.modelDrafts = next.modelDrafts.map(draft => draft.runId === runId && draft.turn === data.turn && draft.depth === depth ? { ...draft, finished: true } : draft)
    next.modelResults = [...next.modelResults, { sequence: event.sequence, runId, turn: data.turn, depth, createdAt: event.createdAt, ...(data.taskId ? { taskId: data.taskId, parentTaskId: data.parentTaskId } : {}), purpose: result.purpose, provider: result.provider, id: result.id, finishReason: result.finishReason, reasoningEffort: result.reasoningEffort, usage: result.usage, durationMs: result.durationMs }]
  }
  if (['input_queued', 'input_updated', 'input_status'].includes(event.kind) && data?.id) {
    const existing = next.queuedInputs.some(input => input.id === data.id)
    next.queuedInputs = existing ? next.queuedInputs.map(input => input.id === data.id ? { ...input, ...data } : input)
      : data.content !== undefined ? [...next.queuedInputs, data as QueuedInput] : next.queuedInputs
  }
  if (event.kind === 'input_delivered' && data?.id) next.queuedInputs = next.queuedInputs.filter(input => input.id !== data.id)
  if (event.kind === 'input_queue_state') {
    if (next.selected) next.selected = { ...next.selected, inputQueuePaused: !!data?.paused }
    if (!data?.paused) next.queuedInputs = next.queuedInputs.map(input => input.status === 'paused' ? { ...input, status: 'queued' } : input)
  }
  if (event.kind.startsWith('input_') && next.selected) next.selected = { ...next.selected,
    pendingInputCount: next.queuedInputs.filter(input => ['queued', 'paused'].includes(input.status)).length }
  if (event.kind === 'context_notice' && !depth) next.contextNotices = [...next.contextNotices, { sequence: event.sequence, runId, message: data.message, summary: data.summary }].slice(-10)
  if (event.kind === 'job' && data?.id) next.jobs = [data as Job, ...next.jobs.filter(item => item.id !== data.id)].sort((a, b) => Number(jobActive(b)) - Number(jobActive(a))).slice(0, 200)
  if (event.kind === 'job_output' && data?.id && Number.isSafeInteger(data.bytes) && data.bytes >= 0) next.jobs = next.jobs.map(job => job.id === data.id ? { ...job, bytes: Math.max(job.bytes, data.bytes) } : job)
  if (event.kind === 'artifact' && data?.id) next.artifacts = [data as Artifact, ...next.artifacts.filter(item => item.id !== data.id)].slice(0, 200)
  if (event.kind === 'model_request' && runId && !depth && !data?.purpose) next.modelByRun = { ...next.modelByRun, [runId]: { key: data.key || '', provider: data.provider, id: data.id, reasoningEffort: data.reasoningEffort } }
  return next.messages.length ? pruneConversationWindow(next) : next
}
