import { computed } from 'vue'
import { useQuery, type QueryClient } from '@tanstack/vue-query'
import type { OutputView } from '../client/types.ts'
import type { VistaClient } from '../client/vista.ts'
import { queriesFor } from '../client/queries.ts'
import { useHistoryRead } from './useConversationHistory.ts'

const bounded = new WeakSet<QueryClient>()
const maxCachedOutputs = 32
const maxCachedTextBytes = 8 * 1024 * 1024

function boundOutputs(client: QueryClient) {
  if (bounded.has(client)) return
  bounded.add(client)
  const cache = client.getQueryCache()
  const used = new WeakMap<object, number>()
  let clock = 0
  cache.subscribe(event => {
    if (event.query.queryKey[0] !== 'full-output') return
    if (event.type === 'observerAdded' || event.type === 'updated' && event.action.type === 'success') used.set(event.query, ++clock)
    if (event.type !== 'observerRemoved' && !(event.type === 'updated' && event.action.type === 'success')) return
    const entries = cache.findAll({ queryKey: ['full-output'] }).filter(query => query.state.data)
      .sort((a, b) => (used.get(a) || 0) - (used.get(b) || 0))
    let bytes = entries.reduce((total, query) => total + (query.state.data as OutputView).content.length * 2, 0)
    let count = entries.length
    for (const query of entries) {
      if (count <= maxCachedOutputs && bytes <= maxCachedTextBytes) break
      // Open viewers retain complete text, including a single oversized body.
      // Query owns request sharing and cancels only after its last viewer leaves.
      if (query.getObserversCount()) continue
      bytes -= (query.state.data as OutputView).content.length * 2
      count--
      cache.remove(query)
    }
  })
}

export function useFullOutput(client: VistaClient, options: { path: string; snapshot: string; enabled: boolean }) {
  const queries = queriesFor(client)
  boundOutputs(queries)
  const enabled = computed(() => options.enabled && !!options.path)
  const query = useQuery(() => ({
    queryKey: ['full-output', enabled.value ? options.path : '', enabled.value ? options.snapshot : ''],
    enabled: enabled.value,
    queryFn: ({ queryKey, signal }) => client.request<OutputView>(queryKey[1]!, { signal }),
    placeholderData: (previous, source) => enabled.value && source?.queryKey[1] === options.path ? previous : undefined,
    shallow: true,
  }), queries)
  const output = computed(() => enabled.value ? query.data.value ?? null : null)
  const loading = computed(() => enabled.value && query.isFetching.value)
  const failure = computed(() => enabled.value && query.error.value ? String(query.error.value) : '')
  useHistoryRead(loading)
  return { output, loading, failure, retry: () => query.refetch() }
}
