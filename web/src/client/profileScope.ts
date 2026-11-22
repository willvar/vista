import { createVistaClient, type VistaClient } from '@vista/client'

export const profileIdPattern = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
const profileIdFormat = new RegExp(`^${profileIdPattern}$`)
export function routeProfileId({ profilePrefix, profileId }: { profilePrefix?: unknown; profileId?: unknown }) {
  return profilePrefix === 'profiles' && typeof profileId === 'string' && profileIdFormat.test(profileId)
    ? profileId : 'default'
}
export function profilePath(id: string, path: string) {
  return id === 'default' ? path : `/profiles/${encodeURIComponent(id)}${path}`
}
export function profileHome(id: string) { return profilePath(id, '/') }
export function profileStorageKey(key: string, profileId = 'default') { return profileId === 'default' ? key : `${key}:${profileId}` }
const deviceEndpoints = new Set(['/profiles', '/server-config', '/restart', '/stop', '/reset', '/model-catalog'])

// Navigation chooses the API base. Both clients use the SDK's own HTTP and
// event transport; only the device-wide endpoints use the unscoped base.
export function createProfileClient(language: () => string, profileId: string): VistaClient {
  const device = createVistaClient({ language })
  if (profileId === 'default') return device
  const scoped = createVistaClient({ baseURL: location.origin + profilePath(profileId, ''), language })
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
