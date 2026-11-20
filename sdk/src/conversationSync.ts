import type { ConversationSnapshot, SessionActionPreview, StreamEvent, SyncCheckpoint } from './types.js'
import type { VistaClient } from './vista.js'

export type ConversationSyncStatus = {
  phase: 'connecting' | 'recovering' | 'ready' | 'error' | 'stopped'
  error?: string
}
export type SyncDiagnostic = { at: string; reason: string; cursor: number; error?: string }
export type ConversationSyncDiagnostics = {
  sessionId: string; scope?: number; cursor: number; lastActivityAt?: string; checkpoint?: SyncCheckpoint; history: SyncDiagnostic[]
}
type Options = {
  client: Pick<VistaClient, 'request' | 'streamEvents'>; sessionId: string; scope?: number; signal: AbortSignal
  cursor: () => number; replySequence: () => number
  inputQueue: () => { count: number; paused: boolean }
  applyEvent: (event: StreamEvent, live: boolean) => void
  applySnapshot: (snapshot: ConversationSnapshot) => void
  applySessionAction?: (action: SessionActionPreview | null) => void
  applyCheckpoint?: (checkpoint: SyncCheckpoint) => void
  status: (status: ConversationSyncStatus) => void
  diagnostics: (value: ConversationSyncDiagnostics) => void
}

