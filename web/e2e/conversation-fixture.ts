import type { Page } from '@playwright/test'
import type { ConversationSnapshot, Message, Session, StreamEvent } from '../src/client/types'

// A mock core read must describe the same saved state as its message/events
// endpoints. Tests with delayed loads should delay this whole view together.
export function coreSnapshot(session: Session, messages: Message[] = [], events: StreamEvent[] = []): ConversationSnapshot {
  return { session: { ...session, latestReplySequence: Math.max(0, ...messages.filter(message => message.role === 'assistant').map(message => message.sequence)) },
    throughSequence: Math.max(0, ...messages.map(message => message.sequence), ...events.map(event => event.sequence)),
    messages, events, hasOlder: false, hasOlderEvents: false }
}

// Keep fixture streams open with valid checkpoints. Deliberate mutations can
// publish events through the returned sender, just as the real server does.
export async function installConversationStream(page: Page, snapshot: ConversationSnapshot | (() => ConversationSnapshot)) {
  const checkpoint = typeof snapshot === 'function' ? snapshot : () => snapshot
  const sessionId = checkpoint().session.id
  const binding = `__vistaFixtureCheckpoint_${sessionId}`
  await page.exposeFunction(binding, () => {
    const { session, throughSequence } = checkpoint()
    return { session, throughSequence }
  })
  await page.addInitScript(({ sessionId, binding }) => {
    const original = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const path = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href).pathname
      if (path !== `/api/sessions/${sessionId}/events`) return original(input, init)
      if (init?.signal?.aborted) throw init.signal.reason
      const checkpoint = await (window as any)[binding]()
      let finish: () => void
      const encoder = new TextEncoder()
      const body = new ReadableStream<Uint8Array>({
        start(sink) {
          const frame = () => sink.enqueue(encoder.encode(`event: sync\ndata: ${JSON.stringify(checkpoint)}\n\n`))
          const publish = (event: Event) => {
            const detail = (event as CustomEvent).detail
            if (detail.sessionId === sessionId) sink.enqueue(encoder.encode(`data: ${JSON.stringify(detail.event)}\n\n`))
          }
          window.addEventListener('vista-fixture-event', publish)
          const timer = setInterval(frame, 15_000)
          finish = () => { clearInterval(timer); window.removeEventListener('vista-fixture-event', publish); try { sink.close() } catch {} }
          init?.signal?.addEventListener('abort', finish, { once: true })
          frame()
        },
        cancel() { finish(); init?.signal?.removeEventListener('abort', finish) },
      })
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    }
  }, { sessionId, binding })
  return (event: StreamEvent) => page.evaluate(detail => window.dispatchEvent(new CustomEvent('vista-fixture-event', { detail })), { sessionId, event })
}
