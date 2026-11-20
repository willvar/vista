import { createVistaClient, type VistaClient } from '@vista/client'

// A tab's Profile is part of its address. Switching uses ordinary navigation
// so in-flight requests and revision cursors never move to a different Profile.
export const profileBase = typeof window === 'undefined' ? '' : /^\/profiles\/[0-9a-f-]{36}(?=\/|$)/.exec(location.pathname)?.[0] || ''
export const profileID = profileBase ? profileBase.slice('/profiles/'.length) : 'default'
export function profileHome(id: string) { return id === 'default' ? '/' : `/profiles/${encodeURIComponent(id)}/` }
export function profileStorageKey(key: string) { return profileBase ? `${key}:${profileID}` : key }
const deviceEndpoints = new Set(['/profiles', '/server-config', '/restart', '/stop', '/reset', '/model-catalog'])

// Navigation chooses the API base. Both clients use the SDK's own HTTP and
// event transport; only the device-wide endpoints use the unscoped base.
export function createProfileClient(language: () => string): VistaClient {
  const device = createVistaClient({ language })
  if (!profileBase) return device
  const scoped = createVistaClient({ baseURL: location.origin + profileBase, language })
  const forPath = (path: string) => {
    const route = path.split('?')[0] || ''
    return deviceEndpoints.has(route) || route.startsWith('/profiles/') ? device : scoped
  }
  return {
    ...scoped,
    url: path => forPath(path).url(path),
    request: <T>(path: string, options?: RequestInit) => forPath(path).request<T>(path, options),
    requestStream: <T>(path: string, options: RequestInit & { signal: AbortSignal }) => forPath(path).requestStream<T>(path, options),
    image: (path, signal) => forPath(path).image(path, signal),
  }
}
