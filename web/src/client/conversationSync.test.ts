import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { createConversationSync, type ConversationSyncStatus, type ConversationSyncDiagnostics } from './conversationSync.ts'
import type { EventStreamOptions } from './sse.ts'
import type { ConversationSnapshot, StreamEvent } from './types.ts'
import type { VistaClient } from './vista.ts'
import { VistaAPIError } from './vista.ts'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(yes => { resolve = yes })
  return { promise, resolve }
}
const event = (sequence: number): StreamEvent => ({ sequence, kind: 'message', data: { id: `m${sequence}`, sequence, role: 'assistant' } })
function snapshot(sequence = 1): ConversationSnapshot {
  return { session: { id: 's', title: '', modelKey: '', open: true, running: false, executing: false, createdAt: '', latestReplySequence: sequence },
    throughSequence: sequence, messages: [{ id: `m${sequence}`, sequence, runId: 'r', role: 'assistant', content: 'reply', createdAt: '' }], events: [], hasOlder: false, hasOlderEvents: false }
}
function fixture(t: TestContext, read?: (path: string) => Promise<unknown> | undefined) {
  const controller = new AbortController()
  t.after(() => controller.abort())
  const requests: string[] = [], streams: EventStreamOptions[] = [], applied: number[] = [], rebuilt: number[] = []
  let cursor = 0, reply = 0, failEvent = false
  let status: ConversationSyncStatus = { phase: 'connecting' }
  let diagnostics: ConversationSyncDiagnostics | undefined
  const client: VistaClient = {
    async request<T>(path: string, options?: RequestInit): Promise<T> {
      assert.equal(options?.method, undefined, 'Recovery must only read saved state')
      requests.push(path)
      return (await (read?.(path) ?? Promise.resolve(path.endsWith('/snapshot') ? snapshot() : []))) as T
    },
    async image() { throw new Error('Recovery must not request images') },
    async streamEvents(_id, options) { streams.push(options); await options.beforeConnect?.() },
  }
  const sync = createConversationSync({ client, sessionId: 's', signal: controller.signal,
    cursor: () => cursor, replySequence: () => reply,
    applyEvent(item) { if (failEvent) throw new Error('apply failed'); applied.push(item.sequence); cursor = item.sequence; reply = item.sequence },
    applySnapshot(view) { rebuilt.push(view.throughSequence); cursor = view.throughSequence; reply = view.session.latestReplySequence || 0 },
    status(value) { status = value }, diagnostics(value) { diagnostics = value },
  })
  return { sync, requests, streams, applied, rebuilt, controller, get cursor() { return cursor }, get status() { return status },
    get diagnostics() { return diagnostics! }, fail() { failEvent = true } }
}

test('checkpoint checks progress without advancing the cursor and bounded replay repairs missed replies', async t => {
  const f = fixture(t, path => path.includes('/event-page') ? Promise.resolve([event(1), event(2), event(3)]) : undefined)
  await f.sync.start()
  const stream = f.streams[0]!
  const point = { throughSequence: 3, session: snapshot(3).session }
  assert.throws(() => stream.onCheckpoint!(point), /behind/)
  assert.equal(f.cursor, 1)
  stream.onError!(new Error('behind'))
  await stream.beforeConnect!()
  await stream.onCheckpoint!(point)
  assert.deepEqual(f.applied, [2, 3])
  assert.deepEqual(f.rebuilt, [1])
  assert.equal(f.status.phase, 'ready')
  assert.ok(f.requests.includes('/sessions/s/event-page?after=1&through=3'))
})

test('same event boundary but missing formal reply proof forces a consistent snapshot', async t => {
  let reads = 0
  const f = fixture(t, path => path.endsWith('/snapshot') ? Promise.resolve(++reads === 1 ? { ...snapshot(3), session: snapshot(1).session } : snapshot(3)) : undefined)
  await f.sync.start()
  const stream = f.streams[0]!
  assert.throws(() => stream.onCheckpoint!({ throughSequence: 3, session: snapshot(3).session }), /behind/)
  stream.onError!(new Error('behind'))
  await stream.beforeConnect!()
  assert.deepEqual(f.rebuilt, [3, 3])
  assert.equal(f.cursor, 3)
})

