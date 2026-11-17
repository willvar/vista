import type { ConversationItem } from './mainOutputs.ts'
import type { StreamEvent } from '../client/types.ts'

export function isTimelineEvent(event: StreamEvent): boolean {
  const data = event.data || {}
  if (event.kind === 'message') return true
  if (event.kind === 'tool') return !!data.tool && data.tool.name?.split('/').at(-1) !== 'update_checklist'
  if (event.kind === 'model_retry') return !!data.modelRetry
  if (event.kind === 'source') return /^https?:\/\//.test(data.content || '')
  return ['model_delta', 'model_reasoning_delta'].includes(event.kind) && !data.purpose && data.turn > 0
    && typeof data.content === 'string' && !!data.content
}

export type TraceTimelineEntry = { key: string; sequence: number; lastSequence: number; estimate: number }
export type TimelineEntry = {
  key: string
  sequence: number
  estimate: number
  message?: ConversationItem
  trace?: string
  step?: string
}
export type TimelineLayout = { index: number; start: number; size: number; key: string | number | bigint }
export function traceTimelineKey(trace: string, step: string) { return `trace/${trace}/${step}` }

// Use the measured virtual viewport, not overscan or bottom-follow intent.
// A reference is useful only after the user's complete row has left above it.
export function recentSentMessage(messages: readonly ConversationItem[], firstVisibleSequence?: number): ConversationItem | undefined {
  if (firstVisibleSequence === undefined) return
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index]!
    if (message.role !== 'user' || message.contextSummary) continue
    return message.displaySequence < firstVisibleSequence && (message.content.trim() || message.imageCount) ? message : undefined
  }
}

export function conversationTimeline(messages: readonly ConversationItem[], traces: Readonly<Record<string, readonly TraceTimelineEntry[]>>, start: number): TimelineEntry[] {
  const rows: TimelineEntry[] = messages.map(message => ({ key: message.displayKey, sequence: message.displaySequence,
    estimate: Math.max(80, Math.min(1200, 80 + message.content.length / 50 * 24)), message }))
  for (const [trace, entries] of Object.entries(traces)) for (const entry of entries) {
    if (entry.lastSequence < start) continue
    rows.push({ key: traceTimelineKey(trace, entry.key), sequence: entry.sequence, estimate: entry.estimate, trace, step: entry.key })
  }
  return rows.sort((a, b) => a.sequence - b.sequence || a.key.localeCompare(b.key))
}
