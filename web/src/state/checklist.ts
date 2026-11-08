import type { ChecklistItem } from '../client/types.ts'

export function checklistItems(items: readonly ChecklistItem[], active: boolean, status = '') {
  const stoppedStatus = ['canceled', 'cancelled', 'interrupted'].includes(status) ? 'interrupted' : 'incomplete'
  return items.map(item => ({ ...item, status: !active && item.status === 'in_progress' ? stoppedStatus : item.status }))
}
