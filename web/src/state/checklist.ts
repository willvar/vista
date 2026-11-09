import type { ChecklistItem, Message } from '../client/types.ts'

export function checklistItems(items: readonly ChecklistItem[], active: boolean, status = '') {
  const stoppedStatus = ['canceled', 'cancelled', 'interrupted'].includes(status) ? 'interrupted' : 'incomplete'
  return items.map(item => ({ ...item, status: !active && item.status === 'in_progress' ? stoppedStatus : item.status }))
}

export function checklistSummaryPosition(message: Message) {
  const summary = message.checklist
  // Unfinished work only becomes incomplete/interrupted when the run stops.
  // Otherwise the checklist's final update can precede a lengthy final reply.
  const settled = summary && !summary.items.some(item => ['pending', 'in_progress'].includes(item.status))
  return {
    createdAt: settled && summary.updatedAt || message.createdAt,
    sequence: settled && summary.updatedSequence || message.sequence,
  }
}
