import { parseSSE, streamSessionEvents, type EventStreamOptions } from './sse.js'

export type VistaClientOptions = {
  /** Service URL (without /api); omit for same-origin WebUI requests. */
  baseURL?: string
  fetch?: typeof fetch
  headers?: HeadersInit
  credentials?: RequestCredentials
  language?: string | (() => string)
}

export type VistaClient = {
  url: (path: string) => string
  openWorkspaceEvents: (metrics: boolean, signal: AbortSignal) => Promise<Response>
  request: <T = unknown>(path: string, options?: RequestInit) => Promise<T>
  requestStream: <T = unknown>(path: string, options: RequestInit & { signal: AbortSignal }) => AsyncIterable<T>
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

function apiBase(baseURL: string | undefined): string {
  if (baseURL === undefined) return '/api'
  const url = new URL(baseURL)
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new TypeError('baseURL must be a Vista HTTP(S) service URL without credentials, query, or fragment')
  }
  return url.origin + url.pathname.replace(/\/+$/, '') + '/api'
}

export function createVistaClient(options: VistaClientOptions = {}): VistaClient {
  const base = apiBase(options.baseURL)
  const { fetch: fetchImpl = fetch, credentials, language } = options
  const defaults = new Headers(options.headers)
  const url = (path: string) => {
    if (!path.startsWith('/') || path.startsWith('//')) throw new TypeError('Expected an API-relative path starting with /')
    return base + path
  }
  const send = (path: string, init: RequestInit = {}): Promise<Response> => {
    const headers = new Headers(defaults)
    new Headers(init.headers).forEach((value, name) => headers.set(name, value))
    if (!headers.has('X-Vista-Language')) {
      const value = typeof language === 'function' ? language() : language
      if (value) headers.set('X-Vista-Language', value)
    }
    if (!headers.has('Content-Type') && typeof init.body === 'string') headers.set('Content-Type', 'application/json')
    return fetchImpl(url(path), { credentials, ...init, headers })
  }
  const events = (path: string, signal: AbortSignal) => send(path, { signal, headers: { Accept: 'text/event-stream' } })
  const response = async (path: string, init?: RequestInit): Promise<Response> => {
    const result = await send(path, init)
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
    url,
    openWorkspaceEvents: (metrics, signal) => events(`/events${metrics ? '?metrics=1' : ''}`, signal),
    async request<T>(path: string, options: RequestInit = {}): Promise<T> {
      const result = await response(path, options)
      const text = await result.text()
      return (text ? JSON.parse(text) : null) as T
    },
    async *requestStream<T>(path: string, options: RequestInit & { signal: AbortSignal }): AsyncGenerator<T> {
      const headers = new Headers(options.headers)
      headers.set('Accept', 'text/event-stream')
      const result = await response(path, { ...options, headers })
      if (!result.body || result.headers.get('Content-Type')?.split(';')[0]?.trim().toLowerCase() !== 'text/event-stream') {
        await result.body?.cancel()
        throw new Error('invalid event stream response')
      }
      for await (const frame of parseSSE(result.body, options.signal)) yield JSON.parse(frame) as T
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
      return streamSessionEvents((after, signal) => events(`/sessions/${encodeURIComponent(sessionId)}/events?after=${after}`, signal), options)
    },
  }
}
