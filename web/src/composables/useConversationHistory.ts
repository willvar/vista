import { computed, hasInjectionContext, inject, nextTick, onScopeDispose, ref, shallowRef, watch, type InjectionKey, type Ref } from 'vue'
import type { ConversationItem } from '../state/mainOutputs.ts'

export const historyReadKey: InjectionKey<(key: symbol, pending: boolean) => void> = Symbol('historyReads')
export function useHistoryRead(loading: Ref<boolean>) {
  const report = hasInjectionContext() ? inject(historyReadKey, undefined) : undefined
  if (!report) return
  const key = Symbol()
  watch(loading, value => report(key, value), { immediate: true, flush: 'sync' })
  onScopeDispose(() => report(key, false))
}

// This is a preparation batch, never a display page: committed records remain
// in the continuous timeline when another batch is prepared above them.
const batchSize = 20
const maxBodies = 32
const maxBodyBytes = 8 * 1024 * 1024
function needsBody(item: ConversationItem) { return !!item.mainOutput || (item.outputBytes || 0) > 8192 }
function bodyVersion(item: ConversationItem) {
  return `${item.displayKey}/${item.outputTurn || 0}/${item.outputBytes || 0}/${item.mainOutput && !item.mainOutput.complete ? item.mainOutput.lastSequence : 'complete'}`
}
function sameFields(left: object, right: object, omitted: readonly string[]) {
  const a = left as Record<string, unknown>, b = right as Record<string, unknown>
  return Object.keys(a).every(key => omitted.includes(key) || a[key] === b[key])
    && Object.keys(b).every(key => omitted.includes(key) || key in a)
}
function preview(item: ConversationItem): ConversationItem {
  // Only presentation previews are shortened. Original model output remains
  // durable and is read in full again when an evicted row enters the viewport.
  return { ...item, content: item.content.slice(0, 8192), mainOutput: item.mainOutput
    ? { ...item.mainOutput, content: '', deltas: [], loaded: false } : undefined }
}
export function useConversationHistory(options: {
  items: Ref<ConversationItem[]>
  session: () => string | undefined
  revision: Ref<number>
  synchronized: () => boolean
  paused: () => boolean
  indexes: (runs: string[]) => Promise<void>
  output: (item: ConversationItem) => Promise<void>
  text: (item: ConversationItem) => string
  release?: (item: ConversationItem) => void
  traceKeys: () => string[]
  hasOlder: () => boolean
  older: () => Promise<void>
}) {
  const start = ref<number>()
  const items = shallowRef<ConversationItem[]>([])
  const prepared = ref(false)
  const loading = ref(false)
  const failure = ref('')
  const prepending = ref(false)
  const missing = ref(new Set<string>())
  const versions = new Map<string, string>()
  const bodies = new Map<string, { version: string; text: string }>()
  let bodyBytes = 0
  let sourceChanged = true
  const changedBodies = new Set<string>()
  const positions = new Map<string, number>()
  let visible = new Set<string>()
  let visibleRegistered = false
  const traces = ref(new Set<string>())
  const candidates = computed(() => start.value === undefined ? [] : options.items.value.filter(item => item.displaySequence >= start.value!))
  const candidatesByKey = computed(() => new Map(candidates.value.map(item => [item.displayKey, item])))
  const hasEarlier = computed(() => options.hasOlder() || start.value !== undefined && options.items.value.some(item => item.displaySequence < start.value!))
  const settled = computed(() => !loading.value && options.traceKeys().every(key => traces.value.has(key)))
  let generation = 0
  let disposed = false
  let request: Promise<void> | undefined
  let olderRequest: Promise<void> | undefined
  let changed = false
  let initialized = false
  let sessionIdentity: string | undefined

  function removeBody(key: string) {
    const body = bodies.get(key)
    if (!body) return
    bodies.delete(key)
    bodyBytes -= body.text.length * 2
    changedBodies.add(key)
  }
  function trim() {
    const pinned = visibleRegistered ? visible : new Set(candidates.value.slice(-batchSize).map(item => item.displayKey))
    for (const [key] of bodies) {
      if (bodies.size <= maxBodies && bodyBytes <= maxBodyBytes) break
      if (!pinned.has(key)) removeBody(key)
    }
  }
  function project(item: ConversationItem, previous?: ConversationItem): ConversationItem {
    const large = needsBody(item)
    if (!large) {
      if (previous && sameFields(item, previous, [])) return previous
      return { ...item, content: options.text(item) }
    }
    const body = bodies.get(item.displayKey)
    const content = body?.version === bodyVersion(item) ? body.text : item.content.slice(0, 8192)
    const output = item.mainOutput, oldOutput = previous?.mainOutput
    const sameOutput = output && oldOutput ? sameFields(output, oldOutput, ['content', 'deltas', 'loaded']) : output === oldOutput
    if (previous && previous.content === content && sameOutput && sameFields(item, previous, ['content', 'mainOutput'])) return previous
    return { ...preview(item), content }
  }
  function publish() {
    if (disposed || options.paused() || !settled.value || !options.synchronized() && !prepared.value) return
    trim()
    if (sourceChanged || changedBodies.size) {
      const previous = items.value
      const absent = sourceChanged ? new Set<string>() : new Set(missing.value)
      const update = (item: ConversationItem, old?: ConversationItem) => {
        if (needsBody(item) && bodies.get(item.displayKey)?.version !== bodyVersion(item)) absent.add(item.displayKey)
        else absent.delete(item.displayKey)
        return project(item, old)
      }
      let next = previous
      if (sourceChanged) {
        const oldByKey = new Map(previous.map(item => [item.displayKey, item]))
        positions.clear()
        next = candidates.value.map((item, index) => {
          positions.set(item.displayKey, index)
          return update(item, oldByKey.get(item.displayKey))
        })
        for (const key of versions.keys()) if (!positions.has(key)) { versions.delete(key); removeBody(key) }
      } else {
        // Visibility changes affect only evicted/rehydrated bodies, not every
        // message already prepared in the continuous history.
        for (const key of changedBodies) {
          const index = positions.get(key), source = candidatesByKey.value.get(key)
          if (index === undefined || !source) continue
          const row = update(source, previous[index])
          if (row === previous[index]) continue
          if (next === previous) next = previous.slice()
          next[index] = row
        }
      }
      if (next !== previous && (next.length !== previous.length || next.some((item, index) => item !== previous[index]))) items.value = next
      if (absent.size !== missing.value.size || [...absent].some(key => !missing.value.has(key))) missing.value = absent
      sourceChanged = false
      changedBodies.clear()
    }
    prepared.value = true
    prepending.value = false
  }
  function prepare(): Promise<void> {
    changed = true
    if (request) return request
    if (!options.session() || !options.synchronized() && !prepared.value) return Promise.resolve()
    const current = generation
    loading.value = true
    failure.value = ''
    const pending = Promise.resolve().then(async () => {
      do {
        changed = false
        const initial = start.value === undefined
        const batch = initial ? options.items.value.slice(-batchSize) : candidates.value
        await options.indexes(batch.map(item => item.runId))
        if (disposed || current !== generation) return
        if (initial && start.value === undefined) start.value = options.items.value.slice(-batchSize)[0]?.displaySequence ?? 0
        const outputs = candidates.value.filter(item => needsBody(item) && (versions.get(item.displayKey) !== bodyVersion(item)
          || visible.has(item.displayKey) && bodies.get(item.displayKey)?.version !== bodyVersion(item)))
        await Promise.all(Array.from({ length: Math.min(4, outputs.length) }, async () => {
          while (outputs.length && current === generation && !disposed) {
            const item = outputs.shift()!
            await options.output(item)
            if (current !== generation || disposed || !needsBody(item)) continue
            const latest = candidatesByKey.value.get(item.displayKey)
            if (!latest) continue
            const version = bodyVersion(latest)
            versions.set(item.displayKey, version)
            removeBody(item.displayKey)
            const text = options.text(latest)
            bodies.set(item.displayKey, { version, text })
            bodyBytes += text.length * 2
            changedBodies.add(item.displayKey)
            options.release?.(latest)
          }
        }))
        if (disposed || current !== generation) return
      } while (changed)
    }).catch(cause => { if (current === generation && !disposed) failure.value = String(cause) }).finally(() => {
      if (current !== generation || disposed) return
      request = undefined
      loading.value = false
    })
    request = pending
    return pending
  }
  function setVisible(keys: string[]) {
    if (visibleRegistered && keys.length === visible.size && keys.every(key => visible.has(key))) return
    visibleRegistered = true
    visible = new Set(keys)
    for (const key of keys) {
      const body = bodies.get(key)
      if (body) { bodies.delete(key); bodies.set(key, body) }
    }
    if (options.paused()) return
    publish()
    if (keys.some(key => missing.value.has(key))) void prepare()
  }
  function traceReady(key: string, ready: boolean) {
    if (traces.value.has(key) === ready) return
    const next = new Set(traces.value)
    if (ready) next.add(key)
    else next.delete(key)
    traces.value = next
  }
  async function earlier() {
    if (olderRequest || loading.value || prepending.value || !hasEarlier.value) return olderRequest
    const current = generation
    prepending.value = true
    olderRequest = (async () => {
      const before = start.value ?? Infinity
      if (!options.items.value.some(item => item.displaySequence < before)) await options.older()
      if (disposed || current !== generation) return
      const preceding = options.items.value.filter(item => item.displaySequence < before).slice(-batchSize)
      if (preceding.length) { sourceChanged = true; start.value = preceding[0]!.displaySequence; await prepare() }
      else prepending.value = false
    })().finally(() => { if (current === generation) olderRequest = undefined })
    return olderRequest
  }
  watch([options.session, options.revision], () => {
    const changedSession = !initialized || sessionIdentity !== options.session()
    initialized = true
    sessionIdentity = options.session()
    generation++
    sourceChanged = true
    request = undefined; olderRequest = undefined; changed = false; prepending.value = false
    if (changedSession) {
      start.value = undefined; items.value = []; prepared.value = false
      visible.clear(); visibleRegistered = false; missing.value = new Set()
      versions.clear(); bodies.clear(); bodyBytes = 0; changedBodies.clear(); positions.clear()
    } else if (start.value !== undefined && !candidates.value.length) {
      start.value = options.items.value.slice(-batchSize)[0]?.displaySequence ?? 0
    }
    loading.value = false
    traces.value = new Set()
    void nextTick(prepare)
  }, { immediate: true, flush: 'sync' })
  watch(options.items, () => { sourceChanged = true; void prepare() }, { flush: 'post' })
  watch(options.synchronized, ready => { if (ready) void prepare() }, { flush: 'post' })
  watch([settled, options.paused], () => { publish(); if (!options.paused() && [...visible].some(key => missing.value.has(key))) void prepare() }, { flush: 'post' })
  onScopeDispose(() => { disposed = true; generation++ })
  return { items, candidates, start, prepared, loading, failure, prepending, hasEarlier, earlier, traceReady, prepare, missing, setVisible }
}
