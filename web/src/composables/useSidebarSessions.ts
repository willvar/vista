import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import { appendSessionPage, pageVersionAtLeast, readSessionWindow, sessionPagePath, sessionWindowFromPage,
  type SessionPage, type SessionPageQuery, type SessionWindow, type SessionWindowSnapshot, type VistaClient } from '@vista/client'

const batchSize = 20

export function useSidebarSessions(client: VistaClient, query: Readonly<Ref<SessionPageQuery>>) {
  const snapshot = shallowRef<SessionWindow>()
  const loading = ref(false)
  const failed = ref(false)
  const hasMore = computed(() => !!snapshot.value && snapshot.value.items.length < snapshot.value.total)
  const batchesLoaded = () => Math.max(1, Math.ceil((snapshot.value?.items.length || 0) / batchSize))
  const interest = computed(() => {
    const size = batchesLoaded() * batchSize
    if (size <= 100) return { size, sort: query.value.sort ?? 'created-desc', untitledLabel: query.value.untitledLabel }
  })
  let pending: { controller: AbortController; read: Promise<void> } | undefined
  let requiredVersion: string | undefined
  let retryAppend = false
  let generation = 0

  function cancel() {
    generation++
    pending?.controller.abort()
    pending = undefined
    loading.value = false
  }
  function load(append = false, parent?: AbortSignal) {
    if (pending) return pending.read
    if (parent?.aborted) return Promise.resolve()
    const attemptGeneration = ++generation
    const controller = new AbortController()
    const signal = parent ? AbortSignal.any([controller.signal, parent]) : controller.signal
    const old = snapshot.value
    const wanted = query.value
    const batches = batchesLoaded()
    loading.value = true
    failed.value = false
    retryAppend = append
    const read = (async () => {
      try {
        let next: SessionWindow | undefined
        if (append && old && old.items.length < old.total) {
          const page = await client.request<SessionPage>(sessionPagePath(batches + 1, batchSize, wanted), { signal })
          next = appendSessionPage(old, page)
        }
        const current = () => !!next && (!requiredVersion || pageVersionAtLeast(next.version, requiredVersion))
        // A mutation between pages invalidates the old boundary. Re-read the
        // now requested prefix rather than appending a shifted page.
        for (let attempt = 0; !signal.aborted && !current() && attempt < 2; attempt++) {
          next = await readSessionWindow(client, wanted, batches + Number(append), batchSize, signal)
        }
        if (signal.aborted) return
        if (next && current()) snapshot.value = next
        else failed.value = true
      } catch {
        if (!signal.aborted) failed.value = true
      } finally {
        if (generation === attemptGeneration) {
          pending = undefined
          loading.value = false
        }
      }
    })()
    pending = { controller, read }
    return read
  }

  async function synchronize(version: string | undefined, signal?: AbortSignal, pushed?: SessionWindowSnapshot) {
    if (version) requiredVersion = version
    if (pending) await pending.read
    if (signal?.aborted || (version && pageVersionAtLeast(snapshot.value?.version, version))) return
    const expected = interest.value
    if (pushed?.version === version && pushed && expected?.size === pushed.interest.size
      && expected.sort === pushed.interest.sort && expected.untitledLabel === pushed.interest.untitledLabel) {
      const window = sessionWindowFromPage(pushed)
      if (window) {
        snapshot.value = window
        failed.value = false
        return
      }
    }
    await load(false, signal)
  }

  watch(query, () => {
    cancel()
    snapshot.value = undefined
    void load()
  }, { flush: 'sync' })
  onScopeDispose(cancel)
  return { snapshot, interest, loading, failed, hasMore, synchronize, loadMore: () => { if (hasMore.value) void load(true) }, retry: () => { void load(retryAppend) } }
}
