import { streamSSE } from './sse.ts'
import type { ServerStatus, SessionListSnapshot, ModelCatalog, OMPCatalogInfo, LoginUpdate, MCPState, ResourceStorageStatus, Memories } from './types.ts'
import type { ModelDiagnosticSummary } from './modelDiagnostics.ts'

export const stateTopics = ['sessions', 'status', 'models', 'catalog', 'provider-login', 'mcp', 'storage', 'memories', 'diagnostics'] as const
export type StateTopic = typeof stateTopics[number]
export type StateConnection = { phase: 'connecting' | 'ready' | 'error' | 'stopped'; error?: string }
export type WorkspaceSnapshots = {
  sessions?: SessionListSnapshot; status?: ServerStatus; models?: ModelCatalog; catalog?: OMPCatalogInfo
  providerLogins?: LoginUpdate[]; mcp?: MCPState; storage?: ResourceStorageStatus; memories?: Memories
  diagnostics?: { after: number; through: number; records: ModelDiagnosticSummary[] }
}
type Checkpoint = WorkspaceSnapshots & { epoch: string; revisions: Record<StateTopic, number> }
type Listener = {
  topics: StateTopic[]
  receive: (topics: StateTopic[], signal: AbortSignal, snapshots: WorkspaceSnapshots, revisions: Checkpoint['revisions']) => Promise<void>
  active: boolean; lifetime: AbortController; seen: Partial<Record<StateTopic, number>>; epoch: string; attempt: number; pending?: Promise<void>
}
const snapshotKeys = { sessions: 'sessions', status: 'status', models: 'models', catalog: 'catalog',
  'provider-login': 'providerLogins', mcp: 'mcp', storage: 'storage', memories: 'memories', diagnostics: 'diagnostics' } as const
type Options = {
  open: (metrics: boolean, signal: AbortSignal) => Promise<Response>
  status: (status: StateConnection) => void
  metrics: (status: ServerStatus) => void
  reconnectDelayMs?: number
  idleTimeoutMs?: number
}

function validStatus(status: ServerStatus) {
  return status?.state === 'running' && !!status.sessions && Array.isArray(status.workerMetrics) && Number.isFinite(Date.parse(status.sampledAt))
}

