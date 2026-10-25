import assert from 'node:assert/strict'
import test from 'node:test'
import { createWorkspaceSync, stateTopics, type StateConnection, type WorkspaceSnapshots } from './workspaceSync.ts'
import type { ServerStatus } from './types.ts'

const encoder = new TextEncoder()
function fixture() {
  const sinks: ReadableStreamDefaultController<Uint8Array>[] = []
  const connections: StateConnection[] = []
  const metrics: ServerStatus[] = []
  const flags: boolean[] = []
  const sync = createWorkspaceSync({
    async open(debug, signal) {
      flags.push(debug)
      const body = new ReadableStream<Uint8Array>({ start(sink) { sinks.push(sink); signal.addEventListener('abort', () => { try { sink.close() } catch {} }, { once: true }) } })
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    },
    status: value => connections.push(value), metrics: value => metrics.push(value), reconnectDelayMs: 1,
  })
  const point = (changes: Record<string, number> = {}, epoch = 'server-a', index = sinks.length - 1, snapshots: WorkspaceSnapshots = {}) => {
    const revisions = Object.fromEntries(stateTopics.map(topic => [topic, changes[topic] ?? 0]))
    sinks[index]!.enqueue(encoder.encode(`event: state\ndata: ${JSON.stringify({ epoch, revisions, ...snapshots })}\n\n`))
  }
  return { sync, sinks, connections, metrics, flags, point }
}
async function until(condition: () => boolean) {
  const deadline = Date.now() + 1000
  while (!condition()) { if (Date.now() > deadline) assert.fail('condition timed out'); await new Promise(resolve => setTimeout(resolve, 1)) }
}

test('one state stream refreshes changed subscriptions and unchanged heartbeats make no reads', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  const reads: string[][] = []
  f.sync.subscribe(['sessions', 'models'], async topics => { reads.push(topics) })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => reads.length === 1)
  assert.deepEqual(reads, [['sessions', 'models']])
  f.point(); f.point(); f.point({ models: 2 })
  await until(() => reads.length === 2)
  assert.deepEqual(reads[1], ['models'])
  assert.equal(f.sinks.length, 1)
})

test('reconnection and a new server epoch reconcile subscribed state even at unchanged revisions', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  let reads = 0
  f.sync.subscribe(['sessions'], async () => { reads++ })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => reads === 1)
  f.sync.reconnect(); await until(() => f.sinks.length === 2)
  f.point(); await until(() => reads === 2)
  f.point({}, 'server-b'); await until(() => reads === 3)
})

test('a failed refresh is retried through stream recovery without accepting its checkpoint', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  let reads = 0
  f.sync.subscribe(['catalog'], async () => { if (++reads === 1) throw new Error('temporary read failure') })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => f.sinks.length === 2)
  assert.ok(f.connections.some(value => value.phase === 'error'))
  f.point(); await until(() => f.connections.at(-1)?.phase === 'ready')
  assert.equal(reads, 2)
})

test('metrics use the shared stream and enabling debug replaces its single connection', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  let reads = 0
  f.sync.subscribe(['status'], async () => { reads++ })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => reads === 1)
  f.sync.setMetrics(true); await until(() => f.sinks.length === 2)
  const value = { state: 'running', sessions: {}, workerMetrics: [], sampledAt: '2026-10-25T00:00:00Z' } as unknown as ServerStatus
  f.sinks[1]!.enqueue(encoder.encode(`event: metrics\ndata: ${JSON.stringify(value)}\n\n`))
  await until(() => f.metrics.length === 1)
  assert.deepEqual(f.flags, [false, true])
  assert.equal(reads, 1)
})

test('unsubscribing cancels a pending component read without breaking other subscriptions', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  let reading = false, canceled = false
  const stop = f.sync.subscribe(['storage'], async (_, signal) => {
    reading = true
    await new Promise<void>((_, reject) => signal.addEventListener('abort', () => { canceled = true; reject(signal.reason) }, { once: true }))
  })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => reading)
  stop(); await until(() => f.connections.at(-1)?.phase === 'ready')
  assert.equal(canceled, true)
  assert.equal(f.sinks.length, 1)
})

test('pushed snapshots replace changed metadata reads while unchanged heartbeats do not reapply them', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  const reads: string[] = [], applied: WorkspaceSnapshots[] = []
  f.sync.subscribe(['sessions', 'status'], async (topics, _, snapshots) => {
    for (const topic of topics) if (!snapshots[topic as keyof WorkspaceSnapshots]) reads.push(topic)
    applied.push(snapshots)
  })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => applied.length === 1)
  assert.deepEqual(reads, ['sessions', 'status'])
  const pushed: WorkspaceSnapshots = { sessions: { items: [], throughSequences: {} },
    status: { state: 'running', sessions: {}, workerMetrics: [], sampledAt: '2026-10-25T00:00:00Z' } as unknown as ServerStatus }
  f.point({ sessions: 1, status: 1 }, 'server-a', 0, pushed)
  await until(() => applied.length === 2)
  const settled = f.connections.length
  f.point({ sessions: 1, status: 1 })
  f.point({ sessions: 1, status: 1, models: 1 })
  await until(() => f.connections.length === settled + 2)
  assert.equal(applied.length, 2)
  assert.deepEqual(applied[1].sessions, pushed.sessions)
  assert.deepEqual(applied[1].status, pushed.status)
  assert.deepEqual(reads, ['sessions', 'status'])
})

