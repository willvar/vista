import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import type { VistaClient } from '../client/vista'
import type { RunOverview, StreamEvent } from '../client/types'
import { inRunTraceRange, runTraceRangeQuery, type RunTraceRange } from '../state/runTraceRange.ts'

type RunHistoryOptions = RunTraceRange & {
  sessionId: string
  runId: string
  before: number
  first: number
  events: StreamEvent[]
}

export type RunHistoryCache = {
  sessionId?: string
  runId?: string
  rangeAfter?: number
  rangeThrough?: number
  overview?: RunOverview | null
  events?: Map<number, StreamEvent>
  hydrated?: StreamEvent[]
  loadedSequences?: Set<number>
  before?: number
  throughSequence?: number
  hasMore?: boolean
}

export function useRunHistory(client: VistaClient, options: RunHistoryOptions, expanded: Ref<boolean>, cache?: RunHistoryCache | (() => RunHistoryCache)) {
  const history = ref(new Map<number, StreamEvent>())
  const loading = ref(false)
  const failure = ref('')
  const overview = ref<RunOverview | null>(null)
  const overviewLoading = ref(false)
  const overviewFailure = ref('')
  const hasMore = ref(true)
  const hydrated = ref<StreamEvent[]>([])
  const loadedSequences = ref(new Set<number>())
  const nodeLoading = ref<Record<number, boolean>>({})
  const nodeFailure = ref<Record<number, string>>({})
  const nodeRequests = new Map<number, Promise<void>>()
  let before = 0
  let throughSequence = 0
  let catchupTarget = 0
  let resumed = false
  let controller = new AbortController()
  let overviewRequest: Promise<void> | undefined
  let historyRequest: Promise<void> | undefined
  let currentCache: RunHistoryCache | undefined
  const inRange = (event: StreamEvent) => event.data?.runId === options.runId && inRunTraceRange(event.sequence, options)
  const firstSequence = () => Math.max(overview.value?.firstSequence || options.first, (options.after ?? 0) + 1)
  const lastLiveSequence = () => Math.max(0, ...options.events.filter(inRange).map(event => event.sequence))
  const rangeLast = (sequence: number) => Math.min(sequence, options.through ?? Infinity)

  function saveCache() {
    if (!currentCache) return
    currentCache.sessionId = options.sessionId
    currentCache.runId = options.runId
    currentCache.rangeAfter = options.after
    currentCache.rangeThrough = options.through
    currentCache.overview = overview.value
    currentCache.events = history.value
    currentCache.hydrated = hydrated.value
    currentCache.loadedSequences = loadedSequences.value
    currentCache.before = before
    currentCache.throughSequence = throughSequence
    currentCache.hasMore = hasMore.value
  }

  function loadOverview(): Promise<void> {
    if (overviewRequest) return overviewRequest
    if (controller.signal.aborted) return Promise.resolve()
    const current = controller
    const sessionId = options.sessionId
    const runId = options.runId
    overviewLoading.value = true
    overviewFailure.value = ''
    const request = Promise.resolve().then(async () => {
      try {
        if (current.signal.aborted || current !== controller) return
        const result = await client.request<RunOverview>(`/sessions/${encodeURIComponent(sessionId)}/runs/${encodeURIComponent(runId)}/overview${runTraceRangeQuery(options)}`, { signal: current.signal })
        if (!current.signal.aborted && current === controller) {
          overview.value = { ...result, events: result.events.filter(inRange) }
          if (resumed) catchupTarget = Math.max(catchupTarget, rangeLast(result.throughSequence))
          saveCache()
        }
      } catch (cause) { if (!current.signal.aborted && current === controller) overviewFailure.value = String(cause) }
      finally {
        if (current === controller) {
          overviewLoading.value = false
          overviewRequest = undefined
        }
      }
    })
    overviewRequest = request
    return request
  }

  function retainPage(page: StreamEvent[]) {
    for (const event of page) {
      if (!inRange(event)) continue
      // Compact tool previews still need their original event on expansion.
      const complete = event.kind !== 'tool' || !event.data?.tool?.truncated
      if (complete || !history.value.has(event.sequence)) history.value.set(event.sequence, event)
      if (complete) loadedSequences.value.add(event.sequence)
    }
  }

  function pageCursor(page: StreamEvent[], requestedBefore: number): number {
    if (!page.length) return 0
    const cursor = Math.min(...page.map(event => event.sequence))
    if (page.some(event => !Number.isSafeInteger(event.sequence) || event.sequence < 1 || requestedBefore > 0 && event.sequence >= requestedBefore)) {
      throw new Error('Execution history did not advance')
    }
    return cursor
  }

  function load(): Promise<void> {
    if (historyRequest) return historyRequest
    if (controller.signal.aborted || !hasMore.value && catchupTarget <= throughSequence && !(resumed && overviewRequest)) return Promise.resolve()
    const current = controller
    const sessionId = options.sessionId
    const runId = options.runId
    loading.value = true
    failure.value = ''
    const request = Promise.resolve().then(async () => {
      try {
        if (current.signal.aborted || current !== controller) return
        if (resumed && overviewRequest) await overviewRequest
        if (current.signal.aborted || current !== controller) return
        if (resumed) catchupTarget = Math.max(catchupTarget, rangeLast(overview.value?.throughSequence || 0))
        const catchupThrough = catchupTarget
        // A virtualized message may have been absent while its run kept
        // executing. Fill only the gap since the last successful history read.
        if (throughSequence > 0 && catchupThrough > throughSequence) {
          do {
            const query = new URLSearchParams({ compact: '1', after: String(throughSequence) })
            const page = await client.request<StreamEvent[]>(`/sessions/${encodeURIComponent(sessionId)}/event-page?${query}`, { signal: current.signal })
            if (current.signal.aborted || current !== controller) return
            if (!page.length || page.some(event => !Number.isSafeInteger(event.sequence) || event.sequence <= throughSequence)) {
              throw new Error('Execution history did not advance')
            }
            retainPage(page.filter(event => event.sequence <= catchupThrough && event.data?.runId === runId))
            throughSequence = Math.min(catchupThrough, Math.max(...page.map(event => event.sequence)))
            saveCache()
          } while (throughSequence < catchupThrough && expanded.value)
        }
        if (!hasMore.value || current.signal.aborted || current !== controller) return
        if (!before) {
          if (!overview.value) await loadOverview()
          if (current.signal.aborted || current !== controller) return
          const runLast = rangeLast(Math.max(overview.value?.throughSequence || 0, lastLiveSequence()))
          // Never start at the session's newest event when this older run has
          // no watermark: it would traverse unrelated executions.
          if (!runLast) {
            if (!overview.value) throw new Error('Execution overview unavailable')
            hasMore.value = false
            saveCache()
            return
          }
          before = Math.min(options.before || runLast + 1, runLast + 1)
        }
        do {
          if (before <= firstSequence()) { hasMore.value = false; saveCache(); break }
          const query = new URLSearchParams({ compact: '1', before: String(before) })
          const requestedBefore = before
          const page = await client.request<StreamEvent[]>(`/sessions/${encodeURIComponent(sessionId)}/event-page?${query}`, { signal: current.signal })
          if (current.signal.aborted || current !== controller) return
          const cursor = pageCursor(page, before)
          retainPage(page.filter(event => event.sequence < before && event.sequence >= firstSequence() && event.data?.runId === runId))
          if (cursor) before = cursor
          throughSequence = Math.max(throughSequence, requestedBefore - 1)
          hasMore.value = page.length >= 100 && cursor > firstSequence()
          saveCache()
          // A collapsed execution doesn't keep walking its history. Opening it
          // resumes this cursor; live events never restart completed pages.
        } while (hasMore.value && expanded.value)
      } catch (cause) { if (!current.signal.aborted && current === controller) failure.value = String(cause) }
      finally {
        if (current === controller) {
          loading.value = false
          historyRequest = undefined
        }
      }
    })
    historyRequest = request
    return request
  }

  function loadNode(sequence: number): Promise<void> {
    if (!inRunTraceRange(sequence, options) || loadedSequences.value.has(sequence) || controller.signal.aborted) return Promise.resolve()
    const pending = nodeRequests.get(sequence)
    if (pending) return pending
    const current = controller
    const sessionId = options.sessionId
    const runId = options.runId
    nodeLoading.value[sequence] = true
    nodeFailure.value[sequence] = ''
    const request = Promise.resolve().then(async () => {
      try {
        if (current.signal.aborted || current !== controller) return
        const event = await client.request<StreamEvent>(`/sessions/${encodeURIComponent(sessionId)}/runs/${encodeURIComponent(runId)}/events/${sequence}`, { signal: current.signal })
        if (current.signal.aborted || current !== controller) return
        if (event.sequence !== sequence || !inRange(event)) throw new Error('Unexpected event')
        hydrated.value = [...hydrated.value.filter(value => value.sequence !== sequence), event]
        loadedSequences.value.add(sequence)
        saveCache()
      } catch (cause) { if (!current.signal.aborted && current === controller) nodeFailure.value[sequence] = String(cause) }
      finally {
        if (current === controller) {
          nodeLoading.value[sequence] = false
          nodeRequests.delete(sequence)
        }
      }
    })
    nodeRequests.set(sequence, request)
    return request
  }

  watch(() => [options.sessionId, options.runId, options.after, options.through], () => {
    controller.abort()
    controller = new AbortController()
    before = 0
    throughSequence = 0
    catchupTarget = 0
    resumed = false
    historyRequest = undefined
    hasMore.value = true
    loading.value = false
    failure.value = ''
    history.value = new Map()
    hydrated.value = []
    loadedSequences.value = new Set()
    nodeLoading.value = {}
    nodeFailure.value = {}
    nodeRequests.clear()
    overview.value = null
    overviewLoading.value = false
    overviewFailure.value = ''
    overviewRequest = undefined
    currentCache = typeof cache === 'function' ? cache() : cache
    if (currentCache?.sessionId === options.sessionId && currentCache.runId === options.runId
      && currentCache.rangeAfter === options.after && currentCache.rangeThrough === options.through) {
      resumed = true
      overview.value = currentCache.overview || null
      history.value = new Map([...(currentCache.events || new Map())].filter(([, event]) => inRange(event)))
      hydrated.value = (currentCache.hydrated || []).filter(inRange)
      loadedSequences.value = new Set([...(currentCache.loadedSequences || [])].filter(sequence => inRunTraceRange(sequence, options)))
      before = currentCache.before || 0
      throughSequence = currentCache.throughSequence || 0
      catchupTarget = rangeLast(Math.max(lastLiveSequence(), overview.value?.throughSequence || 0))
      hasMore.value = currentCache.hasMore ?? true
    }
    const finished = options.through !== undefined || !!overview.value?.finishedAt || [...history.value.values()].some(event => event.kind === 'run_finished')
    const liveBackground = [...overview.value?.events || [], ...history.value.values()].some(event => event.kind === 'agent_task' && event.data?.task?.join === 'detached')
    if (!overview.value || resumed && (!finished || liveBackground)) void loadOverview()
    if (expanded.value) void load()
  }, { immediate: true })
  watch(expanded, value => { if (value && (hasMore.value || catchupTarget > throughSequence)) void load() }, { immediate: true })
  onScopeDispose(() => controller.abort())
  const events = computed(() => {
    const unique = new Map<number, StreamEvent>()
    for (const event of [...overview.value?.events || [], ...options.events, ...history.value.values(), ...hydrated.value]) {
      if (!inRange(event)) continue
      const existing = unique.get(event.sequence)
      // A compact historical page must not replace an original live or
      // individually hydrated tool result with its shorter preview.
      if (event.kind === 'tool' && event.data?.tool?.truncated && existing?.kind === 'tool' && !existing.data?.tool?.truncated) continue
      unique.set(event.sequence, event)
    }
    return [...unique.values()].sort((a, b) => a.sequence - b.sequence)
  })
  const modelCalls = computed(() => {
    if (!overview.value) return null
    const after = overview.value.throughSequence
    return overview.value.modelCalls + events.value.filter(event => event.sequence > after && event.kind === 'model_request' && event.data?.purpose !== 'summary').length
  })
  return { events, loading, failure, load, hasMore, overview, overviewLoading, overviewFailure, loadOverview, modelCalls, loadNode, nodeLoading, nodeFailure, loadedSequences }
}