export function createWorkspaceSync(options: Options) {
  const listeners = new Set<Listener>()
  let connection: AbortController | undefined
  let metrics = false
  let stopped = true
  let attempt = 0
  let latest: { point: Checkpoint; signal: AbortSignal; attempt: number } | undefined

  function deliver(listener: Listener, state: NonNullable<typeof latest>) {
    const run = async () => {
      if (!listener.active || state.signal.aborted) return
      const topics = listener.topics.filter(topic => listener.attempt !== state.attempt || listener.epoch !== state.point.epoch || listener.seen[topic] !== state.point.revisions[topic])
      if (!topics.length) return
      const timeout = new AbortController()
      const timer = setTimeout(() => timeout.abort(new Error('State synchronization timed out')), 10_000)
      const signal = AbortSignal.any([state.signal, listener.lifetime.signal, timeout.signal])
      try {
        await listener.receive(topics, signal, state.point, state.point.revisions)
        if (signal.aborted) throw signal.reason
        listener.seen = { ...state.point.revisions }; listener.epoch = state.point.epoch; listener.attempt = state.attempt
      } catch (error) { if (listener.active) throw error }
      finally { clearTimeout(timer); timeout.abort() }
    }
    return listener.pending = (listener.pending ?? Promise.resolve()).catch(() => {}).then(run)
  }

  function subscribe(topics: StateTopic[], receive: Listener['receive']) {
    const listener: Listener = { topics, receive, active: true, lifetime: new AbortController(), seen: {}, epoch: '', attempt: -1 }
    listeners.add(listener)
    // A newly opened view receives the latest valid payload, or performs one
    // initial snapshot read. It does not wait for another change/heartbeat.
    if (latest) {
      const state = latest
      void deliver(listener, state).catch(error => {
        if (listener.active && !stopped && !state.signal.aborted) { options.status({ phase: 'error', error: String(error) }); connect() }
      })
    }
    return () => { listener.active = false; listener.lifetime.abort(); listeners.delete(listener) }
  }

  function connect() {
    connection?.abort()
    const owner = connection = new AbortController()
    const current = () => !owner.signal.aborted && connection === owner
    latest = undefined
    options.status({ phase: 'connecting' })
    void streamSSE(signal => options.open(metrics, signal), {
      signal: owner.signal,
      reconnectDelayMs: options.reconnectDelayMs,
      idleTimeoutMs: options.idleTimeoutMs,
      onConnect() { attempt++; latest = undefined },
      onDisconnect() { if (current()) options.status({ phase: 'error', error: 'State connection closed' }) },
      onError(error) { if (current()) options.status({ phase: 'error', error: String(error) }) },
      async onFrame(frame, signal) {
        if (frame.event === 'metrics') {
          const status: ServerStatus = JSON.parse(frame.data)
          if (!validStatus(status)) throw new Error('Invalid metrics frame')
          if (current()) options.metrics(status)
          return
        }
        if (frame.event !== 'state') return
        const point: Checkpoint = JSON.parse(frame.data)
        if (!point.epoch || !point.revisions || stateTopics.some(topic => !Number.isSafeInteger(point.revisions[topic]) || point.revisions[topic]! < 0)) throw new Error('Invalid state checkpoint')
        if (point.sessions !== undefined && (!point.sessions || !Array.isArray(point.sessions.items) || !point.sessions.throughSequences
          || point.sessions.items.some(session => !session || typeof session.id !== 'string' || typeof session.title !== 'string'
            || !Number.isSafeInteger(point.sessions!.throughSequences[session.id]) || point.sessions!.throughSequences[session.id]! < 0))) throw new Error('Invalid session list snapshot')
        if (point.status !== undefined && !validStatus(point.status)) throw new Error('Invalid status snapshot')
        if (point.models !== undefined && (!point.models || typeof point.models.default !== 'string' || !Array.isArray(point.models.models))) throw new Error('Invalid models snapshot')
        if (point.catalog !== undefined && (!point.catalog || typeof point.catalog.revision !== 'string')) throw new Error('Invalid catalog snapshot')
        if (point.providerLogins !== undefined && (!Array.isArray(point.providerLogins) || point.providerLogins.some(login => !login?.id || typeof login.status !== 'string'))) throw new Error('Invalid login snapshot')
        if (point.mcp !== undefined && (!point.mcp || !Array.isArray(point.mcp.servers) || !Array.isArray(point.mcp.logins))) throw new Error('Invalid MCP snapshot')
        if (point.storage !== undefined && (!point.storage || typeof point.storage.phase !== 'string')) throw new Error('Invalid storage snapshot')
        if (point.memories !== undefined && (!point.memories || !Array.isArray(point.memories.memories))) throw new Error('Invalid memories snapshot')
        if (point.diagnostics !== undefined && (!point.diagnostics || !Array.isArray(point.diagnostics.records)
          || !Number.isSafeInteger(point.diagnostics.after) || point.diagnostics.after < 0
          || point.diagnostics.through !== point.revisions.diagnostics || point.diagnostics.after > point.diagnostics.through
          || point.diagnostics.records.length !== point.diagnostics.through - point.diagnostics.after)) throw new Error('Invalid diagnostic update')
        // An unchanged revision can reuse its bounded payload when a view is
        // opened later. Never reuse a payload across a missed change/epoch.
        if (latest?.point.epoch === point.epoch) {
          stateTopics.forEach(topic => {
            const key = snapshotKeys[topic]
            if (point[key] === undefined && latest!.point.revisions[topic] === point.revisions[topic]) Object.assign(point, { [key]: latest!.point[key] })
          })
        }
        latest = { point, signal, attempt }
        await Promise.all([...listeners].map(listener => deliver(listener, latest!)))
        if (current()) options.status({ phase: 'ready' })
      },
    })
  }

  return {
    subscribe,
    snapshot: (): Readonly<WorkspaceSnapshots> | undefined => latest && !latest.signal.aborted ? latest.point : undefined,
    start(value = false) { metrics = value; stopped = false; connect() },
    setMetrics(value: boolean) { if (metrics === value) return false; metrics = value; if (!stopped) { connect(); return true }; return false },
    reconnect() { if (!stopped) connect() },
    stop() { stopped = true; connection?.abort(); latest = undefined; options.status({ phase: 'stopped' }) },
  }
}
