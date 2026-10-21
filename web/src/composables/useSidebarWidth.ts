import { computed, onMounted, onUnmounted, ref } from 'vue'

const storageKey = 'vista-sidebar-width'
const minWidth = 220
const maxWidth = 480

function readWidth(): number | null {
  try {
    const saved = document.cookie.split('; ').find(item => item.startsWith(storageKey + '='))?.slice(storageKey.length + 1)
      ?? localStorage.getItem(storageKey)
    const value = Number(saved)
    if (value > 0 && Number.isFinite(value)) return Math.max(minWidth, Math.min(maxWidth, value))
  } catch { /* Use the responsive default if browser storage is unavailable. */ }
  return null
}

export function useSidebarWidth() {
  const preferredWidth = ref(readWidth())
  const viewportWidth = ref(window.innerWidth)
  const desktop = computed(() => viewportWidth.value > 830)
  const maximum = computed(() => Math.max(minWidth, Math.min(maxWidth, viewportWidth.value - 480)))
  const width = computed(() => desktop.value
    ? Math.min(maximum.value, preferredWidth.value ?? (viewportWidth.value > 1100 ? 270 : 240)) : 0)
  function updateViewport() { viewportWidth.value = window.innerWidth }
  onMounted(() => window.addEventListener('resize', updateViewport))
  onUnmounted(() => window.removeEventListener('resize', updateViewport))

  function resize(sizes: number[]) {
    if (desktop.value && Number.isFinite(sizes[0])) preferredWidth.value = Math.round(Math.max(minWidth, Math.min(maximum.value, sizes[0]!)))
  }
  function persist() {
    try {
      if (preferredWidth.value === null) localStorage.removeItem(storageKey)
      else localStorage.setItem(storageKey, String(preferredWidth.value))
    } catch { /* Keep the width for this page. */ }
    document.cookie = `${storageKey}=${preferredWidth.value ?? ''}; Path=/; Max-Age=${preferredWidth.value === null ? 0 : 31536000}; SameSite=Strict`
  }
  function finishResize(sizes: number[]) { if (desktop.value) { resize(sizes); persist() } }
  function reset() { if (desktop.value) { preferredWidth.value = null; persist() } }
  return { desktop, width, minimum: minWidth, maximum, resize, finishResize, reset }
}