// One selected-conversation owner serializes repairs; the SSE transport owns
// reconnect/backoff. Neither path submits messages or invokes model execution.
export function createConversationSync(options: Options) {
  const { client, sessionId, signal } = options
  const base = `/sessions/${encodeURIComponent(sessionId)}`
  let initialized = false, dirty = true, snapshotRequired = true
  let stopped = false
  let stream: AbortController | undefined
  let repair: Promise<void> | undefined
  let revision = 0
  let checkpoint: SyncCheckpoint | undefined
  let lastActivityAt: string | undefined
  const history: SyncDiagnostic[] = []
  function diagnostic(reason: string, error?: unknown) {
    history.push({ at: new Date().toISOString(), reason, cursor: options.cursor(), ...(error ? { error: (error instanceof Error ? error.stack || String(error) : String(error)).slice(0, 2000) } : {}) })
    if (history.length > 32) history.shift()
    options.diagnostics({ sessionId, scope: options.scope, cursor: options.cursor(), lastActivityAt, checkpoint, history: [...history] })
  }
  async function read<T>(path: string): Promise<T> {
    const controller = new AbortController()
    const abort = () => controller.abort(signal.reason)
    signal.addEventListener('abort', abort, { once: true })
    const timer = setTimeout(() => controller.abort(new Error('Conversation synchronization timed out')), 10_000)
    try {
      if (signal.aborted) throw signal.reason
      return await client.request<T>(path, { signal: controller.signal })
    } finally { clearTimeout(timer); signal.removeEventListener('abort', abort) }
  }
  function active() { return !signal.aborted }
  function validate(snapshot: ConversationSnapshot) {
    if (!snapshot || snapshot.session?.id !== sessionId || !Number.isSafeInteger(snapshot.throughSequence) || snapshot.throughSequence < 0
      || !Array.isArray(snapshot.messages) || !Array.isArray(snapshot.events) || !Array.isArray(snapshot.queuedInputs)
      || snapshot.queuedInputs.some(input => !input.id || typeof input.content !== 'string' || !['queued', 'paused'].includes(input.status))
      || new Set(snapshot.queuedInputs.map(input => input.id)).size !== snapshot.queuedInputs.length
      || snapshot.messages.some(message => !message.id || typeof message.content !== 'string' || !Number.isSafeInteger(message.sequence) || message.sequence <= 0 || message.sequence > snapshot.throughSequence)
      || snapshot.events.some(event => !Number.isSafeInteger(event.sequence) || event.sequence <= 0 || event.sequence > snapshot.throughSequence || typeof event.kind !== 'string' || !event.data || typeof event.data !== 'object')) {
      throw new Error('Invalid conversation snapshot')
    }
  }
  function queueMismatch(point: SyncCheckpoint) {
    if (point.throughSequence !== options.cursor()) return false
    const queue = options.inputQueue()
    return queue.count !== (point.session.pendingInputCount || 0) || queue.paused !== !!point.session.inputQueuePaused
  }
  async function rebuild() {
    const snapshot = await read<ConversationSnapshot>(`${base}/snapshot`)
    validate(snapshot)
    if (!active()) return
    options.applySnapshot(snapshot)
    checkpoint = { throughSequence: snapshot.throughSequence, session: snapshot.session }
    initialized = true
    diagnostic('snapshot applied')
  }
  async function reconcile() {
    if (!active() || !dirty) return
    if (repair) return repair
    options.status({ phase: initialized ? 'recovering' : 'connecting' })
    repair = (async () => {
      while (active() && dirty) {
        const started = revision
        // Consume this request only. A later invalidation must survive this read.
        const rebuildRequested = snapshotRequired
        snapshotRequired = false
        if (!initialized || rebuildRequested) await rebuild()
        else {
          try {
            const target = checkpoint && checkpoint.throughSequence > options.cursor() ? checkpoint.throughSequence : undefined
            for (let pageNumber = 0; active(); pageNumber++) {
              if (pageNumber === 10) throw new Error('Conversation replay exceeds recovery window')
              const cursor = options.cursor()
              const bounded = target !== undefined && target > cursor
              const events = await read<StreamEvent[]>(`${base}/event-page?after=${cursor}${bounded ? `&through=${target}` : ''}`)
              if (!active()) return
              for (const event of events) {
                if (event.sequence > options.cursor()) {
                  if (event.kind === 'conversation_reverted') { snapshotRequired = true; break }
                  options.applyEvent(event, false)
                }
                if (snapshotRequired) break
              }
              if (snapshotRequired) { snapshotRequired = false; await rebuild(); break }
              if (events.length && options.cursor() <= cursor) throw new Error('Conversation replay did not advance')
              if (target !== undefined && options.cursor() >= target || target === undefined && events.length < 100) break
              if (events.length < 100) throw new Error('Conversation replay did not reach checkpoint')
            }
            if (checkpoint && (options.replySequence() < (checkpoint.session.latestReplySequence || 0) || queueMismatch(checkpoint))) await rebuild()
            diagnostic('replay applied')
          } catch (error) {
            if (!active()) return
            diagnostic('replay failed; rebuilding', error)
            await rebuild()
          }
        }
        if (active() && revision === started) dirty = false
      }
    })().catch(error => { snapshotRequired = true; throw error }).finally(() => { repair = undefined })
    return repair
  }
  function launch() {
    if (!active()) return
    stream?.abort()
    const controller = stream = new AbortController()
    const current = () => active() && stream === controller && !controller.signal.aborted
    void client.streamEvents(sessionId, {
      signal: controller.signal,
      after: options.cursor,
      beforeConnect: reconcile,
      onActivity() { if (current()) lastActivityAt = new Date().toISOString() },
      onEvent(event) {
        if (!current()) return
        if (event.kind === 'conversation_reverted') {
          reconnect('rollback', true)
          return
        }
        try { options.applyEvent(event, true) }
        catch (error) { snapshotRequired = true; throw error }
      },
      onSessionAction(action) { if (current()) options.applySessionAction?.(action) },
      onCheckpoint(point) {
        if (!current()) return
        if (point.session.id !== sessionId) throw new Error('Checkpoint belongs to another conversation')
        checkpoint = point
        diagnostic('checkpoint')
        if (point.throughSequence < options.cursor()) return // A newer snapshot/replay can win the race.
        if (point.throughSequence > options.cursor() || options.replySequence() < (point.session.latestReplySequence || 0) || queueMismatch(point)) {
          if (point.throughSequence === options.cursor()) snapshotRequired = true
          throw new Error('Conversation is behind the server checkpoint')
        }
        options.applyCheckpoint?.(point)
        options.status({ phase: point.session.open ? 'ready' : 'stopped' })
        if (!point.session.open) { stopped = true; controller.abort() }
      },
      onDisconnect() { if (current()) { dirty = true; checkpoint = undefined; options.status({ phase: 'recovering' }); diagnostic('stream ended') } },
      onError(error, permanent) {
        if (!current()) return
        dirty = true
        stopped = !!permanent
        options.status({ phase: 'error', error: String(error).slice(0, 2000) })
        diagnostic(permanent ? 'stream stopped' : 'stream failed', error)
      },
    }).catch(error => {
      if (current()) { dirty = true; options.status({ phase: 'error', error: String(error) }); diagnostic('stream failed', error) }
    })
  }
  async function start() {
    try { await reconcile() }
    catch (error) {
      if (active()) { options.status({ phase: 'error', error: String(error) }); diagnostic('initialization failed', error) }
      if (terminal(error)) { stopped = true; return }
    }
    if (active()) launch()
  }
  function reconnect(reason: string, rebuildView = false) {
    if (!active() || stopped && reason !== 'manual recovery' && reason !== 'conversation reopened') return
    stopped = false
    dirty = true
    revision++
    snapshotRequired ||= rebuildView
    if (reason === 'foreground' || reason === 'online') checkpoint = undefined
    diagnostic(reason)
    options.status({ phase: initialized ? 'recovering' : 'connecting' })
    launch()
  }
  signal.addEventListener('abort', () => stream?.abort(), { once: true })
  return { start, reconnect, diagnostic }
}

function terminal(error: unknown) {
  const status = error && typeof error === 'object' && 'status' in error ? Number(error.status) : 0
  return status >= 400 && status < 500 && status !== 408 && status !== 429
}
