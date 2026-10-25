import { ref, watch } from 'vue'
import type { VistaClient } from '../client/vista'

export type SelectionMode = 'manual' | 'auto'
export type SelectionTier = 'speed' | 'quality'
export type RememberedModel = { key: string; id: string; reasoningEffort: string }
export type UserPreferences = {
  userId: string; lastMode: SelectionMode; lastTier: SelectionTier
  manualModelKey: string; reasoningByModelId: Record<string, string>; feedbackInvitationSeen: boolean
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

export function useInteractionPreferences(client?: VistaClient, report: (error: unknown) => void = () => {}) {
  const rememberedModel = ref<RememberedModel | null>(readJSON('vista-last-model', null))
  const lastMode = ref<SelectionMode>('manual')
  const lastTier = ref<SelectionTier>('speed')
  const reasoningByModelId = ref<Record<string, string>>(readJSON('vista-reasoning-by-model', {}))
  const feedbackInvitationSeen = ref(false)
  const ready = ref(!client)
  let hydrating = false
  let saving = Promise.resolve()
  function persist(patch: Partial<UserPreferences>) {
    if (!client || hydrating || !ready.value) return
    saving = saving.then(async () => { await client.request('/preferences', { method: 'PATCH', body: JSON.stringify(patch) }) }).catch(report)
  }
  watch(rememberedModel, (value, previous) => {
    if (value) {
      savePreference('vista-last-model', JSON.stringify(value))
      if (value.key !== previous?.key) persist({ manualModelKey: value.key })
    }
  }, { flush: 'sync' })
  watch(lastMode, value => persist({ lastMode: value }), { flush: 'sync' })
  watch(lastTier, value => persist({ lastTier: value }), { flush: 'sync' })
  watch(reasoningByModelId, value => {
    savePreference('vista-reasoning-by-model', JSON.stringify(value)); persist({ reasoningByModelId: value })
  }, { flush: 'sync' })
  watch(feedbackInvitationSeen, value => persist({ feedbackInvitationSeen: value }), { flush: 'sync' })
  async function load() {
    if (!client || ready.value) return
    try {
      const saved = await client.request<UserPreferences>('/preferences')
      hydrating = true
      lastMode.value = saved.lastMode === 'auto' ? 'auto' : 'manual'
      lastTier.value = saved.lastTier === 'quality' ? 'quality' : 'speed'
      rememberedModel.value = saved.manualModelKey ? { key: saved.manualModelKey, id: '', reasoningEffort: '' } : null
      reasoningByModelId.value = saved.reasoningByModelId || {}
      feedbackInvitationSeen.value = !!saved.feedbackInvitationSeen
    } catch (error) { report(error) }
    finally { hydrating = false; ready.value = true }
  }
  return { rememberedModel, lastMode, lastTier, reasoningByModelId, feedbackInvitationSeen, ready, load, flush: () => saving }
}
export type InteractionPreferences = ReturnType<typeof useInteractionPreferences>
