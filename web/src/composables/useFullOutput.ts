import { onScopeDispose, ref, watch } from 'vue'
import type { OutputView } from '../client/types.ts'
import type { VistaClient } from '../client/vista.ts'

type Pending = { controller: AbortController; users: number; promise: Promise<OutputView> }
type OutputCache = { loaded: Map<string, { snapshot: string; output: OutputView }>; pending: Map<string, Pending>; latest: Map<string, string> }
const caches = new WeakMap<VistaClient, OutputCache>()
const maxCachedOutputs = 32
const maxCachedTextBytes = 8 * 1024 * 1024

function retain(cache: OutputCache, path: string, snapshot: string, output: OutputView) {
  cache.loaded.delete(path)
  if (output.content.length * 2 > maxCachedTextBytes) return
  cache.loaded.set(path, { snapshot, output })
  // Bound retained text, not the response itself. An open viewer still has its
  // complete output even when a large entry cannot remain in the shared cache.
  let bytes = [...cache.loaded.values()].reduce((total, entry) => total + entry.output.content.length * 2, 0)
  for (const [key, entry] of cache.loaded) {
    if (cache.loaded.size <= maxCachedOutputs && bytes <= maxCachedTextBytes) break
    cache.loaded.delete(key)
    bytes -= entry.output.content.length * 2
  }
}

export function useFullOutput(client: VistaClient, options: { path: string; snapshot: string; enabled: boolean }) {
  let cache = caches.get(client)
  if (!cache) { cache = { loaded: new Map(), pending: new Map(), latest: new Map() }; caches.set(client, cache) }
  const shared = cache
  const output = ref<OutputView | null>(null)
  const loading = ref(false)
  const failure = ref('')
  let revision = 0
  let previousPath = ''
  let release: (() => void) | undefined

  async function load() {
    const current = ++revision
    release?.(); release = undefined
    const { path, snapshot, enabled } = options
    if (path !== previousPath) output.value = null
    previousPath = path
    loading.value = false
    failure.value = ''
    if (!enabled || !path) { output.value = null; return }
    const loaded = shared.loaded.get(path)
    if (loaded?.snapshot === snapshot) {
      shared.loaded.delete(path); shared.loaded.set(path, loaded)
      output.value = loaded.output; return
    }
    const key = `${path}\n${snapshot}`
    let pending = shared.pending.get(key)
    if (!pending) {
      const controller = new AbortController()
      shared.latest.set(path, key)
      pending = { controller, users: 0, promise: client.request<OutputView>(path, { signal: controller.signal }).then(result => {
        if (!controller.signal.aborted && shared.latest.get(path) === key) retain(shared, path, snapshot, result)
        return result
      }).finally(() => {
        if (shared.pending.get(key)?.controller === controller) {
          shared.pending.delete(key)
          if (shared.latest.get(path) === key) shared.latest.delete(path)
        }
      }) }
      shared.pending.set(key, pending)
    }
    const request = pending
    request.users++
    let released = false
    const leave = () => {
      if (released) return
      released = true
      if (--request.users === 0 && shared.pending.get(key) === request) {
        request.controller.abort()
        shared.pending.delete(key)
        if (shared.latest.get(path) === key) shared.latest.delete(path)
      }
    }
    release = leave
    loading.value = true
    try {
      const result = await request.promise
      if (current === revision && !request.controller.signal.aborted) output.value = result
    } catch (cause) {
      if (current === revision && !request.controller.signal.aborted) failure.value = String(cause)
    } finally {
      leave()
      if (current === revision) { loading.value = false; release = undefined }
    }
  }
  watch(() => [options.path, options.snapshot, options.enabled], () => { void load() }, { immediate: true })
  onScopeDispose(() => { revision++; release?.(); output.value = null })
  return { output, loading, failure, retry: load }
}
