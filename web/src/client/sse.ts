import type { StreamEvent } from './types.ts'

const maxFrameBytes = 128_000

export type EventStreamOptions = {
  signal: AbortSignal
  after: () => number
  onEvent: (event: StreamEvent) => void
  onError?: (error: unknown) => void
  reconnectDelayMs?: number
}

// Decode complete lines so UTF-8 characters may span any number of chunks.
// Buffering is bounded by a single frame, including comments and field names.
export async function* parseSSE(body: ReadableStream<Uint8Array>, signal: AbortSignal): AsyncGenerator<string> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let parts: Uint8Array[] = []
  let lineBytes = 0
  let frameBytes = 0
  let data: string[] = []
  let complete = false
  const cancel = () => { void reader.cancel().catch(() => {}) }
  signal.addEventListener('abort', cancel, { once: true })
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read()
      if (signal.aborted) return
      if (done) { complete = true; return }
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
          const payload = data.length ? data.join('\n') : null
          data = []
          frameBytes = 0
          if (payload !== null && !signal.aborted) yield payload
          if (signal.aborted) return
        } else if (line === 'data' || line.startsWith('data:')) {
          data.push(line === 'data' ? '' : line.slice(5).replace(/^ /, ''))
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

export async function streamSessionEvents(
  openStream: (after: number, signal: AbortSignal) => Promise<Response>,
  options: EventStreamOptions,
): Promise<void> {
  const { signal, after, onEvent, onError } = options
  let cursor = after()
  while (!signal.aborted) {
    let permanentFailure = false
    try {
      cursor = Math.max(cursor, after())
      const response = await openStream(cursor, signal)
      if (signal.aborted) { await response.body?.cancel(); return }
      if (!response.ok || !response.body) {
        // Retrying cannot repair credentials or a missing/deleted Session.
        permanentFailure = response.status >= 400 && response.status < 500 && response.status !== 408 && response.status !== 429
        await response.body?.cancel()
        throw new Error(`event stream: HTTP ${response.status}`)
      }
      for await (const payload of parseSSE(response.body, signal)) {
        if (signal.aborted) return
        const event: StreamEvent = JSON.parse(payload)
        if (!Number.isSafeInteger(event.sequence) || event.sequence < 0 || typeof event.kind !== 'string') {
          throw new Error('invalid event stream frame')
        }
        cursor = Math.max(cursor, after())
        if (event.sequence <= cursor) continue
        onEvent(event)
        cursor = event.sequence
      }
    } catch (error) {
      if (!signal.aborted) onError?.(error)
    }
    if (permanentFailure || signal.aborted) return
    await reconnectDelay(options.reconnectDelayMs ?? 1000, signal)
  }
}
