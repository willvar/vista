export type ModelDiagnosticSummary = {
  id: string
  sessionId: string
  runId: string
  taskId?: string
  turn?: number
  purpose?: string
  provider: string
  protocol: string
  model: string
  startedAt: string
  durationMs: number
  error?: string
  truncated: boolean
}

export function mergeDiagnosticSummaries(current: ModelDiagnosticSummary[], updates: ModelDiagnosticSummary[], sessionId: string, runId: string) {
  const records = new Map(current.map(record => [record.id, record]))
  for (const record of updates) if (record.sessionId === sessionId && record.runId === runId) records.set(record.id, record)
  return [...records.values()].sort((a, b) => a.startedAt.localeCompare(b.startedAt) || a.id.localeCompare(b.id))
}

type CapturedBody = { content: string; encoding: string; bytes: number; truncated: boolean }
export type ModelDiagnosticRecord = ModelDiagnosticSummary & {
  finishReason?: string
  parsing?: {
    line?: number; event?: number; reasoningFields?: string[]
    reasoningFragments: number; reasoningBlocks: number; reasoningBytes: number; visibleReasoningBytes: number
  }
  exchanges: {
    method: string; url: string; requestHeaders: Record<string, string[]>; request: CapturedBody
    status: number; responseHeaders?: Record<string, string[]>; response: CapturedBody
    startedMs: number; headersMs: number; durationMs: number
    chunks: { offset: number; bytes: number; atMs: number }[]
    timingsTruncated: boolean; eof: boolean; readError?: string; error?: string
  }[]
}
