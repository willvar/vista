import type { RunHistoryCache } from '../composables/useRunHistory'

export type RunTraceViewState = {
  expanded: boolean
  tasks: Record<string, boolean>
  nodes: string[]
  history: RunHistoryCache
}

export function createRunTraceView(): RunTraceViewState {
  return { expanded: false, tasks: {}, nodes: [], history: {} }
}
