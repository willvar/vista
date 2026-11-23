import { computed, onScopeDispose, ref } from 'vue'
import {
  useRouter,
  type LocationQuery, type RouteLocationNormalized, type RouteLocationNormalizedLoaded,
  type RouteLocationRaw, type Router,
} from 'vue-router'

// The URL records the visible layers. Vue Router owns traversal and guard
// cancellation; this module never creates an additional in-memory history.
export function layerTokens(route: Pick<RouteLocationNormalized, 'query'>): string[] {
  const values = route.query.layer
  return (Array.isArray(values) ? values : [values]).filter((value): value is string => typeof value === 'string' && value.length > 0)
}

export function withoutLayers({ layer: _, ...query }: LocationQuery) { return query }

export function withLayers(route: Pick<RouteLocationNormalized, 'path' | 'query' | 'hash'>, layers: string[]): RouteLocationRaw {
  return { path: route.path, query: { ...withoutLayers(route.query), layer: layers.length ? layers : undefined }, hash: route.hash }
}

// router.back() doesn't return a promise. Keep one request pending for the
// exact entry, including while a leave guard waits for a user decision.
const closing = new WeakSet<Router>()
export function returnToParent(router: Router, parent: RouteLocationRaw, entry = router.currentRoute.value) {
  // A pending browser traversal has already moved the platform's cursor even
  // if Vue Router is still waiting on a guard for the previous entry.
  if (router.currentRoute.value !== entry || router.options.history.location !== entry.fullPath || closing.has(router)) return
  const target = router.resolve(parent)
  if (target.fullPath === entry.fullPath) return
  closing.add(router)
  const stopAfter = router.afterEach(release)
  const stopError = router.onError(release)
  function release() { closing.delete(router); stopAfter(); stopError() }
  if (router.options.history.state.back === target.fullPath) router.back()
  else void router.replace(parent)
}

export function useRouteLayer(name: string, inScope: (route: RouteLocationNormalized) => boolean) {
  const router = useRouter()
  const prefix = name + ':'
  const tokenIn = (route: RouteLocationNormalized) => inScope(route)
    ? layerTokens(route).find(token => token.startsWith(prefix)) : undefined
  const current = computed(() => tokenIn(router.currentRoute.value)?.slice(prefix.length))

  function open(id = '', replace = false) {
    const route = router.currentRoute.value
    if (!inScope(route) || closing.has(router)) return
    const stack = layerTokens(route)
    const token = prefix + id
    const existing = tokenIn(route)
    if (existing === token || existing && existing !== stack.at(-1)) return
    const replacing = !!existing || replace && stack.length > 0
    const location = withLayers(route, [...(replacing ? stack.slice(0, -1) : stack), token])
    return replacing ? router.replace(location) : router.push(location)
  }

  function parent(entry: RouteLocationNormalizedLoaded) {
    const stack = layerTokens(entry)
    if (!stack.length || tokenIn(entry) !== stack.at(-1)) return
    return withLayers(entry, stack.slice(0, -1))
  }

  function captureClose() {
    const entry = router.currentRoute.value
    const target = parent(entry)
    return () => { if (target) returnToParent(router, target, entry) }
  }

  function close() { captureClose()() }
  // Completing a picker is navigation to a destination, not a new step
  // above the picker. If the destination is already underneath, consume it.
  function complete(destination: RouteLocationRaw) {
    const entry = router.currentRoute.value
    const previous = parent(entry)
    if (!previous || closing.has(router) || router.options.history.location !== entry.fullPath) return
    if (router.resolve(previous).fullPath === router.resolve(destination).fullPath) return returnToParent(router, previous, entry)
    return router.replace(destination)
  }

  function drop() {
    const route = router.currentRoute.value
    const token = tokenIn(route)
    const stack = layerTokens(route)
    if (token) void router.replace(withLayers(route, stack.slice(0, stack.indexOf(token))))
  }

  function onLeave(guard: (to: RouteLocationNormalized, from: RouteLocationNormalized) => boolean | Promise<boolean>) {
    function check(to: RouteLocationNormalized, from: RouteLocationNormalized) {
      const token = tokenIn(from)
      if (!token || from.path === to.path && from.query.session === to.query.session && token === tokenIn(to)) return true
      return guard(to, from)
    }
    // The same page component can be reused across different route records
    // (e.g. settings categories). Bind to its lifetime, not its first record.
    onScopeDispose(router.beforeEach(check))
  }

  return { current, open, close, captureClose, complete, drop, onLeave }
}

// The confirmation belongs to the pending action, not to browser history.
export function useLeaveConfirmation() {
  const pending = ref(false)
  let resolve: ((allow: boolean) => void) | undefined
  let decision: Promise<boolean> | undefined
  function ask() {
    if (decision) return decision
    pending.value = true
    return decision = new Promise<boolean>(done => { resolve = done })
  }
  function finish(allow: boolean) {
    const done = resolve
    resolve = undefined
    decision = undefined
    pending.value = false
    done?.(allow)
  }
  onScopeDispose(() => finish(false))
  return { pending, ask, finish }
}
