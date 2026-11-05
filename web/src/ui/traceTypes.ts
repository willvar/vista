import type { RunTraceNode } from '../state/runTrace'

export type TraceNodeView = RunTraceNode & { collapsible?: boolean; description?: string }
