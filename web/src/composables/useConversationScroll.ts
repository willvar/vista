import { computed, nextTick, onScopeDispose, onUpdated, provide, ref, shallowReactive, shallowRef, watch, type InjectionKey, type Ref, type WatchSource } from 'vue'
import { defaultRangeExtractor, useVirtualizer } from '@tanstack/vue-virtual'
import { useScrollbarDrag } from './useScrollbarDrag'
import type { TimelineEntry } from '../state/conversationTimeline.ts'

export const timelineReadKey: InjectionKey<(key: string, pending: boolean) => void> = Symbol('timelineRead')

// TanStack owns all scroll writes, measurements and prepend compensation.
// Input ownership only stages presentation and cancels an old scroll command.
export function useConversationScroll(options: {
  element: Ref<HTMLElement | null>
  rows: Ref<TimelineEntry[]>
  session: WatchSource<string | undefined>
  prepared: Ref<boolean>
  missing: Ref<Set<string>>
  paused: Ref<boolean>
  earlier: () => Promise<unknown>
}) {
  const { element, rows, paused } = options
  const following = ref(true)
  const atEnd = ref(true)
  const presented = ref(false)
  const jumpPhase = ref<'preparing' | 'scrolling' | null>(null)
  const location = shallowRef<{ controller: AbortController; followedEnd: boolean; keepEndWhileReading: boolean; key?: string; lost?: () => void }>()
  const readingFromEnd = computed(() => !!location.value?.keepEndWhileReading && !location.value.key)
  type Anchor = { key: string; within: number }
  const preparation = shallowRef<{ id: number; from: Anchor; target: Anchor; start: number; end: number; follow: boolean; locate?: boolean; corrections?: number } | null>(null)
  const loading = computed(() => !presented.value || !!location.value || !!preparation.value)
  const locked = computed(() => !!preparation.value && !preparation.value.locate)
  const overscan = 6
  let lastReady: Anchor | undefined
  let preparationId = 0
  let applying = false
  const touching = ref(false)
  const motion = ref(false)
  const padding = ref({ start: 0, end: 0 })
  const measurements = shallowReactive(new Map<string, number>())
  const reads = shallowReactive(new Set<string>())
  const measurementRevision = ref(0), readRevision = ref(0)
  const indices = computed(() => new Map(rows.value.map((row, index) => [row.key, index])))
  let revealFrame = 0
  provide(timelineReadKey, (key, pending) => {
    if (reads.has(key) === pending) return
    if (pending) reads.add(key)
    else reads.delete(key)
    readRevision.value++
  })
  function clearMeasurements() { if (measurements.size) { measurements.clear(); measurementRevision.value++ } }
  let generation = 0
  let input = false
  let inputAtEnd = false
  let inputOffset = 0
  let inputEnd = 0
  let smoothCommand = false
  let idleTimer: ReturnType<typeof setTimeout> | undefined
  let width = 0
  let viewportObserver: ResizeObserver | undefined
  let touchTarget: EventTarget | null = null

  function cancelLocation() {
    location.value?.controller.abort()
    location.value = undefined
    if (!preparation.value?.locate) return
    const from = preparation.value.from
    preparation.value = null
    applying = false
    cancelAnimationFrame(revealFrame)
    virtualizer.value.cancelScroll()
    if (indices.value.has(from.key)) virtualizer.value.scrollToOffset(offsetFor(from))
  }
  function loseLocation() {
    const active = location.value
    if (!active) return
    restoreLocation(active.controller.signal)
    active.lost?.()
  }
  function takeControl() {
    cancelLocation()
    const offset = element.value?.scrollTop || 0, range = rangeAt(offset)
    if (!input) {
      updatePosition()
      inputAtEnd = atEnd.value
      inputOffset = offset
      inputEnd = Math.max(0, (element.value?.scrollHeight || 0) - (element.value?.clientHeight || 0))
    }
    if (presented.value && ready(range.start, range.end)) lastReady = anchorAt(offset)
    jumpPhase.value = null
    input = true
    motion.value = true
    following.value = false
    // cancelScroll drops TanStack's target. Chromium can still finish the
    // browser's native smooth animation, so stop that motion at its current
    // offset once, before handing subsequent movement to the user.
    if (smoothCommand && element.value) virtualizer.value.scrollToOffset(element.value.scrollTop)
    smoothCommand = false
    virtualizer.value.cancelScroll()
    scheduleIdle()
  }
  function scheduleIdle() {
    clearTimeout(idleTimer)
    // Input can precede the browser's scroll event. Only actual input/scroll
    // events renew ownership; virtualizer notifications also describe layout
    // changes and must never end a gesture before that movement arrives.
    // This also releases wheel/keyboard input at an edge, which need not move.
    idleTimer = setTimeout(() => { if (!touching.value && !dragging.value) settle() }, 180)
  }
  function settle() {
    clearTimeout(idleTimer)
    const reachedEnd = input && inputAtEnd
    input = false
    motion.value = false
    if (locked.value || location.value) return
    updatePosition()
    following.value = reachedEnd || atEnd.value
    void nextTick(() => { measureVisible(); syncEnd(); maybeEarlier() })
  }
  const scrollbar = useScrollbarDrag(element, takeControl, () => { queueMicrotask(settle) })
  const { dragging } = scrollbar
  watch([dragging, touching, motion], () => { paused.value = dragging.value || touching.value || motion.value }, { flush: 'sync' })

  const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(computed(() => {
    const entries = rows.value
    const extra = preparation.value
    return {
    count: entries.length,
    getScrollElement: () => element.value,
    getItemKey: (index: number) => entries[index]!.key,
    estimateSize: (index: number) => measurements.get(entries[index]!.key) ?? entries[index]!.estimate,
    anchorTo: 'end' as const,
    followOnAppend: !paused.value && !extra && following.value,
    scrollEndThreshold: 3,
    overscan,
    // Prepare the requested destination off screen while the current readable
    // viewport stays mounted. Both windows share this virtualizer's geometry.
    rangeExtractor: (range: Parameters<typeof defaultRangeExtractor>[0]) => extra
      ? [...new Set([...defaultRangeExtractor(range), ...defaultRangeExtractor({ ...range, startIndex: extra.start, endIndex: extra.end })])].sort((a, b) => a - b)
      : defaultRangeExtractor(range),
    paddingStart: padding.value.start,
    paddingEnd: padding.value.end,
    useAnimationFrameWithResizeObserver: true,
    measureElement: (node: HTMLElement, entry: ResizeObserverEntry | undefined) => {
      const key = node.dataset.timelineKey!
      if (paused.value) return measurements.get(key) ?? rows.value[Number(node.dataset.index)]?.estimate ?? 80
      const size = entry?.borderBoxSize?.[0]?.blockSize ?? node.getBoundingClientRect().height
      if (size > 0 && measurements.get(key) !== size) { measurements.set(key, size); measurementRevision.value++ }
      return size
    },
  }}))
  const visible = computed(() => virtualizer.value.getVirtualItems())
  const placeholders = computed(() => new Set(paused.value ? visible.value.filter(item => !measurements.has(String(item.key))).map(item => String(item.key)) : []))
  // Only the first entry hides the message area. Later preparation freezes the
  // last readable viewport and measures a bounded destination window off screen.
  const viewport = computed(() => {
    const range = virtualizer.value.calculateRange()
    return range ? visible.value.filter(item => item.index >= range.startIndex && item.index <= range.endIndex) : []
  })
  const demand = computed(() => {
    if (!presented.value) return []
    const pending = preparation.value
    if (!pending) return viewport.value
    const padding = pending.locate ? overscan : 0
    return visible.value.filter(item => item.index >= pending.start - padding && item.index <= pending.end + padding)
  })
  function anchorAt(offset: number): Anchor | undefined {
    const item = virtualizer.value.getVirtualItemForOffset(offset)
    return item ? { key: String(item.key), within: Math.max(0, offset - item.start) } : undefined
  }
  function offsetFor(anchor: Anchor) {
    const index = indices.value.get(anchor.key) ?? -1
    const item = rows.value[index]
    const start = virtualizer.value.getOffsetForIndex(index, 'start')?.[0] ?? 0
    return item ? Math.max(0, Math.min(start + Math.min(anchor.within, Math.max(0, (measurements.get(item.key) ?? item.estimate) - 1)),
      virtualizer.value.getTotalSize() - (element.value?.clientHeight || 0))) : 0
  }
  function rangeAt(offset: number) {
    const first = virtualizer.value.getVirtualItemForOffset(offset)
    const last = virtualizer.value.getVirtualItemForOffset(offset + (element.value?.clientHeight || 0) - 1)
    return { start: first?.index ?? 0, end: last?.index ?? first?.index ?? 0 }
  }
  function ready(start: number, end: number) {
    return rows.value.slice(start, end + 1).every(item =>
      measurements.has(item.key) && !options.missing.value.has(item.key) && !reads.has(item.key))
  }
  function prepareDestination(offset: number, key?: string) {
    if (!lastReady || preparation.value || !presented.value) return
    // Keep an explicit source identity. A rounded pixel offset at a row's
    // boundary can resolve to the preceding, still unmeasured row.
    const target = key ? { key, within: 0 } : anchorAt(offset)
    if (!target) return
    preparation.value = { id: ++preparationId, from: lastReady, target, ...rangeAt(offset), follow: following.value, locate: !!key }
    clearTimeout(idleTimer)
    input = false; smoothCommand = false; motion.value = false; touching.value = false
    releaseTouchTarget()
    scrollbar.finish()
    virtualizer.value.cancelScroll()
    virtualizer.value.scrollToOffset(offsetFor(lastReady))
  }
  function destinationReady(pending: NonNullable<typeof preparation.value>) {
    return ready(Math.max(0, pending.start - (pending.locate ? overscan : 0)),
      Math.min(rows.value.length - 1, pending.end + (pending.locate ? overscan : 0)))
  }
  function finishPreparation(pending: NonNullable<typeof preparation.value>) {
    if (pending.locate) {
      const viewport = element.value
      const node = [...(viewport?.querySelectorAll<HTMLElement>('[data-timeline-key]') || [])].find(row => row.dataset.timelineKey === pending.target.key)
      if (!viewport || !node || !destinationReady(pending)) return false
      const box = node.getBoundingClientRect(), bounds = viewport.getBoundingClientRect()
      if (box.bottom <= bounds.top || box.top >= bounds.bottom) return false
      location.value = undefined
    }
    preparation.value = null
    applying = false
    // The DOM scroll event can reach us before TanStack updates its cached
    // offset. Use the same actual position as the jump-to-latest control.
    updatePosition()
    following.value = pending.follow || atEnd.value
    lastReady = anchorAt(element.value?.scrollTop || 0)
    if (!pending.locate) maybeEarlier()
    return true
  }
  function updatePreparation() {
    if (!presented.value) return
    const target = location.value?.key
    // A rollback can remove a resolved target. Never scroll a stale index or
    // wait indefinitely for a row that cannot be mounted again.
    if (target && !indices.value.has(target)) { loseLocation(); return }
    if (applying) return
    cancelAnimationFrame(revealFrame)
    const pending = preparation.value
    if (!pending && location.value) {
      if (target) prepareDestination(offsetFor({ key: target, within: 0 }), target)
      else if (readingFromEnd.value && atEnd.value) {
        const offset = element.value?.scrollTop || 0, range = rangeAt(offset)
        if (ready(range.start, range.end)) lastReady = anchorAt(offset)
      }
      return
    }
    if (pending) {
      const offset = pending.follow ? Math.max(0, virtualizer.value.getTotalSize() - (element.value?.clientHeight || 0)) : offsetFor(pending.target)
      const range = rangeAt(offset)
      if (range.start !== pending.start || range.end !== pending.end) {
        preparation.value = { ...pending, ...range }
        return
      }
      if (!destinationReady(pending)) return
      // Wait for measurements and TanStack's after-commit anchor correction.
      revealFrame = requestAnimationFrame(() => { revealFrame = requestAnimationFrame(async () => {
        if (preparation.value !== pending) return
        const targetIndex = indices.value.get(pending.target.key)
        if (pending.locate && targetIndex === undefined) { loseLocation(); return }
        applying = true
        if (pending.locate) virtualizer.value.scrollToOffset(offsetFor(pending.target))
        else if (pending.follow) virtualizer.value.scrollToEnd()
        else virtualizer.value.scrollToOffset(offsetFor(pending.target))
        await nextTick(); await frame(); await frame()
        if (preparation.value !== pending) return
        const rangeNow = pending.locate ? rangeAt(offsetFor(pending.target)) : pending
        const stable = rangeNow.start === pending.start && rangeNow.end === pending.end
        if (stable && finishPreparation(pending)) return
        // Rebuild the destination after a layout change. Only count corrections
        // where the measured range stayed stable but we could not reveal it.
        applying = false
        const corrections = stable && destinationReady(pending) ? (pending.corrections || 0) + 1 : 0
        if (corrections >= 3) loseLocation()
        else preparation.value = { ...pending, corrections }
      }) })
      return
    }
    const range = rangeAt(element.value?.scrollTop || 0)
    if (!ready(range.start, range.end)) {
      if (paused.value || !following.value || jumpPhase.value) prepareDestination(element.value?.scrollTop || 0)
      return
    }
    lastReady = anchorAt(element.value?.scrollTop || 0)
    updatePosition()
  }
  watch([presented, paused, following, jumpPhase, preparation, location, indices, options.missing,
    readRevision, measurementRevision,
    () => viewport.value.map(item => `${item.key}/${item.start}/${item.size}`).join('|')], updatePreparation, { flush: 'post' })
  // Focus navigation and text selection can scroll without taking input
  // ownership. Button visibility follows the DOM position, not follow intent.
  function updatePosition() {
    const viewport = element.value
    atEnd.value = !viewport || viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop <= 3
    // Arrival is a DOM fact, not follow intent or an earlier cached offset.
    if (viewport && !loading.value && jumpPhase.value === 'scrolling' && atEnd.value) {
      const range = rangeAt(viewport.scrollTop)
      if (ready(range.start, range.end)) jumpPhase.value = null
    }
  }
  function syncEnd() {
    if (!paused.value && jumpPhase.value !== 'preparing' && (following.value && !loading.value || presented.value && readingFromEnd.value)) {
      virtualizer.value.scrollToEnd({ behavior: smoothCommand ? 'smooth' : 'auto' })
    }
  }
  const showJumpToLatest = computed(() => presented.value && !jumpPhase.value && !atEnd.value)
  const totalSize = computed(() => virtualizer.value.getTotalSize())
  watch(totalSize, (_, previous) => {
    const viewport = element.value
    // A cold body can grow in place, without an append. Only preserve an end
    // the reader actually occupied before its measured size changed.
    if (viewport && !jumpPhase.value && (readingFromEnd.value || previous - viewport.clientHeight - viewport.scrollTop <= 3)) syncEnd()
  }, { flush: 'post' })
  // The Vue adapter updates options before the DOM commits. Finish TanStack's
  // layout lifecycle afterwards as well, so a bottom correction clamped by
  // the previous spacer height is retried against the new scroll range.
  onUpdated(() => { virtualizer.value._willUpdate(); updatePosition() })

  function measure(node: Element | null) { virtualizer.value.measureElement(node as HTMLElement | null) }
  function measureVisible() {
    if (paused.value) return
    element.value?.querySelectorAll<HTMLElement>('[data-timeline-key]').forEach(measure)
  }
  function maybeEarlier() {
    const viewport = element.value
    if (!presented.value || locked.value || location.value || following.value || !viewport) return
    if (viewport.scrollTop < Math.max(240, viewport.clientHeight * .75)) void options.earlier()
  }
  function onScroll() {
    updatePosition()
    const offset = element.value?.scrollTop || 0
    if (input && offset !== inputOffset) {
      // Keep the gesture's original end across layout changes; only actual
      // movement away from it can cancel the reached-end intent.
      inputAtEnd = atEnd.value || offset > inputOffset && offset >= inputEnd - 3
      inputOffset = offset
    }
    if (preparation.value) {
      if (!applying) virtualizer.value.scrollToOffset(offsetFor(preparation.value.from))
      return
    }
    const range = rangeAt(offset)
    if (presented.value && (paused.value || jumpPhase.value) && !ready(range.start, range.end)) prepareDestination(offset)
    if (input) scheduleIdle()
    maybeEarlier()
  }
  function navigate(nextDirection: number) {
    if (!nextDirection || element.value?.classList.contains('usage-scroll-locked')) return
    takeControl()
  }
  function onWheel(event: WheelEvent) {
    if (locked.value) { event.preventDefault(); return }
    navigate(Math.sign(event.deltaY))
  }
  function releaseTouchTarget() {
    touchTarget?.removeEventListener('touchend', onTouchEnd as EventListener)
    touchTarget?.removeEventListener('touchcancel', onTouchEnd as EventListener)
    touchTarget = null
  }
  function onTouchStart(event: TouchEvent) {
    if (locked.value) { event.preventDefault(); return }
    if (!touching.value) {
      // Native touchend keeps the original target even when virtualization
      // removes it. Its event then cannot bubble to the stable scroller.
      touchTarget = event.target
      touchTarget?.addEventListener('touchend', onTouchEnd as EventListener, { passive: true })
      touchTarget?.addEventListener('touchcancel', onTouchEnd as EventListener, { passive: true })
    }
    touching.value = true
    takeControl()
  }
  function onTouchEnd(event?: TouchEvent) {
    if (event?.touches.length) return
    releaseTouchTarget()
    touching.value = false
    scheduleIdle()
  }
  function onTouchMove(event: TouchEvent) { if (locked.value) event.preventDefault() }
  function onBlur() { if (touching.value) onTouchEnd() }
  window.addEventListener('blur', onBlur)
  function onKeyDown(event: KeyboardEvent) {
    const target = event.target instanceof Element ? event.target : null
    if (!['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(event.key)
      || target?.closest('input, textarea, select, [contenteditable="true"], [role="menu"], [role="combobox"]')
      || event.key === ' ' && target?.closest('button, a[href], summary, [role="button"]')) return
    if (locked.value) { event.preventDefault(); return }
    navigate(['ArrowUp', 'PageUp', 'Home'].includes(event.key) || event.key === ' ' && event.shiftKey ? -1 : 1)
  }
  function inspect() {
    cancelLocation()
    jumpPhase.value = null
    following.value = false
    if (smoothCommand && element.value) virtualizer.value.scrollToOffset(element.value.scrollTop)
    smoothCommand = false
    virtualizer.value.cancelScroll()
  }
  function beginLocation(opening: boolean) {
    const followedEnd = location.value?.followedEnd ?? following.value
    updatePosition()
    // Keep the opening/latest reply readable while the source index is held.
    // Presentation may precede the opening layout's actual arrival at the end.
    // Stop keeping the end as soon as there is a real destination to prepare.
    const keepEndWhileReading = readingFromEnd.value || followedEnd && (opening || !presented.value || atEnd.value)
    inspect()
    preparation.value = null
    applying = false
    cancelAnimationFrame(revealFrame)
    clearTimeout(idleTimer)
    input = false; motion.value = false
    const controller = new AbortController()
    location.value = { controller, followedEnd, keepEndWhileReading }
    if (keepEndWhileReading) syncEnd()
    return controller.signal
  }
  function restoreLocation(signal: AbortSignal) {
    const active = location.value
    if (!active || active.controller.signal !== signal) return
    cancelLocation()
    // The attempt must not turn a reader who was following the latest reply
    // into an older-history reader when late trace measurements arrive.
    if (active.followedEnd) follow()
  }
  function revealLocation(signal: AbortSignal, key: string, lost: () => void) {
    const pending = location.value
    if (!pending || pending.controller.signal !== signal || !indices.value.has(key)) return false
    location.value = { ...pending, key, lost }
    return true
  }
  function onPointerDown(event: PointerEvent) {
    if (locked.value) { event.preventDefault(); return }
    scrollbar.pointerDown(event)
    if ((event.target as Element | null)?.closest('.run-trace')) inspect()
  }
  function follow(behavior: ScrollBehavior | null = 'auto', preserveLocation = false) {
    if (!preserveLocation) cancelLocation()
    preparation.value = null
    applying = false
    cancelAnimationFrame(revealFrame)
    jumpPhase.value = null
    clearTimeout(idleTimer)
    input = false; motion.value = false
    following.value = !location.value
    smoothCommand = behavior === 'smooth'
    if (behavior) virtualizer.value.scrollToEnd({ behavior })
    else virtualizer.value.cancelScroll()
  }
  function jump() {
    follow(window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth')
    jumpPhase.value = 'scrolling'
  }
  // Prepare the destination before moving. An early automatic scroll would
  // bypass the later smooth journey and make user interruption ineffective.
  function beginJump() { follow(null); jumpPhase.value = 'preparing' }
  const frame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  async function present() {
    if (!options.prepared.value || presented.value) return
    const current = generation
    await nextTick()
    if (current !== generation) return
    // A source can be clicked before the initial history has been presented.
    follow('auto', true)
    await frame()
    if (current !== generation) return
    measureVisible()
    await nextTick()
    await frame()
    if (current !== generation) return
    follow('auto', true)
    presented.value = true
    lastReady = anchorAt(element.value?.scrollTop || 0)
  }
  watch(options.prepared, () => { void present() }, { immediate: true, flush: 'post' })
  watch(options.session, () => {
    generation++
    cancelLocation()
    presented.value = false
    preparation.value = null
    applying = false
    lastReady = undefined
    jumpPhase.value = null
    cancelAnimationFrame(revealFrame)
    clearMeasurements()
    reads.clear()
    following.value = true
    atEnd.value = true
    clearTimeout(idleTimer)
    releaseTouchTarget()
    input = false; touching.value = false; motion.value = false
    smoothCommand = false
    scrollbar.finish()
    virtualizer.value.cancelScroll()
    virtualizer.value.measure()
    void nextTick(present)
  }, { flush: 'sync' })
  watch(paused, value => { if (!value) void nextTick(measureVisible) }, { flush: 'post' })
  watch(element, viewport => {
    viewportObserver?.disconnect()
    width = viewport?.clientWidth ?? 0
    if (!viewport) return
    viewportObserver = new ResizeObserver(() => {
      const style = getComputedStyle(viewport)
      const start = parseFloat(style.getPropertyValue('--history-padding-start')) || 0
      const end = parseFloat(style.getPropertyValue('--history-padding-end')) || 0
      if (padding.value.start !== start || padding.value.end !== end) padding.value = { start, end }
      const widthChanged = width !== viewport.clientWidth
      if (widthChanged) {
        width = viewport.clientWidth
        clearMeasurements()
        virtualizer.value.measure()
      }
      void nextTick(() => {
        if (widthChanged) measureVisible()
        // Composer and execution controls also change the viewport's height.
        // Keep the user's bottom-follow intent through those layout changes.
        syncEnd()
        updatePosition()
      })
    })
    viewportObserver.observe(viewport)
  }, { flush: 'post' })
  onScopeDispose(() => { generation++; location.value?.controller.abort(); location.value = undefined; preparation.value = null; applying = false; clearTimeout(idleTimer); cancelAnimationFrame(revealFrame); releaseTouchTarget(); window.removeEventListener('blur', onBlur); viewportObserver?.disconnect() })
  return { virtualizer, visible, viewport, demand, totalSize, placeholders, presented, loading, locked, following, showJumpToLatest, dragging,
    measure, follow, beginJump, jump, inspect, beginLocation, revealLocation, restoreLocation, onScroll, onWheel, onTouchStart, onTouchMove, onTouchEnd, onPointerDown, onKeyDown }
}
