import type { StreamEvent, SyncCheckpoint } from './types.ts'

const maxFrameBytes = 128_000

export type EventStreamOptions = {
  signal: AbortSignal
  after: () => number
  onEvent: (event: StreamEvent) => void | Promise<void>
  onCheckpoint?: (checkpoint: SyncCheckpoint) => void | Promise<void>
  beforeConnect?: () => Promise<void>
  onActivity?: () => void
  onDisconnect?: () => void
  onError?: (error: unknown, permanent?: boolean) => void
  reconnectDelayMs?: number
  idleTimeoutMs?: number
  connectTimeoutMs?: number
}

// Decode complete lines so UTF-8 characters may span any number of chunks.
// Buffering is bounded by a single frame, including comments and field names.
export async function* parseSSE(body: ReadableStream<Uint8Array>, signal: AbortSignal, onActivity?: () => void): AsyncGenerator<string> {
  for await (const frame of parseFrames(body, signal, onActivity)) yield frame.data
}

async function* parseFrames(body: ReadableStream<Uint8Array>, signal: AbortSignal, onActivity?: () => void): AsyncGenerator<{ event: string; data: string }> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let parts: Uint8Array[] = []
  let lineBytes = 0
  let frameBytes = 0
  let data: string[] = []
  let eventName = ''
  let complete = false
  const cancel = () => { void reader.cancel().catch(() => {}) }
  signal.addEventListener('abort', cancel, { once: true })
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read()
      if (signal.aborted) return
      if (done) { complete = true; return }
      if (value.length) onActivity?.()
      let start = 0
      for (let i = 0; i < value.length; i++) {
        frameBytes++
        if (frameBytes > maxFrameBytes) throw new Error('event frame too large')
        if (value[i] !== 10) continue
        parts.push(value.subarray(start, i))
        lineBytes += i - start
        const bytes = new Uint8Array(lineBytes)
        let offset = 0
        for (const part of parts) { bytes.set(part, offset); offset += part.length }
        const line = decoder.decode(bytes).replace(/\r$/, '')
        parts = []
        lineBytes = 0
        start = i + 1
        if (!line) {
          const payload = data.length ? { event: eventName, data: data.join('\n') } : null
          data = []
          eventName = ''
          frameBytes = 0
          if (payload !== null && !signal.aborted) yield payload
          if (signal.aborted) return
        } else if (line === 'data' || line.startsWith('data:')) {
          data.push(line === 'data' ? '' : line.slice(5).replace(/^ /, ''))
        } else if (line.startsWith('event:')) {
          eventName = line.slice(6).trim()
        }
      }
      if (start < value.length) {
        parts.push(value.subarray(start))
        lineBytes += value.length - start
      }
    }
  } finally {
    signal.removeEventListener('abort', cancel)
    if (!complete) await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}

function reconnectDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise(resolve => {
    const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve() }
    const timer = setTimeout(finish, milliseconds)
    signal.addEventListener('abort', finish, { once: true })
  })
}

export type SSEFrame = { event: string; data: string }
export type SSEOptions = Omit<EventStreamOptions, 'after' | 'onEvent' | 'onCheckpoint'> & {
  onFrame: (frame: SSEFrame, signal: AbortSignal) => void | Promise<void>
  onConnect?: () => void
}

// One transport owns cancellation, liveness and reconnect for both durable
// conversation records and current-state notifications.
export async function streamSSE(
  openStream: (signal: AbortSignal) => Promise<Response>,
  options: SSEOptions,
): Promise<void> {
  const { signal, onError } = options
  let attempts = 0
  while (!signal.aborted) {
    let permanentFailure = false
    const attempt = new AbortController()
    const abort = () => attempt.abort(signal.reason)
    signal.addEventListener('abort', abort, { once: true })
    let timer: ReturnType<typeof setTimeout> | undefined
    const arm = (ms: number) => {
      clearTimeout(timer)
      timer = setTimeout(() => attempt.abort(new Error('Event connection timed out')), ms)
    }
    try {
      await options.beforeConnect?.()
      if (signal.aborted) return
      arm(options.connectTimeoutMs ?? 10_000)
      const response = await openStream(attempt.signal)
      if (signal.aborted) { await response.body?.cancel(); return }
      if (!response.ok || !response.body || (response.headers.get('Content-Type') || '').toLowerCase().split(';')[0]?.trim() !== 'text/event-stream') {
        permanentFailure = response.status >= 400 && response.status < 500 && response.status !== 408 && response.status !== 429
        await response.body?.cancel()
        throw new Error(`event stream: HTTP ${response.status} (${response.headers.get('Content-Type') || 'missing Content-Type'})`)
      }
      options.onConnect?.()
      const activity = () => { arm(options.idleTimeoutMs ?? 45_000); options.onActivity?.() }
      activity()
      for await (const frame of parseFrames(response.body, attempt.signal, activity)) {
        if (signal.aborted) return
        await options.onFrame(frame, attempt.signal)
        attempts = 0
      }
      if (!signal.aborted && attempt.signal.aborted) throw attempt.signal.reason
      if (!signal.aborted) options.onDisconnect?.()
    } catch (error) {
      if (error && typeof error === 'object' && 'status' in error) {
        const status = Number(error.status)
        permanentFailure ||= status >= 400 && status < 500 && status !== 408 && status !== 429
      }
      if (!signal.aborted) onError?.(error, permanentFailure)
    } finally {
      clearTimeout(timer)
      signal.removeEventListener('abort', abort)
      attempt.abort()
    }
    if (permanentFailure || signal.aborted) return
    await reconnectDelay(options.reconnectDelayMs ?? [1000, 2000, 4000, 8000, 15_000, 30_000][Math.min(attempts++, 5)]!, signal)
  }
}

export async function streamSessionEvents(
  openStream: (after: number, signal: AbortSignal) => Promise<Response>,
  options: EventStreamOptions,
): Promise<void> {
  let cursor = options.after()
  return streamSSE(signal => { cursor = options.after(); return openStream(cursor, signal) }, {
    ...options,
    async onFrame(frame) {
      if (frame.event === 'sync') {
        const point: SyncCheckpoint = JSON.parse(frame.data)
        if (!Number.isSafeInteger(point.throughSequence) || point.throughSequence < 0 || !point.session?.id) throw new Error('invalid synchronization checkpoint')
        await options.onCheckpoint?.(point)
        return
      }
      if (frame.event && frame.event !== 'record') return
      const event: StreamEvent & { afterSequence?: number } = JSON.parse(frame.data)
      if (!Number.isSafeInteger(event.sequence) || event.sequence < 0 || typeof event.kind !== 'string') throw new Error('invalid event stream frame')
      cursor = Math.max(cursor, options.after())
      if (event.sequence <= cursor) return
      if (event.afterSequence !== undefined && event.afterSequence !== cursor) throw new Error('Conversation event coverage does not match cursor')
      await options.onEvent(event)
      cursor = event.sequence
    },
  })
}
