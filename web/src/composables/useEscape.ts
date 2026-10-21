import { watch, type Ref } from 'vue'

export function isPlainEscape(event: KeyboardEvent) {
  return event.key === 'Escape' && !event.defaultPrevented && !event.repeat
    && !event.isComposing && event.keyCode !== 229
    && !event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
}

// Consume dismissal before the workspace can use the same Escape to stop a run.
export function useEscapeDismiss(open: Ref<boolean>) {
  watch(open, (visible, _, onCleanup) => {
    if (!visible) return
    function dismiss(event: KeyboardEvent) {
      if (!isPlainEscape(event)) return
      event.preventDefault()
      event.stopPropagation()
      open.value = false
    }
    document.addEventListener('keydown', dismiss)
    onCleanup(() => document.removeEventListener('keydown', dismiss))
  }, { immediate: true, flush: 'sync' })
}
