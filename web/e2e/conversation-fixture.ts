import type { ConversationSnapshot, Message, Session, StreamEvent } from '../src/client/types'

// A mock core read must describe the same saved state as its message/events
// endpoints. Tests with delayed loads should delay this whole view together.
export function coreSnapshot(session: Session, messages: Message[] = [], events: StreamEvent[] = []): ConversationSnapshot {
  return { session: { ...session, latestReplySequence: Math.max(0, ...messages.filter(message => message.role === 'assistant').map(message => message.sequence)) },
    throughSequence: Math.max(0, ...messages.map(message => message.sequence), ...events.map(event => event.sequence)),
    messages, events, hasOlder: false, hasOlderEvents: false }
}
