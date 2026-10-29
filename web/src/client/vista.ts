import { streamSessionEvents, type EventStreamOptions } from './sse.ts'

export type VistaClient = {
  request: <T = unknown>(path: string, options?: RequestInit) => Promise<T>
  image: (path: string, signal?: AbortSignal) => Promise<Blob>
  streamEvents: (sessionId: string, options: EventStreamOptions) => Promise<void>
}

export class VistaAPIError extends Error {
  readonly status: number
  readonly code: string
  readonly details: Record<string, unknown> | undefined

  constructor(message: string, status: number, code = '', details?: Record<string, unknown>) {
    super(message)
    this.name = 'VistaAPIError'
    this.status = status
    this.code = code
    this.details = details
  }
}

export function createVistaClient(fetchImpl: typeof fetch = fetch): VistaClient {
  const response = async (path: string, options: RequestInit = {}): Promise<Response> => {
    const headers = new Headers(options.headers)
    if (typeof document !== 'undefined' && document.documentElement.lang) headers.set('X-Vista-Language', document.documentElement.lang)
    if (!headers.has('Content-Type') && typeof options.body === 'string') headers.set('Content-Type', 'application/json')
    const result = await fetchImpl('/api' + path, { ...options, headers })
    if (!result.ok) {
      const body = await result.text()
      let parsed: Record<string, unknown> | undefined
      try {
        const value: unknown = JSON.parse(body)
        if (value && typeof value === 'object' && !Array.isArray(value)) parsed = value as Record<string, unknown>
      } catch { /* Plain HTTP errors remain supported. */ }
      const details = parsed?.details && typeof parsed.details === 'object' && !Array.isArray(parsed.details)
        ? parsed.details as Record<string, unknown> : undefined
      throw new VistaAPIError(typeof parsed?.message === 'string' ? parsed.message : body || result.statusText,
        result.status, typeof parsed?.code === 'string' ? parsed.code : '', details)
    }
    return result
  }
  return {
    async request<T>(path: string, options: RequestInit = {}): Promise<T> {
      const result = await response(path, options)
      const text = await result.text()
      return (text ? JSON.parse(text) : null) as T
    },
    async image(path: string, signal?: AbortSignal): Promise<Blob> {
      const result = await response(path, { signal })
      if (!(result.headers.get('Content-Type') || '').toLowerCase().startsWith('image/')) {
        await result.body?.cancel()
        throw new Error('invalid image response')
      }
      if (Number(result.headers.get('Content-Length')) > 50 * 1_048_576) {
        await result.body?.cancel()
        throw new Error('image response exceeds 50 MiB')
      }
      const blob = await result.blob()
      if (blob.size > 50 * 1_048_576) throw new Error('image response exceeds 50 MiB')
      return blob
    },
    streamEvents(sessionId, options) {
      return streamSessionEvents((after, signal) => fetchImpl(`/api/sessions/${encodeURIComponent(sessionId)}/events?after=${after}`, {
        signal,
      }), options)
    },
  }
}
