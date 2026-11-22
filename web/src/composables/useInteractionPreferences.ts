import { profileStorageKey } from '../client/profileScope.ts'
import { ref, watch } from 'vue'
import type { VistaClient } from '@vista/client'

export type RememberedModel = { key: string; id: string; reasoningEffort: string }
export type UserPreferences = {
  userId: string
  manualModelKey: string; reasoningByModelId: Record<string, string>
}

function readPreference(key: string): string | null {
  try {
    const cookie = typeof document === 'undefined' ? undefined
      : document.cookie.split('; ').find(item => item.startsWith(key + '='))?.slice(key.length + 1)
    return cookie !== undefined ? decodeURIComponent(cookie) : localStorage.getItem(key)
  } catch { return null }
}
function savePreference(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* Keep the choice in this page. */ }
  if (typeof document !== 'undefined') document.cookie = `${key}=${encodeURIComponent(value)}; Path=/; Max-Age=31536000; SameSite=Strict`
}
function readJSON<T>(key: string, fallback: T): T {
  try { return JSON.parse(readPreference(key) || 'null') ?? fallback } catch { return fallback }
}

export function useInteractionPreferences(client?: VistaClient, report: (error: unknown) => void = () => {}, profileId = 'default') {
  const rememberedModel = ref<RememberedModel | null>(readJSON(profileStorageKey('vista-last-model', profileId), null))
  const reasoningByModelId = ref<Record<string, string>>(readJSON(profileStorageKey('vista-reasoning-by-model', profileId), {}))
  const ready = ref(!client)
  let hydrating = false
  let saving = Promise.resolve()
  function persist(patch: Partial<UserPreferences>) {
    if (!client || hydrating || !ready.value) return
    saving = saving.then(async () => { await client.request('/preferences', { method: 'PATCH', body: JSON.stringify(patch) }) }).catch(report)
  }
  watch(rememberedModel, (value, previous) => {
    if (value) {
      savePreference(profileStorageKey('vista-last-model', profileId), JSON.stringify(value))
      if (value.key !== previous?.key) persist({ manualModelKey: value.key })
    }
  }, { flush: 'sync' })
  watch(reasoningByModelId, value => {
    savePreference(profileStorageKey('vista-reasoning-by-model', profileId), JSON.stringify(value)); persist({ reasoningByModelId: value })
  }, { flush: 'sync' })
  async function load(signal?: AbortSignal) {
    if (!client || ready.value) return
    try {
      const saved = await client.request<UserPreferences>('/preferences', { signal })
      if (signal?.aborted) return
      hydrating = true
      rememberedModel.value = saved.manualModelKey ? { key: saved.manualModelKey, id: '', reasoningEffort: '' } : null
      reasoningByModelId.value = saved.reasoningByModelId || {}
    } catch (error) { if (!signal?.aborted) report(error) }
    finally { hydrating = false; ready.value = true }
  }
  return { rememberedModel, reasoningByModelId, ready, load, flush: () => saving }
}
export type InteractionPreferences = ReturnType<typeof useInteractionPreferences>
