import type { StreamEvent } from '../types.js'

export function agentFailureReason(event: StreamEvent): { taskId: string; reason: string } | undefined {
  const data = event.data
  if (event.kind === 'runtime_event' && data?.kind === 'agent' && data.status === 'failed' && !data.modelOnly
    && typeof data.sourceId === 'string' && data.sourceId && data.sourceId !== 'main'
    && typeof data.content === 'string' && data.content.trim())
    return { taskId: data.sourceId, reason: data.content }
}

export function isTimelineEvent(event: StreamEvent): boolean {
  const data = event.data || {}
  if (event.kind === 'message') return true
  if (event.kind === 'tool') return !!data.tool && data.tool.name?.split('/').at(-1) !== 'update_checklist'
  if (event.kind === 'model_retry') return !!data.modelRetry
  if (event.kind === 'source') return /^https?:\/\//.test(data.content || '')
  return ['model_delta', 'model_reasoning_delta'].includes(event.kind) && !data.purpose && data.turn > 0
    && typeof data.content === 'string' && !!data.content
}
