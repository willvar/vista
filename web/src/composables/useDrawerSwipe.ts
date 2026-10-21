import { onMounted, onUnmounted, watch, type Ref } from 'vue'

type Gesture = {
  id: number; startX: number; startY: number; initialOffset: number; width: number
  axis: 'pending' | 'horizontal' | 'ignored'; offset: number
  samples: Array<{ x: number; time: number }>
}

export function useDrawerSwipe(panel: Ref<HTMLElement | null>, backdrop: Ref<HTMLElement | null>, open: Ref<boolean>, desktop: Ref<boolean>) {
  let gesture: Gesture | null = null
  let suppressClickUntil = 0

  function reset() {
    const previous = gesture
    gesture = null
    if (previous && panel.value?.hasPointerCapture(previous.id)) panel.value.releasePointerCapture(previous.id)
    panel.value?.classList.remove('mobile-dragging')
    backdrop.value?.classList.remove('mobile-dragging')
    panel.value?.style.removeProperty('--drawer-offset')
    backdrop.value?.style.removeProperty('--drawer-scrim-opacity')
  }
  function start(event: PointerEvent) {
    if (gesture && gesture.id !== event.pointerId) { reset(); return }
    suppressClickUntil = 0
    if (desktop.value || !open.value || !panel.value || !event.isPrimary || event.button !== 0
      || (event.target instanceof Element && event.target.closest('input, textarea, [contenteditable="true"]'))) return
    const bounds = panel.value.getBoundingClientRect()
    gesture = { id: event.pointerId, startX: event.clientX, startY: event.clientY,
      initialOffset: Math.min(0, bounds.left), offset: Math.min(0, bounds.left), width: bounds.width,
      axis: 'pending', samples: [{ x: event.clientX, time: event.timeStamp }] }
  }
  function sample(event: PointerEvent, current: Gesture) {
    current.samples.push({ x: event.clientX, time: event.timeStamp })
    while (current.samples.length > 2 && current.samples[1]!.time < event.timeStamp - 100) current.samples.shift()
  }
  function move(event: PointerEvent) {
    const current = gesture
    if (!current || current.id !== event.pointerId || !panel.value) return
    const dx = event.clientX - current.startX
    const dy = event.clientY - current.startY
    if (current.axis === 'pending') {
      if (Math.max(Math.abs(dx), Math.abs(dy)) < 8) return
      current.axis = dx < 0 && Math.abs(dx) > Math.abs(dy) ? 'horizontal' : 'ignored'
      if (current.axis === 'horizontal') {
        panel.value.setPointerCapture(event.pointerId)
        panel.value.classList.add('mobile-dragging')
        backdrop.value?.classList.add('mobile-dragging')
      }
    }
    if (current.axis !== 'horizontal') return
    if (event.cancelable) event.preventDefault()
    current.offset = Math.max(-current.width, Math.min(0, current.initialOffset + dx))
    sample(event, current)
    // Only compositor properties change while dragging; the session list need not rerender.
    panel.value.style.setProperty('--drawer-offset', `${current.offset}px`)
    backdrop.value?.style.setProperty('--drawer-scrim-opacity', String(1 + current.offset / current.width))
  }
  function end(event: PointerEvent) {
    const current = gesture
    if (!current || current.id !== event.pointerId) return
    if (current.axis === 'horizontal') {
      sample(event, current)
      const first = current.samples[0]!
      const velocity = (event.clientX - first.x) / Math.max(1, event.timeStamp - first.time)
      const distance = -current.offset
      const close = velocity < -0.5 ? distance >= 24 : velocity > 0.5 ? false : distance >= current.width * 0.3
      suppressClickUntil = performance.now() + 500
      if (close) open.value = false
    }
    reset()
  }
  function cancel(event: PointerEvent) {
    if (gesture?.id === event.pointerId) reset()
  }
  function lostCapture(event: PointerEvent) {
    if (event.target === panel.value) cancel(event)
  }
  function click(event: MouseEvent) {
    if (event.detail && performance.now() < suppressClickUntil) {
      event.preventDefault()
      event.stopImmediatePropagation()
    }
  }
  watch([open, desktop], reset, { flush: 'post' })
  onMounted(() => window.addEventListener('resize', reset))
  onUnmounted(() => { window.removeEventListener('resize', reset); reset() })
  return { start, move, end, cancel, lostCapture, click }
}
