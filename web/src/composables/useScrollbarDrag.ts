import { inject, nextTick, onScopeDispose, provide, readonly, ref, watch, type InjectionKey, type Ref } from 'vue'
import type { Virtualizer } from '@tanstack/vue-virtual'

const scrollbarDragKey: InjectionKey<Readonly<Ref<boolean>>> = Symbol('scrollbarDrag')

// Native scrollbar dragging does not deliver pointermove to the page. Keep its
// scroll range stable until pointerup instead of competing with the browser.
export function provideScrollbarDrag(scroller: Ref<HTMLElement | null>, start: () => void, end: () => void) {
  const dragging = ref(false)
  provide(scrollbarDragKey, readonly(dragging))
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

export function useScrollbarDrag() { return inject(scrollbarDragKey, readonly(ref(false))) }

export function useDragStableMeasurement(dragging: Readonly<Ref<boolean>>, getVirtualizer: () => Virtualizer<HTMLElement, Element>, afterChildren = false) {
  watch(dragging, async active => {
    if (active) return
    await nextTick()
    if (afterChildren) await nextTick()
    if (dragging.value) return
    const instance = getVirtualizer()
    if (instance.scrollElement) instance.scrollOffset = instance.scrollElement.scrollTop
    // measureElement skips synchronous reads while isScrolling is true. A
    // frozen ResizeObserver entry may not fire again, so explicitly resume the
    // currently mounted rows without discarding offscreen height caches.
    for (const element of instance.elementsCache.values()) {
      if (element.isConnected) instance.resizeItem(instance.indexFromElement(element), element.getBoundingClientRect().height)
    }
  }, { flush: 'post' })
  return (element: Element, entry: ResizeObserverEntry | undefined, instance: Virtualizer<HTMLElement, Element>) => {
    if (dragging.value) {
      const index = instance.indexFromElement(element)
      return instance.itemSizeCache.get(instance.options.getItemKey(index)) ?? instance.options.estimateSize(index)
    }
    return entry?.borderBoxSize[0]?.blockSize ?? element.getBoundingClientRect().height
  }
}
