import { computed, ref, watch } from 'vue'

export type Appearance = 'system' | 'light' | 'dark'
const storageKey = 'vista-appearance'
const system = window.matchMedia('(prefers-color-scheme: dark)')
const systemDark = ref(system.matches)
function preference(value: string | null): Appearance {
  return value === 'light' || value === 'dark' ? value : 'system'
}
function savedAppearance(): Appearance {
  try { return preference(localStorage.getItem(storageKey)) }
  catch { return 'system' }
}

const appearance = ref<Appearance>(savedAppearance())
export const resolvedAppearance = computed(() => appearance.value === 'system' ? systemDark.value ? 'dark' : 'light' : appearance.value)
system.addEventListener('change', event => { systemDark.value = event.matches })
window.addEventListener('storage', event => {
  if (event.key === storageKey || event.key === null) appearance.value = preference(event.newValue)
})
watch(resolvedAppearance, value => {
  document.documentElement.dataset.theme = value
  document.documentElement.style.colorScheme = value
}, { immediate: true, flush: 'sync' })

export function useAppearance() {
  function setAppearance(value: Appearance) {
    appearance.value = value
    try { localStorage.setItem(storageKey, value) }
    catch { /* The selection still applies for this visit. */ }
  }
  return { appearance, resolvedAppearance, setAppearance }
}
