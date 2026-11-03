import { onScopeDispose, ref, shallowRef } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import type { createWorkspaceSync, StateTopic } from '../client/workspaceSync.ts'

type Versioned = { version?: string }
const caches = new WeakMap<VistaClient, Map<string, { value: Versioned; at: number }>>()
function atLeast(value: string | undefined, target: string) {
  const [epoch, ...revisions] = value?.split(':') ?? []
  const [expectedEpoch, ...expected] = target.split(':')
  return epoch === expectedEpoch && revisions.length === expected.length
    && revisions.every((revision, i) => Number.isSafeInteger(Number(revision)) && Number(revision) >= Number(expected[i]))
}

// Revision-only topics keep inventories scoped to their visible view. Reads
// are shared, canceled on close and never overwrite a newer mutation result.
export function useStateQuery<T extends Versioned>(client: VistaClient, sync: ReturnType<typeof createWorkspaceSync>, path: string, topics: StateTopic[], maxAgeMs = Infinity) {
  const cache = caches.get(client) ?? new Map<string, { value: Versioned; at: number }>()
  caches.set(client, cache)
  const data = shallowRef<T>()
  const loading = ref(true), error = ref('')
  let generation = 0, controller: AbortController | undefined
  let pending: { signal: AbortSignal; promise: Promise<void> } | undefined
  let target = ''
  function cancel() { generation++; controller?.abort(); pending = undefined; loading.value = false }
  function apply(value: T) {
    const previous = cache.get(path)?.value as T | undefined
    if (previous?.version !== value.version && value.version && atLeast(previous?.version, value.version)) return
    cancel(); cache.set(path, { value, at: Date.now() }); data.value = value; error.value = ''
  }
  async function load(signal?: AbortSignal, propagate = false) {
    if (pending && !pending.signal.aborted) {
      try { await pending.promise } catch (cause) { if (propagate) throw cause }
      return
    }
    cancel()
    const current = generation, fallbackVersion = target
    const owner = controller = new AbortController()
    const requestSignal = signal ? AbortSignal.any([signal, owner.signal]) : owner.signal
    loading.value = true; error.value = ''
    const promise = (async () => {
      try {
        const value = await client.request<T>(path, { signal: requestSignal })
        if (!requestSignal.aborted && current === generation) apply({ ...value, version: value.version ?? fallbackVersion })
      } catch (cause) {
        if (!requestSignal.aborted && current === generation) { error.value = String(cause); throw cause }
      } finally { if (current === generation) { pending = undefined; loading.value = false } }
    })()
    pending = { signal: requestSignal, promise }
    try { await promise } catch (cause) { if (propagate) throw cause }
  }
  const unsubscribe = sync.subscribe(topics, async (_, signal, point) => {
    target = `${point.epoch}:${topics.map(topic => point.revisions[topic]).join(':')}`
    const saved = cache.get(path)
    if (saved && Date.now() - saved.at < maxAgeMs && atLeast(saved.value.version, target)) { data.value = saved.value as T; loading.value = false; error.value = ''; return }
    await load(signal, true)
  })
  onScopeDispose(() => { unsubscribe(); cancel() })
  return { data, loading, error, apply, cancel, refresh: () => load() }
}
