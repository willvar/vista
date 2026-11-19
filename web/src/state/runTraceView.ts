import type { RunHistoryCache } from '../composables/useRunHistory'

export type RunTraceViewState = {
  expanded: boolean
  tasks: Record<string, boolean>
  nodes: string[]
  lookups: Record<string, boolean>
  history: RunHistoryCache
}

export function createRunTraceView(): RunTraceViewState {
  return { expanded: false, tasks: {}, nodes: [], lookups: {}, history: {} }
}
