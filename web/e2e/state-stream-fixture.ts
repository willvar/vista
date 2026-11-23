import type { Page } from '@playwright/test'
import { stateTopics, type StateTopic, type WorkspaceSnapshots } from '@vista/client'

// Mock-only UI suites still need a persistent connection. Explicit revision
// notifications replace fixtures that used to change state on the next poll.
export async function installMockStateStream(page: Page) {
  await page.addInitScript(topics => {
    const original = window.fetch.bind(window)
    const encoder = new TextEncoder()
    const revisions = Object.fromEntries(topics.map(topic => [topic, 0]))
    const sessionVersion = () => `mock-state:${revisions.sessions}`
    const clients = new Set<ReadableStreamDefaultController<Uint8Array>>()
    const frame = (snapshots: Record<string, unknown> = {}) => encoder.encode(`event: state\ndata: ${JSON.stringify({ epoch: 'mock-state', revisions, ...snapshots })}\n\n`)
    const notify = (changed: string[], snapshots: WorkspaceSnapshots = {}) => {
      for (const topic of changed) revisions[topic]!++
      const window = snapshots.sessionWindow
      for (const sink of clients) sink.enqueue(frame(window ? { ...snapshots, sessionWindow: { ...window, version: sessionVersion() } } : snapshots))
    }
    Object.assign(window, { notifyStateFixture: notify })
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href)
      const path = url.pathname
      if (path !== '/api/events') {
        const version = sessionVersion()
        const response = await original(input, init)
        const method = init?.method || (input instanceof Request ? input.method : 'GET')
        if (response.ok && !['GET', 'HEAD'].includes(method.toUpperCase())) {
          // Mirror the backend's post-commit settings notifications, including
          // fixtures that intercept a mutation instead of reaching the server.
          if (['/api/provider-config', '/api/model-config'].includes(path)) setTimeout(() => notify(['models', 'status']), 20)
          else if (path === '/api/mcp-config' || /^\/api\/mcp-servers\/[^/]+\/(login|check)$/.test(path)) setTimeout(() => notify(['mcp']), 20)
        }
        // Page reads must use the same epoch as the substituted state stream.
        if (response.ok && method.toUpperCase() === 'GET' && path === '/api/sessions' && url.searchParams.has('page')) {
          return new Response(JSON.stringify({ ...await response.json(), version }), { headers: { 'Content-Type': 'application/json' } })
        }
        return response
      }
      if (init?.signal?.aborted) throw init.signal.reason
      let timer: ReturnType<typeof setInterval> | undefined
      let close: () => void
      const body = new ReadableStream<Uint8Array>({
        start(sink) {
          clients.add(sink)
          sink.enqueue(frame())
          timer = setInterval(() => sink.enqueue(frame()), 15_000)
          close = () => { clearInterval(timer); clients.delete(sink); try { sink.close() } catch {} }
          init?.signal?.addEventListener('abort', close, { once: true })
        },
        cancel() { close(); init?.signal?.removeEventListener('abort', close) },
      })
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    }
  }, [...stateTopics])
}

export async function notifyState(page: Page, topics: StateTopic[], snapshots: WorkspaceSnapshots = {}) {
  await page.evaluate(({ topics, snapshots }) => (window as unknown as { notifyStateFixture: (topics: string[], snapshots: WorkspaceSnapshots) => void }).notifyStateFixture(topics, snapshots), { topics, snapshots })
}
