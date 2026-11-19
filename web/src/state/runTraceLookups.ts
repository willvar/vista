import { compactTraceText, type RunTraceNode } from './runTrace.ts'

type LookupKind = 'search' | 'file' | 'page' | 'directory'
type LookupMessage = { key: string; params?: Record<string, string | number> }
export type TraceLookupStep<T extends RunTraceNode = RunTraceNode> = {
  item: T; index: number; kind: LookupKind; target: string; label: string
  status: 'success' | 'loading' | 'problem'; hint?: LookupMessage; reason?: string
}
export type TraceLookupRow<T extends RunTraceNode = RunTraceNode> = {
  key: string; item: T; index: number; members?: TraceLookupStep<T>[]; open?: boolean; lookup?: TraceLookupStep<T>
}

// Classify known read-only tools, not shell commands, labels or MCP names.
const lookupKinds = new Map<string, LookupKind>([
  ['search', 'search'], ['search_files', 'search'], ['grep', 'search'], ['find_files', 'search'], ['websearch', 'search'],
  ['read_file', 'file'], ['read', 'file'], ['webfetch', 'page'], ['fetch_url', 'page'], ['list_files', 'directory'],
])

function shortTarget(kind: LookupKind, target: string) {
  if (kind === 'page') {
    try {
      const url = new URL(target)
      return compactTraceText(url.host + (url.pathname === '/' ? '' : url.pathname), 64) || compactTraceText(target, 64)
    } catch { /* Some historical events have a descriptive title instead of a URL. */ }
  }
  if (kind === 'file' || kind === 'directory') target = target.split(/[\\/]/).filter(Boolean).at(-1) || target
  return compactTraceText(target, 64)
}

function resultHint(item: RunTraceNode, kind: LookupKind): LookupMessage | undefined {
  if (item.search) {
    const { count, paths, matches, truncated, warnings } = item.search
    if (truncated || warnings?.length) return { key: 'traceSearchPartial' }
    const hits = paths?.length ?? count ?? matches.filter(row => !row.context).length
    return hits ? { key: 'traceSearchCount', params: { count: hits } } : { key: 'traceNoMatches' }
  }
  if (item.resultSummaryKey) return { key: item.resultSummaryKey, params: item.resultSummaryParams }
  // A compact history page can omit these fields. Never fetch a result to
  // produce its summary, or infer an empty search from missing metadata.
  if (!item.truncated && (kind === 'file' || item.toolName === 'websearch') && item.rawBody?.startsWith('{')) {
    try {
      const result = JSON.parse(item.rawBody)
      if (kind === 'file' && Number.isInteger(result.startLine) && result.startLine > 0 && Number.isInteger(result.endLine) && result.endLine >= result.startLine) {
        return { key: 'traceLookupLines', params: { start: result.startLine, end: result.endLine } }
      }
      if (item.toolName === 'websearch' && Array.isArray(result.results)) {
        if (result.truncated === true) return { key: 'traceSearchPartial' }
        if (!result.results.length) return { key: 'traceNoResults' }
      }
    } catch { /* No structured summary available. */ }
  }
}

function lookupStep<T extends RunTraceNode>(item: T, index: number): TraceLookupStep<T> | undefined {
  const kind = item.kind === 'tool' && lookupKinds.get(item.toolName || '')
  const target = item.summary?.trim()
  if (!kind || !target) return
  const step: TraceLookupStep<T> = { item, index, kind, target, label: shortTarget(kind, target), status: 'problem' }
  if (item.status === 'error' || item.error) {
    step.hint = { key: 'statusFailed' }
    step.reason = compactTraceText(item.error || (typeof item.eventData?.error === 'string' ? item.eventData.error : ''), 140)
  } else if (item.status === 'abort') step.hint = { key: 'statusInterrupted' }
  else if (item.resultSummaryKey === 'traceInstructionsRequired') step.hint = { key: item.resultSummaryKey }
  else if (item.status === 'loading') {
    step.status = 'loading'
    step.hint = { key: 'statusRunning' }
  } else if (item.status === 'success') {
    step.status = 'success'
    step.hint = resultHint(item, kind)
  } else step.hint = { key: 'traceStatusUnreported' }
  return step
}

export function runTraceLookupRows<T extends RunTraceNode>(items: readonly T[], boundaries: readonly number[],
  openGroups: Readonly<Record<string, boolean>>, openSteps: readonly string[]): TraceLookupRow<T>[] {
  const rows: TraceLookupRow<T>[] = []
  const cuts = [...boundaries].sort((a, b) => a - b)
  const disclosed = new Set(openSteps)
  let cut = 0
  let members: TraceLookupStep<T>[] = []
  function flush() {
    const first = members[0]
    if (members.length === 1) rows.push({ item: first.item, index: first.index, key: first.item.key })
    else if (members.length > 1) {
      const key = `lookup/${first.item.key}`
      // Keep already open output visible when a live second step forms a group.
      const open = openGroups[key] ?? members.some(step => disclosed.has(step.item.key))
      rows.push({ item: first.item, index: first.index, key, members, open })
      if (open) for (const step of members) rows.push({ item: step.item, index: step.index, key: step.item.key, lookup: step })
    }
    members = []
  }
  items.forEach((item, index) => {
    const previous = members.at(-1)?.item
    const step = lookupStep(item, index)
    if (previous) {
      while (cuts[cut] !== undefined && cuts[cut] <= previous.sequence) cut++
      if (!step || item.taskId !== previous.taskId || cuts[cut] < item.sequence) flush()
    }
    if (step) members.push(step)
    else rows.push({ item, index, key: item.key })
  })
  flush()
  return rows
}

export function summarizeTraceLookups(steps: readonly TraceLookupStep[]) {
  const counts: Record<LookupKind, number> = { search: 0, file: 0, page: 0, directory: 0 }
  const completed = new Map<string, TraceLookupStep>()
  const unsettled = steps.filter(step => step.status !== 'success')
  for (const step of steps) {
    if (step.status !== 'success') continue
    const id = step.kind === 'search' ? step.item.key : `${step.kind}/${step.target}`
    if (!completed.has(id)) counts[step.kind]++
    // Prefer the most recently recorded result for repeated reads of a target.
    completed.set(id, step)
  }
  const chosen = new Set<TraceLookupStep>()
  const kinds = new Set<LookupKind>()
  for (const step of completed.values()) if (!kinds.has(step.kind)) {
    kinds.add(step.kind)
    chosen.add(step)
  }
  for (const step of completed.values()) {
    if (chosen.size >= 5) break
    chosen.add(step)
  }
  return { counts, preview: [...chosen].sort((a, b) => a.index - b.index), omitted: completed.size - chosen.size, unsettled }
}
