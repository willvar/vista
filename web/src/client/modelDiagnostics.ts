import type { ModelDiagnosticSummary } from '@vista/client'

export function mergeDiagnosticSummaries(current: ModelDiagnosticSummary[], updates: ModelDiagnosticSummary[], sessionId: string, runId: string) {
  const records = new Map(current.map(record => [record.id, record]))
  for (const record of updates) if (record.sessionId === sessionId && record.runId === runId) records.set(record.id, record)
  return [...records.values()].sort((a, b) => a.startedAt.localeCompare(b.startedAt) || a.id.localeCompare(b.id))
}
