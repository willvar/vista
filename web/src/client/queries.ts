import { QueryClient } from '@tanstack/vue-query'
import type { VistaClient } from './vista.ts'

const clients = new WeakMap<VistaClient, QueryClient>()

// SSE owns freshness and recovery. Query owns shared HTTP reads and their
// lifetime; focus, reconnect and timers must not start another sync path.
export function queriesFor(client: VistaClient) {
  let queries = clients.get(client)
  if (!queries) {
    queries = new QueryClient({ defaultOptions: { queries: {
      staleTime: Infinity,
      retry: false,
      networkMode: 'always',
      refetchOnMount: false,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
      structuralSharing: false,
    } } })
    clients.set(client, queries)
  }
  return queries
}