test('a heartbeat covering a missed payload requests only the affected snapshot', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  const reads: string[][] = []
  f.sync.subscribe(['sessions', 'status'], async (topics, _, snapshots) => {
    reads.push(topics.filter(topic => !snapshots[topic as keyof WorkspaceSnapshots]))
  })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => reads.length === 1)
  // The server already sent the payload, but delivery lost it. Its next
  // revision-only heartbeat must still repair the browser's unchanged cursor.
  f.point({ sessions: 2 }); await until(() => reads.length === 2)
  assert.deepEqual(reads, [['sessions', 'status'], ['sessions']])
  assert.equal(f.sinks.length, 1)
})

test('invalid pushed data cannot accept a checkpoint and reconnects for snapshot recovery', async t => {
  for (const snapshots of [
    { sessions: { items: [{ id: 's', title: 'Missing boundary' }], throughSequences: {} } },
    { status: { state: 'running', sessions: {}, workerMetrics: [], sampledAt: 'invalid' } },
  ]) {
    const f = fixture(); t.after(() => f.sync.stop())
    let reads = 0
    f.sync.subscribe(['sessions', 'status'], async () => { reads++ })
    f.sync.start(); await until(() => f.sinks.length === 1)
    f.point(); await until(() => reads === 1)
    f.point({ sessions: 1, status: 1 }, 'server-a', 0, snapshots as WorkspaceSnapshots)
    await until(() => f.sinks.length === 2)
    assert.equal(reads, 1)
    assert.ok(f.connections.some(value => value.phase === 'error'))
    f.point({ sessions: 1, status: 1 }); await until(() => reads === 2)
    assert.equal(f.connections.at(-1)?.phase, 'ready')
    f.sync.stop()
  }
})

test('all small settings payloads are delivered directly and replayed to a newly opened view', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => f.connections.at(-1)?.phase === 'ready')
  const snapshots: WorkspaceSnapshots = {
    models: { default: '', models: [] }, catalog: { revision: 'catalog', modelCount: 1, providerCount: 1 },
    providerLogins: [{ id: 'login', status: 'connected' }],
    mcp: { servers: [], maxServers: 4, defaultDirectory: '/workspace', logins: [] },
    storage: { phase: 'idle', deletedBytes: 2, deletedCount: 1 },
    memories: { memories: [], maxCount: 128, maxBytes: 4096, maxTotalBytes: 32768 },
  }
  const revisions = Object.fromEntries(['models', 'catalog', 'provider-login', 'mcp', 'storage', 'memories'].map(topic => [topic, 1]))
  f.point(revisions, 'server-a', 0, snapshots)
  await until(() => f.sync.snapshot()?.catalog?.revision === 'catalog')
  const delivered: WorkspaceSnapshots[] = []
  f.sync.subscribe(['models', 'catalog', 'provider-login', 'mcp', 'storage', 'memories'], async (_, __, value) => { delivered.push(value) })
  await until(() => delivered.length === 1)
  for (const key of Object.keys(snapshots)) assert.deepEqual(delivered[0]![key as keyof WorkspaceSnapshots], snapshots[key as keyof WorkspaceSnapshots])
  f.point(revisions)
  await until(() => f.connections.at(-1)?.phase === 'ready')
  assert.equal(delivered.length, 1)
  assert.equal(f.sinks.length, 1)
})

test('a new subscriber serializes its initial read with arriving checkpoints', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => f.connections.at(-1)?.phase === 'ready')
  let reads = 0, release: (() => void) | undefined
  f.sync.subscribe(['memories'], async () => {
    reads++
    if (reads === 1) await new Promise<void>(resolve => { release = resolve })
  })
  await until(() => !!release)
  f.point(); f.point({ memories: 1 })
  release!()
  await until(() => reads === 2)
  assert.equal(f.sinks.length, 1)
})

test('a missing changed payload invalidates the cached value before a view reopens', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point({ models: 1 }, 'server-a', 0, { models: { default: 'old', models: [] } })
  await until(() => f.sync.snapshot()?.models?.default === 'old')
  f.point({ models: 2 })
  await until(() => f.sync.snapshot()?.models === undefined)
  let initialReads = 0
  f.sync.subscribe(['models'], async (_, __, snapshot) => { if (!snapshot.models) initialReads++ })
  await until(() => initialReads === 1)
  assert.equal(f.sinks.length, 1)
})

test('diagnostic patches carry their revision coverage and unchanged heartbeats do not repeat them', async t => {
  const f = fixture(); t.after(() => f.sync.stop())
  const applied: WorkspaceSnapshots[] = []
  const revisions: number[] = []
  f.sync.subscribe(['diagnostics'], async (_, __, snapshot, revision) => { applied.push(snapshot); revisions.push(revision.diagnostics) })
  f.sync.start(); await until(() => f.sinks.length === 1)
  f.point(); await until(() => applied.length === 1)
  const record = { id: 'call', sessionId: 's', runId: 'r', provider: 'mock', protocol: 'chat', model: 'fixture', startedAt: '2026-10-25T00:00:00Z', durationMs: 1, truncated: false }
  f.point({ diagnostics: 1 }, 'server-a', 0, { diagnostics: { after: 0, through: 1, records: [record] } })
  await until(() => applied.length === 2)
  f.point({ diagnostics: 1 })
  await until(() => f.connections.at(-1)?.phase === 'ready')
  assert.equal(applied.length, 2)
  assert.deepEqual(applied[1]!.diagnostics?.records, [record])
  assert.deepEqual(revisions, [0, 1])
})
