import assert from 'node:assert/strict'
import test from 'node:test'
import { parseSSE, streamSessionEvents } from './sse.ts'
import { createVistaClient } from './vista.ts'

const encoder = new TextEncoder()
const event = (sequence: number, content = '') => `data: ${JSON.stringify({ sequence, kind: 'model_delta', data: { content } })}\n\n`

function sseResponse(body?: BodyInit | null, init?: ResponseInit) {
  return new Response(body, { ...init, headers: { 'Content-Type': 'text/event-stream', ...init?.headers } })
}

function body(...chunks: Uint8Array[]): ReadableStream<Uint8Array> {
  return new ReadableStream({ start(controller) { for (const chunk of chunks) controller.enqueue(chunk); controller.close() } })
}

test('SSE decodes split UTF-8, CRLF, multiple data lines, comments and adjacent frames', async () => {
  const input = ': keepalive\r\ndata: {"text":\r\ndata: "你好🙂"}\r\n\r\nevent: message\ndata: second\n\n'
  const bytes = encoder.encode(input)
  // Every byte boundary is exercised, including inside all Chinese/emoji bytes
  // and between the CR and LF of a line ending.
  const chunks = Array.from(bytes, byte => Uint8Array.of(byte))
  const frames: string[] = []
  for await (const payload of parseSSE(body(...chunks), new AbortController().signal)) frames.push(payload)
  assert.deepEqual(frames, ['{"text":\n"你好🙂"}', 'second'])
  assert.deepEqual(JSON.parse(frames[0]), { text: '你好🙂' })
})

test('reconnection uses the current cursor and ignores repeated events', async () => {
  const controller = new AbortController()
  const cursors: number[] = []
  const received: number[] = []
  let cursor = 0
  const client = createVistaClient(async (url, options) => {
    assert.equal(new Headers(options?.headers).get('Authorization'), null)
    const after = Number(String(url).split('after=')[1])
    cursors.push(after)
    const frames = cursors.length === 1 ? event(1) + event(1) : event(3) + event(4) + event(5)
    return sseResponse(body(encoder.encode(frames)))
  })
  await client.streamEvents('session-id', {
    signal: controller.signal, after: () => cursor, reconnectDelayMs: 1,
    onEvent(item) {
      received.push(item.sequence)
      if (item.sequence === 1) cursor = 3 // A concurrently loaded history page advanced the cursor.
      if (item.sequence === 4) controller.abort()
    },
    onError(error) { throw error },
  })
  assert.deepEqual(cursors, [0, 3])
  assert.deepEqual(received, [1, 4])
})

test('abort cancels a pending read, releases its lock and suppresses callbacks', async () => {
  const controller = new AbortController()
  let cancelled = false
  let callbackCount = 0
  const stream = new ReadableStream<Uint8Array>({
    start(sink) { sink.enqueue(encoder.encode('data: {"sequence":1')) },
    cancel() { cancelled = true },
  })
  const pending = streamSessionEvents(async () => sseResponse(stream), {
    signal: controller.signal, after: () => 0,
    onEvent() { callbackCount++ }, onError() { callbackCount++ },
  })
  await new Promise(resolve => setImmediate(resolve))
  controller.abort()
  await pending
  assert.equal(cancelled, true)
  assert.equal(stream.locked, false)
  assert.equal(callbackCount, 0)
})

test('abort interrupts the reconnection delay without another request', async () => {
  const controller = new AbortController()
  let calls = 0
  let reportError: (() => void) | undefined
  const reported = new Promise<void>(resolve => { reportError = resolve })
  const pending = streamSessionEvents(async () => { calls++; throw new Error('offline') }, {
    signal: controller.signal, after: () => 0, reconnectDelayMs: 60_000,
    onEvent() { assert.fail('no events expected') }, onError() { reportError?.() },
  })
  await reported
  await new Promise(resolve => setImmediate(resolve))
  controller.abort()
  await pending
  assert.equal(calls, 1)
})

test('authorization or deleted-session failures report once and stop reconnecting', async () => {
  for (const status of [401, 403, 404]) {
    let calls = 0
    const errors: unknown[] = []
    await streamSessionEvents(async () => { calls++; return sseResponse('denied', { status }) }, {
      signal: new AbortController().signal, after: () => 0,
      onEvent() { assert.fail('no events expected') }, onError(error) { errors.push(error) },
    })
    assert.equal(calls, 1)
    assert.equal(errors.length, 1)
    assert.match(String(errors[0]), new RegExp(`HTTP ${status}`))
  }
})

