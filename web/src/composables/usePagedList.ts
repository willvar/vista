import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import type { PageInfo } from '../client/types.ts'
import type { VistaClient } from '../client/vista.ts'
import type { createWorkspaceSync, StateTopic } from '../client/workspaceSync.ts'

function atLeast(value: string | undefined, expected: string) {
  const [epoch, revision] = value?.split(':') ?? []
  const [expectedEpoch, expectedRevision] = expected.split(':')
  return epoch === expectedEpoch && Number.isSafeInteger(Number(revision)) && Number(revision) >= Number(expectedRevision)
}

export function usePagedList<T extends PageInfo>(options: {
  client: VistaClient
  sync: ReturnType<typeof createWorkspaceSync>
  topic: StateTopic
  page: Ref<number>
  path: (page: number) => string
  enabled?: () => boolean
  message?: (error: unknown) => string
}) {
  const data = shallowRef<T>()
  const loading = ref(true)
  const error = ref('')
  const path = computed(() => options.path(options.page.value))
  const enabled = computed(() => options.enabled?.() ?? true)
  let loadedPath = ''
  let generation = 0
  let controller: AbortController | undefined
  let pending: { path: string; promise: Promise<void> } | undefined
  let unsubscribe: (() => void) | undefined

  function cancel() {
    generation++
    controller?.abort()
    pending = undefined
    loading.value = false
  }
  function apply(value: T) {
    if (loadedPath === options.path(value.page) && data.value?.version !== value.version && atLeast(data.value?.version, value.version)) return
    cancel()
    data.value = value
    loadedPath = options.path(value.page)
    options.page.value = value.page
    error.value = ''
  }
  async function read(signal?: AbortSignal, propagate = false) {
    if (!enabled.value) return
    const requested = path.value
    if (pending?.path === requested) {
      try { await pending.promise } catch (cause) { if (propagate) throw cause }
      return
    }
    cancel()
    const version = generation
    const owner = controller = new AbortController()
    const requestSignal = signal ? AbortSignal.any([signal, owner.signal]) : owner.signal
    loading.value = true
    error.value = ''
    const promise = (async () => {
      try {
        const value = await options.client.request<T>(requested, { signal: requestSignal })
        if (!requestSignal.aborted && version === generation && requested === path.value) apply(value)
      } catch (cause) {
        if (!requestSignal.aborted && version === generation) {
          error.value = options.message?.(cause) ?? String(cause)
          throw cause
        }
      } finally {
        if (version === generation) { pending = undefined; loading.value = false }
      }
    })()
    pending = { path: requested, promise }
    try { await promise } catch (cause) { if (propagate) throw cause }
  }
  watch(path, () => {
    if (enabled.value && loadedPath !== path.value) void read()
  })
  watch(enabled, active => {
    unsubscribe?.()
    if (!active) { cancel(); return }
    loading.value = true
    unsubscribe = options.sync.subscribe([options.topic], async (_, signal, point) => {
      const version = `${point.epoch}:${point.revisions[options.topic]}`
      if (atLeast(data.value?.version, version) && loadedPath === path.value) { loading.value = false; return }
      await read(signal, true)
    })
  }, { immediate: true, flush: 'sync' })
  onScopeDispose(() => { unsubscribe?.(); cancel() })
  return { data, loading, error, apply, cancel, refresh: () => read(), ready: computed(() => !!data.value && loadedPath === path.value) }
}
