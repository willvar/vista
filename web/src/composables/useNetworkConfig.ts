import { onScopeDispose, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import type { VistaClient, createWorkspaceSync, NetworkConfiguration, NetworkSettings } from '@vista/client'
import { networkInput } from '../client/network.ts'

type Dependencies = { client: VistaClient; stateSync: ReturnType<typeof createWorkspaceSync> }
type SnapshotRead = { controller: AbortController; readers: number; promise: Promise<void> }
function createStore(client: VistaClient) {
  const value = shallowRef<NetworkConfiguration>()
  const error = ref(''), loading = ref(false)
  let generation = 0, pending: SnapshotRead | undefined
  function accept(next: NetworkConfiguration) {
    const [epoch, version] = next.version.split(':'), [oldEpoch, oldVersion] = value.value?.version.split(':') ?? []
    if (epoch === oldEpoch && Number(version) < Number(oldVersion)) return
    if (value.value?.revision === next.revision && value.value.version === next.version) return
    generation++; value.value = next; error.value = ''; loading.value = false
  }
  async function read(signal?: AbortSignal) {
    if (signal?.aborted) return
    if (!pending || pending.controller.signal.aborted) {
      const ticket = ++generation; loading.value = true; error.value = ''
      const request: SnapshotRead = { controller: new AbortController(), readers: 0, promise: Promise.resolve() }
      const sharedSignal = AbortSignal.any([request.controller.signal, AbortSignal.timeout(10_000)])
      request.promise = client.request<NetworkConfiguration>('/network-config', { signal: sharedSignal }).then(next => {
        if (!sharedSignal.aborted && ticket === generation) accept(next)
      }).catch(cause => {
        if (ticket !== generation) return
        if (!request.controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause)
        throw cause
      }).finally(() => {
        if (ticket === generation) loading.value = false
        if (pending === request) pending = undefined
      })
      pending = request
    }
    const request = pending
    request.readers++
    let released = false
    const release = () => {
      if (released) return
      released = true
      if (--request.readers === 0) {
        request.controller.abort()
        if (pending === request) pending = undefined
      }
    }
    signal?.addEventListener('abort', release, { once: true })
    try { await request.promise }
    catch (cause) { if (signal && !signal.aborted) throw cause }
    finally { signal?.removeEventListener('abort', release); release() }
  }
  async function save(config: NetworkSettings, revision: string, reuseCredentials: string[] = []) {
    const next = await client.request<NetworkConfiguration>('/network-config', { method: 'PUT', body: JSON.stringify({ config: networkInput(config), revision, reuseCredentials }) })
    accept(next)
  }
  return { value, error, loading, accept, read, save }
}
const stores = new WeakMap<VistaClient, ReturnType<typeof createStore>>()
export function useNetworkConfig(dependencies: Dependencies, active: MaybeRefOrGetter<boolean> = true) {
  let store = stores.get(dependencies.client)
  if (!store) { store = createStore(dependencies.client); stores.set(dependencies.client, store) }
  const state = store
  let unsubscribe: (() => void) | undefined
  watch(() => toValue(active), enabled => {
    unsubscribe?.(); unsubscribe = undefined
    if (!enabled) return
    unsubscribe = dependencies.stateSync.subscribe(['network'], async (_, signal, point) => {
      if (point.network) state.accept(point.network)
      else await state.read(signal)
    })
  }, { immediate: true })
  onScopeDispose(() => unsubscribe?.())
  return state
}
