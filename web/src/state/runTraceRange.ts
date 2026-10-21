export type RunTraceRange = { after?: number; through?: number }

export function inRunTraceRange(sequence: number, range: RunTraceRange): boolean {
  return sequence > (range.after ?? 0) && (range.through === undefined || sequence <= range.through)
}

export function runTraceRangeQuery(range: RunTraceRange): string {
  const query = new URLSearchParams()
  if (range.after !== undefined) query.set('after', String(range.after))
  if (range.through !== undefined) query.set('through', String(range.through))
  return query.size ? `?${query}` : ''
}
