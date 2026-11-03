import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import type { createWorkspaceSync } from '../client/workspaceSync.ts'
import { localDateKey, revisionAtLeast, type UsageStatistics } from '../client/statistics.ts'

const caches = new WeakMap<VistaClient, Map<string, UsageStatistics>>()

export function useStatistics(client: VistaClient, sync: ReturnType<typeof createWorkspaceSync>, days: Ref<number>) {
  const cache = caches.get(client) ?? new Map<string, UsageStatistics>()
  caches.set(client, cache)
  const data = shallowRef<UsageStatistics>()
  const loading = ref(true)
  const error = ref('')
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  const path = computed(() => `/statistics?days=${days.value}&timezone=${encodeURIComponent(timezone)}`)
  let controller: AbortController | undefined
  let generation = 0
  let pending: { path: string; signal: AbortSignal; promise: Promise<void> } | undefined
  let targetVersion = ''

  async function load(signal?: AbortSignal, propagate = false) {
    const requested = path.value
    if (pending?.path === requested && !pending.signal.aborted) {
      try { await pending.promise } catch(cause) { if (propagate) throw cause }
      return
    }
    controller?.abort()
    const owner = controller = new AbortController()
    const requestSignal = signal ? AbortSignal.any([signal, owner.signal]) : owner.signal
    const current = ++generation
    loading.value = true; error.value = ''
    const promise = (async () => {
      try {
        const value = await client.request<UsageStatistics>(requested, { signal: requestSignal })
        if (!requestSignal.aborted && current === generation && path.value === requested) {
          const previous = cache.get(requested)
          if (!previous || !revisionAtLeast(previous.version, value.version) || previous.version === value.version && previous.sampledAt <= value.sampledAt) cache.set(requested, value)
          data.value = cache.get(requested)!
        }
      } catch (cause) {
        if (!requestSignal.aborted && current === generation) { error.value = String(cause); throw cause }
      } finally { if (current === generation) { pending = undefined; loading.value = false } }
    })()
    pending = { path: requested, signal: requestSignal, promise }
    try { await promise } catch(cause) { if (propagate) throw cause }
  }
  function fresh(value: UsageStatistics | undefined) {
    // A view can be closed across an hour boundary without having a server
    // clock subscription. Reopening must update the average's denominator.
    const sampled = new Date(value?.sampledAt ?? ''), now = new Date()
    return !!value && value.days.at(-1)?.date === localDateKey() && sampled.getHours() === now.getHours()
      && sampled.getTimezoneOffset() === now.getTimezoneOffset() && revisionAtLeast(value.version, targetVersion)
  }
  watch(path, () => {
    const saved = cache.get(path.value)
    if (fresh(saved)) { controller?.abort(); generation++; pending = undefined; data.value = saved; loading.value = false; error.value = '' }
    else { data.value = undefined; void load() }
  })
  const unsubscribe = sync.subscribe(['statistics'], async (_, signal, point) => {
    targetVersion = `${point.epoch}:${point.revisions.statistics}`
    const saved = cache.get(path.value)
    if (fresh(saved)) { data.value = saved; loading.value = false; error.value = ''; return }
    await load(signal, true)
  })
  onScopeDispose(() => { unsubscribe(); generation++; controller?.abort() })
  return { data, loading, error, refresh: () => load() }
}
