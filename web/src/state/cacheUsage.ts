import type { ModelResultView } from '@vista/client'

export function cacheReadCount(usage: ModelResultView['usage']): number | undefined {
  if (!usage || usage.cacheReadKnown === false) return undefined
  // Older persisted positive counts establish a measurement; a legacy default
  // zero does not establish that the service reported cache telemetry.
  return usage.cacheReadKnown || usage.cacheReadTokens > 0 ? usage.cacheReadTokens : undefined
}

export function cacheTotals(results: ModelResultView[]) {
  const reads = results.map(result => cacheReadCount(result.usage))
  const writes = results.map(result => result.usage?.cacheWriteTokens)
  const sum = (counts: (number | undefined)[]) => counts.length && counts.every(count => count !== undefined)
    ? counts.reduce<number>((total, count) => total + count!, 0) : undefined
  return { read: sum(reads), write: sum(writes) }
}
