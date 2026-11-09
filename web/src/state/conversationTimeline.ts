import type { ConversationItem } from './mainOutputs.ts'

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

export function conversationTimeline(messages: readonly ConversationItem[], traces: Readonly<Record<string, readonly TraceTimelineEntry[]>>, start: number): TimelineEntry[] {
  const rows: TimelineEntry[] = messages.map(message => ({ key: message.displayKey, sequence: message.displaySequence,
    estimate: message.checklist ? 160 : Math.max(80, Math.min(1200, 80 + message.content.length / 50 * 24)), message }))
  for (const [trace, entries] of Object.entries(traces)) for (const entry of entries) {
    if (entry.lastSequence < start) continue
    rows.push({ key: traceTimelineKey(trace, entry.key), sequence: entry.sequence, estimate: entry.estimate, trace, step: entry.key })
  }
  return rows.sort((a, b) => a.sequence - b.sequence || a.key.localeCompare(b.key))
}