test('oversized incomplete frames are rejected and the reader is cancelled', async () => {
  let cancelled = false
  const stream = new ReadableStream<Uint8Array>({
    start(sink) { sink.enqueue(encoder.encode('data: ' + 'x'.repeat(128_000))) },
    cancel() { cancelled = true },
  })
  await assert.rejects(async () => {
    for await (const _ of parseSSE(stream, new AbortController().signal)) assert.fail('no complete frame expected')
  }, /event frame too large/)
  assert.equal(cancelled, true)
  assert.equal(stream.locked, false)
})

test('sync frames are separate from records and cannot advance the replay cursor', async () => {
  const controller = new AbortController()
  let cursor = 0
  const received: number[] = [], checkpoints: number[] = []
  const frames = 'event: sync\ndata: {"throughSequence":9,"session":{"id":"s"}}\n\n'
    + 'event: future-control\ndata: {}\n\n' + event(1)
  await streamSessionEvents(async () => sseResponse(body(encoder.encode(frames))), {
    signal: controller.signal, after: () => cursor,
    onCheckpoint(point) { checkpoints.push(point.throughSequence); assert.equal(cursor, 0) },
    onEvent(item) { received.push(item.sequence); cursor = item.sequence; controller.abort() },
  })
  assert.deepEqual(checkpoints, [9])
  assert.deepEqual(received, [1])
})

test('an application exception reconnects from the last successfully applied record', async () => {
  const controller = new AbortController()
  let cursor = 0, fail = true
  const cursors: number[] = []
  await streamSessionEvents(async after => {
    cursors.push(after)
    return sseResponse(body(encoder.encode(event(1) + event(2))))
  }, {
    signal: controller.signal, after: () => cursor, reconnectDelayMs: 1,
    onEvent(item) {
      if (item.sequence === 2 && fail) { fail = false; throw new Error('apply failed') }
      cursor = item.sequence
      if (cursor === 2) controller.abort()
    },
  })
  assert.deepEqual(cursors, [0, 1])
})

test('a connected but silent stream is cancelled and reports a recoverable inactivity error', async () => {
  const controller = new AbortController()
  let cancelled = false
  const errors: unknown[] = []
  const stream = new ReadableStream<Uint8Array>({ cancel() { cancelled = true } })
  await streamSessionEvents(async () => sseResponse(stream), {
    signal: controller.signal, after: () => 0, idleTimeoutMs: 10,
    onEvent() { assert.fail('silent stream') },
    onError(error, permanent) { errors.push(error); assert.equal(permanent, false); controller.abort() },
  })
  assert.equal(errors.length, 1)
  assert.match(String(errors[0]), /timed out/)
  assert.equal(cancelled, true)
  assert.equal(stream.locked, false)
})

test('comments and partial frames count as activity during model silence', async () => {
  const controller = new AbortController()
  let timer: ReturnType<typeof setInterval> | undefined
  let ticks = 0
  const stream = new ReadableStream<Uint8Array>({
    start(sink) { timer = setInterval(() => {
      sink.enqueue(encoder.encode(ticks++ === 0 ? ': heartbeat\n\n' : ': partial'))
      if (ticks === 6) controller.abort()
    }, 25) },
    cancel() { clearInterval(timer) },
  })
  let activity = 0
  await streamSessionEvents(async () => sseResponse(stream), {
    signal: controller.signal, after: () => 0, idleTimeoutMs: 100,
    onActivity() { activity++ },
    onEvent() { assert.fail('no model records') }, onError(error) { assert.fail(String(error)) },
  })
  assert.ok(activity >= 6)
})

test('an unresponsive connection attempt is bounded and cleans up its timer', async () => {
  const controller = new AbortController()
  let requests = 0
  await streamSessionEvents(async (_after, signal) => {
    requests++
    return new Promise<Response>((_resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }))
  }, {
    signal: controller.signal, after: () => 0, connectTimeoutMs: 10,
    onEvent() { assert.fail('no stream') }, onError(error) { assert.match(String(error), /timed out/); controller.abort() },
  })
  assert.equal(requests, 1)
})

test('coverage detects a missing intermediate record without requiring consecutive sequences', async () => {
  for (const missed of [false, true]) {
    const controller = new AbortController()
    let cursor = 3, errors = 0
    const frame = { sequence: 9, afterSequence: missed ? 7 : 3, kind: 'message', data: {} }
    await streamSessionEvents(async () => sseResponse(body(encoder.encode(`event: record\ndata: ${JSON.stringify(frame)}\n\n`))), {
      signal: controller.signal, after: () => cursor,
      onEvent(item) { assert.equal(missed, false); cursor = item.sequence; controller.abort() },
      onError(error) { errors++; assert.match(String(error), /coverage/); controller.abort() },
    })
    assert.equal(cursor, missed ? 3 : 9)
    assert.equal(errors, missed ? 1 : 0)
  }
})
