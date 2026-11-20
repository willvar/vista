import { computed, ref, watch } from 'vue'
import type { Session } from '@vista/client'

type QuestionMode = 'answers' | 'text'

// Presentation preferences never consume pending input or call the server.
export function useQuestionComposer(session: () => Session | null | undefined, inputProtected: () => boolean) {
  const pending = computed(() => {
    const value = session()?.pendingReply
    return value?.kind === 'question' && value.questions?.length ? value : null
  })
  const requestKey = computed(() => pending.value
    ? `vista-question-mode:${session()!.id}:${pending.value.runId}:${pending.value.callId || 'legacy'}` : '')
  const mode = ref<QuestionMode>('text')
  function remember(value: QuestionMode) {
    mode.value = value
    if (requestKey.value) {
      try { sessionStorage.setItem(requestKey.value, value) } catch { /* Keep the current mode in memory. */ }
    }
  }
  watch([requestKey, () => session()?.id], ([key, id], previous) => {
    const [oldKey, oldID] = previous || []
    if (oldKey && oldKey !== key && oldID === id) {
      try { sessionStorage.removeItem(oldKey) } catch { /* Pending identity remains authoritative. */ }
    }
    if (!key) { mode.value = 'text'; return }
    let saved: string | null = null
    try { saved = sessionStorage.getItem(key) } catch { /* Default to a usable local mode. */ }
    // An explicit session switch must not mistake the old editor for the new one.
    const protectedHere = (!oldID || oldID === id) && inputProtected()
    remember(protectedHere || saved === 'text' ? 'text' : 'answers')
  }, { immediate: true, flush: 'sync' })
  return {
    pending, requestKey,
    answering: computed(() => !!pending.value && mode.value === 'answers'),
    showText: () => remember('text'),
    showAnswers: () => { if (pending.value) remember('answers') },
  }
}
