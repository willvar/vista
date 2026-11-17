<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import TimelineItem from '../../src/TimelineItem.vue'
import { useConversationScroll } from '../../src/composables/useConversationScroll'

const element = ref<HTMLElement | null>(null)
const paused = ref(false)
const missing = ref(new Set<string>())
const parameters = new URLSearchParams(location.search)
const pendingTarget = parameters.has('pending-target')
const count = parameters.has('delayed-arrival') || pendingTarget ? 4 : 40
const rows = computed(() => Array.from({ length: count }, (_, index) => ({ key: `row-${index}`, sequence: index + 1, estimate: 80 })))
const scrolling = useConversationScroll({ element, rows, session: () => 'scroll-contract', prepared: ref(true), missing, paused, earlier: async () => {} })
const growth = ref(0)
const writeKind = ref('')
const arrivalDistance = ref<number>()
const arrivalJumpVisible = ref<boolean>()
let delayedArrival = false
let armed = false
const lastKey = rows.value.at(-1)!.key

function readEarlier() {
  scrolling.inspect()
  scrolling.virtualizer.value.scrollToOffset(0)
}
function jump(delayed = false) {
  delayedArrival = delayed
  arrivalDistance.value = undefined
  arrivalJumpVisible.value = undefined
  growth.value = 0
  writeKind.value = ''
  armed = !pendingTarget
  missing.value = new Set([lastKey])
  scrolling.beginJump()
  scrolling.jump()
  if (pendingTarget) element.value!.dispatchEvent(new Event('scroll'))
}
watch([scrolling.loading, () => scrolling.visible.value.some(item => item.key === lastKey)], ([loading, visible]) => {
  if (armed && loading && visible && missing.value.has(lastKey)) missing.value = new Set()
}, { flush: 'post' })
watch(scrolling.loading, loading => {
  if (loading || !delayedArrival || !writeKind.value || arrivalDistance.value !== undefined || !element.value) return
  arrivalDistance.value = element.value.scrollHeight - element.value.clientHeight - element.value.scrollTop
  arrivalJumpVisible.value = scrolling.showJumpToLatest.value
}, { flush: 'post' })

// The component owns the same public measurement callback as ConversationPage.
// Deliver a late row measurement after the prepared scroll write, before the
// browser reports its position. Do not add a product debug API or mock TanStack.
watch(element, (viewport, _, onCleanup) => {
  if (!viewport) return
  const virtualizer = scrolling.virtualizer.value
  const offset = virtualizer.scrollToOffset
  const end = virtualizer.scrollToEnd
  function measureAfterWrite(kind: string) {
    if (!armed) return
    armed = false
    if (!delayedArrival) {
      const row = viewport!.querySelector<HTMLElement>(`[data-index="${rows.value.length - 1}"]`)!
      const before = row.getBoundingClientRect().height
      row.style.paddingBottom = `${(parseFloat(getComputedStyle(row).paddingBottom) || 0) + 199}px`
      scrolling.measure(row)
      growth.value = Math.round(row.getBoundingClientRect().height - before)
    }
    writeKind.value = kind
  }
  virtualizer.scrollToOffset = (target, options) => {
    const inject = armed && scrolling.loading.value && scrolling.following.value
      && target >= virtualizer.getTotalSize() - viewport.clientHeight - 3
    offset(target, inject && delayedArrival ? { ...options, behavior: 'smooth' } : options)
    if (inject) measureAfterWrite('offset')
  }
  virtualizer.scrollToEnd = options => {
    const inject = armed && scrolling.loading.value && scrolling.following.value
    // A native smooth landing separates issuing a jump from reaching it.
    end(inject && delayedArrival ? { ...options, behavior: 'smooth' } : options)
    if (inject) measureAfterWrite('end')
  }
  onCleanup(() => { virtualizer.scrollToOffset = offset; virtualizer.scrollToEnd = end })
}, { flush: 'post' })
</script>

<template>
  <main class="scroll-contract">
    <h1>Conversation scrolling</h1>
    <button @click="readEarlier">Read earlier</button>
    <button @click="jump()">{{ pendingTarget ? 'Jump with pending target' : 'Jump with late measurement' }}</button>
    <button v-if="pendingTarget" @click="missing = new Set()">Finish target preparation</button>
    <button @click="jump(true)">Jump with delayed arrival</button>
    <output data-testid="late-growth">{{ growth }}</output>
    <output data-testid="scroll-write">{{ writeKind }}</output>
    <output data-testid="arrival-distance">{{ arrivalDistance }}</output>
    <output data-testid="arrival-jump-visible">{{ arrivalJumpVisible }}</output>
    <output data-testid="following">{{ scrolling.following.value }}</output>
    <output data-testid="jump-visible">{{ scrolling.showJumpToLatest.value }}</output>
    <div ref="element" class="scroll-contract-viewport" :aria-busy="scrolling.loading.value" :inert="!scrolling.presented.value"
      @scroll="scrolling.onScroll" @wheel="scrolling.onWheel" @keydown="scrolling.onKeyDown">
      <div class="message-space" :style="{ height: `${scrolling.totalSize.value}px` }">
        <TimelineItem v-for="layout in scrolling.visible.value" :key="String(layout.key)" :layout="layout" :paused="paused || missing.has(String(layout.key))"
          :placeholder="scrolling.placeholders.value.has(String(layout.key)) || missing.has(String(layout.key))" @measure="scrolling.measure">
          <div :style="{ height: `${layout.index === rows.length - 1 ? 800 : 80 + layout.index % 4 * 20}px` }">Row {{ layout.index + 1 }}</div>
        </TimelineItem>
      </div>
    </div>
  </main>
</template>

<style scoped>
.scroll-contract { width: min(640px, 100%); margin: auto; padding: 8px; }
.scroll-contract h1 { font-size: 18px; margin: 0 0 8px; }
.scroll-contract button { margin: 4px; }
.scroll-contract output { display: inline-block; margin: 4px; }
.scroll-contract-viewport { height: 120px; overflow: auto; overflow-anchor: none; }
</style>
