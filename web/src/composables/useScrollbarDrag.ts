import { onScopeDispose, readonly, ref, watch, type Ref } from 'vue'

// Native scrollbar dragging does not deliver pointermove to the page. Keep its
// scroll range stable until pointerup instead of competing with the browser.
export function useScrollbarDrag(scroller: Ref<HTMLElement | null>, start: () => void, end: () => void) {
  const dragging = ref(false)
  function finish() {
    if (!dragging.value) return
    end()
    dragging.value = false
  }
  function pointerDown(event: PointerEvent) {
    const element = scroller.value
    if (!element || event.target !== element || event.button !== 0 || event.pointerType !== 'mouse'
      || element.scrollHeight <= element.clientHeight) return
    const bounds = element.getBoundingClientRect()
    const contentLeft = bounds.left + element.clientLeft
    const contentRight = contentLeft + element.clientWidth
    const gutter = element.offsetWidth - element.clientWidth
    // Overlay scrollbars do not reserve a gutter; their hit target is still
    // the scroller itself, at its edge. Content clicks must keep normal behavior.
    const inGutter = gutter > 0 ? event.clientX < contentLeft || event.clientX >= contentRight
      : event.clientX >= bounds.right - 16
    if (!inGutter) return
    start()
    dragging.value = true
  }
  document.addEventListener('pointerup', finish, true)
  document.addEventListener('pointercancel', finish, true)
  window.addEventListener('blur', finish)
  watch(scroller, () => finish())
  onScopeDispose(() => {
    document.removeEventListener('pointerup', finish, true)
    document.removeEventListener('pointercancel', finish, true)
    window.removeEventListener('blur', finish)
  })
  return { dragging: readonly(dragging), pointerDown, finish }
}
