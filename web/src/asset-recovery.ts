import { ref } from 'vue'
import type { Router } from 'vue-router'

export const assetLoadFailed = ref(false)
const failures = new WeakSet<object>()

function record(error: unknown) {
  if (error && typeof error === 'object') failures.add(error)
  assetLoadFailed.value = true
}

export function captureAssetError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error)
  if (!(error && typeof error === 'object' && failures.has(error)) && !/Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS/i.test(message)) return false
  record(error)
  return true
}

export function installAssetRecovery(router: Router) {
  // Keep the old view and its editors mounted. A reload is always deliberate.
  window.addEventListener('vite:preloadError', event => record((event as Event & { payload: unknown }).payload))
  window.addEventListener('vista:asset-error', event => record((event as CustomEvent).detail))
  window.addEventListener('unhandledrejection', event => {
    if (captureAssetError(event.reason)) event.preventDefault()
  })
  router.onError(error => { if (!captureAssetError(error)) console.error(error) })
}

export function showBootFailure(error: unknown) {
  console.error(error)
  const root = document.getElementById('app')!
  const message = document.createElement('p')
  message.setAttribute('role', 'alert')
  message.textContent = 'Vista could not load. Check the connection, then reload the page.'
  const reload = document.createElement('button')
  reload.textContent = 'Reload'
  reload.onclick = () => window.location.reload()
  root.replaceChildren(message, reload)
}