for (const reason of ['replay request fails', 'event application fails', 'history was rolled back', 'replay exceeds its bound']) {
  test(`${reason}: rebuilds the view without submitting a model request`, async t => {
    let reads = 0
    const f = fixture(t, path => {
      if (path.endsWith('/snapshot')) return Promise.resolve(snapshot(++reads === 1 ? 1 : 2000))
      if (reason === 'replay request fails') return Promise.reject(new Error('offline'))
      if (reason === 'history was rolled back') return Promise.resolve([{ sequence: 5, kind: 'conversation_reverted', data: {} }])
      if (reason === 'replay exceeds its bound') {
        const after = Number(new URL(path, 'http://vista').searchParams.get('after'))
        return Promise.resolve(Array.from({ length: 100 }, (_, i) => event(after + i + 1)))
      }
      return Promise.resolve([event(2)])
    })
    await f.sync.start()
    if (reason === 'event application fails') f.fail()
    f.streams[0]!.onError!(new Error('network failure'))
    await f.streams[0]!.beforeConnect!()
    assert.deepEqual(f.rebuilt, [1, 2000])
    assert.equal(f.cursor, 2000)
    assert.ok(f.requests.filter(path => path.includes('/event-page')).length <= 10)
  })
}

test('a live application exception does not become acknowledged progress', async t => {
  const f = fixture(t)
  await f.sync.start()
  f.fail()
  assert.throws(() => f.streams[0]!.onEvent(event(2)), /apply failed/)
  assert.equal(f.cursor, 1)
})

test('failed initialization remains retryable and auxiliary endpoints cannot gate subscription', async t => {
  let reads = 0
  const f = fixture(t, path => path.endsWith('/snapshot') && ++reads === 1 ? Promise.reject(new Error('temporary failure')) : undefined)
  await f.sync.start()
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(f.cursor, 1)
  assert.ok(f.streams.length)
  assert.ok(f.requests.every(path => path.endsWith('/snapshot')))
})

test('authorization failure stops initial retries until a manual recovery', async t => {
  const f = fixture(t, () => Promise.reject(new VistaAPIError('denied', 403)))
  await f.sync.start()
  assert.equal(f.status.phase, 'error')
  assert.equal(f.requests.length, 1)
  assert.equal(f.streams.length, 0)
  f.sync.reconnect('foreground')
  assert.equal(f.streams.length, 0)
})

test('a second invalidation during a repair is serialized and cannot be lost', async t => {
  const pending = deferred<unknown>()
  let reads = 0, concurrent = 0, maximum = 0
  const f = fixture(t, path => {
    if (!path.endsWith('/snapshot')) return
    reads++
    if (reads !== 2) return Promise.resolve(snapshot(reads))
    maximum = Math.max(maximum, ++concurrent)
    return pending.promise.finally(() => { concurrent-- })
  })
  await f.sync.start()
  f.sync.reconnect('manual recovery', true)
  const first = f.streams.at(-1)!.beforeConnect!()
  await new Promise(resolve => setImmediate(resolve))
  f.sync.reconnect('manual recovery', true)
  const second = f.streams.at(-1)!.beforeConnect!()
  pending.resolve(snapshot(2))
  await Promise.all([first, second])
  assert.deepEqual(f.rebuilt, [1, 2, 3])
  assert.equal(maximum, 1)
  assert.equal(f.cursor, 3)
  assert.equal(f.streams[0]!.signal.aborted, true)
})

test('scope disposal rejects late snapshots and stale stream callbacks', async t => {
  const pending = deferred<unknown>()
  let reads = 0
  const f = fixture(t, path => path.endsWith('/snapshot') && ++reads === 2 ? pending.promise : undefined)
  await f.sync.start()
  const stale = f.streams[0]!
  f.sync.reconnect('manual recovery', true)
  const repairing = f.streams.at(-1)!.beforeConnect!()
  f.controller.abort()
  stale.onEvent(event(99))
  stale.onError!(new Error('stale'))
  pending.resolve(snapshot(5))
  await repairing
  assert.deepEqual(f.rebuilt, [1])
  assert.equal(f.cursor, 1)
})

test('diagnostics are bounded and rendering diagnostics do not initiate recovery', async t => {
  const f = fixture(t)
  await f.sync.start()
  const reads = f.requests.length
  for (let i = 0; i < 50; i++) f.sync.diagnostic('render failed', 'x'.repeat(4000))
  assert.equal(f.diagnostics.history.length, 32)
  assert.equal(f.diagnostics.history[0]!.error!.length, 2000)
  assert.equal(f.requests.length, reads)
})
