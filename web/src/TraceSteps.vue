<script setup lang="ts">
import { computed, onScopeDispose, ref, watch, type ComponentPublicInstance } from 'vue'
import { elementScroll, useVirtualizer } from '@tanstack/vue-virtual'
import { useDragStableMeasurement, useScrollbarDrag } from './composables/useScrollbarDrag'
import type { ThoughtChainItemType } from '@antdv-next/x/dist/thought-chain/interface'
import type { RunTraceNode, RunTraceTask } from './state/runTrace'
import TraceReport from './TraceReport.vue'
import TraceStep from './TraceStep.vue'

type Node = RunTraceNode & ThoughtChainItemType
const props = defineProps<{
  sessionId: string; task: RunTraceTask; showReport: boolean; items: Node[]; expandedKeys: string[]
  nodeLoading: Record<number, boolean>; nodeFailure: Record<number, string>; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; retry: [node: RunTraceNode] }>()
const root = ref<HTMLElement | null>(null)
const scrollElement = ref<HTMLElement | null>(null)
const scrollMargin = ref(0)
const listPadding = ref(0)
const virtualized = computed(() => props.items.length > 100)
const scrollbarDragging = useScrollbarDrag()
const measureSize = useDragStableMeasurement(scrollbarDragging, () => virtualizer.value)
let observer: ResizeObserver | undefined
let offsetFrame = 0
function updateOffset() {
  const element = root.value
  const scroller = scrollElement.value
  if (element && scroller) {
    scrollMargin.value = element.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop
    listPadding.value = parseFloat(getComputedStyle(element).paddingTop) || 0
  }
}
watch(root, element => {
  observer?.disconnect()
  scrollElement.value = element?.closest<HTMLElement>('.messages') || null
  if (!element || !scrollElement.value) return
  updateOffset()
  observer = new ResizeObserver(() => {
    if (!offsetFrame) offsetFrame = requestAnimationFrame(() => { offsetFrame = 0; updateOffset() })
  })
  observer.observe(element)
  observer.observe(scrollElement.value)
  // A session above this list can expand or receive new steps. Its height
  // changes the list's offset inside the shared conversation scroll area.
  const content = scrollElement.value.querySelector('.message-space')
  if (content) observer.observe(content)
}, { flush: 'post' })
onScopeDispose(() => { observer?.disconnect(); cancelAnimationFrame(offsetFrame) })
const virtualizer = useVirtualizer(computed(() => ({
  count: props.items.length,
  // This list joins an already-scrolled conversation. Wait for its scroller
  // before initializing, including when long lists render before mounting.
  enabled: !!scrollElement.value,
  initialOffset: () => scrollElement.value?.scrollTop ?? 0,
  // Measure short lists too, so adding the 101st step reuses their real heights
  // instead of replacing open output with fresh collapsed-height estimates.
  getScrollElement: () => scrollElement.value,
  getItemKey: (index: number) => props.items[index]!.key,
  // Rounding each row's height accumulates a visible jump when 100 normal
  // rows switch to absolute virtual positions. Keep CSS subpixel precision.
  measureElement: measureSize,
  scrollToFn: (...args: Parameters<typeof elementScroll<HTMLElement>>) => { if (!scrollbarDragging.value) elementScroll(...args) },
  estimateSize: () => 72,
  overscan: 5,
  scrollMargin: scrollMargin.value,
  paddingStart: listPadding.value,
  paddingEnd: listPadding.value,
  useAnimationFrameWithResizeObserver: true,
})))
virtualizer.value.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) => {
  if (!virtualized.value || scrollbarDragging.value) return false
  const scroller = scrollElement.value
  const message = root.value?.closest<HTMLElement>('.message-row')
  if (scroller && message?.parentElement?.classList.contains('virtualized') && message.getBoundingClientRect().bottom <= scroller.getBoundingClientRect().top) {
    // The outer message virtualizer owns height changes above the viewport.
    return false
  }
  const offset = (instance.scrollOffset || 0) + instance.scrollAdjustments
  return !instance.itemSizeCache.has(item.key) ? item.start < offset
    : item.end <= offset && instance.scrollDirection !== 'backward'
}
const rows = computed(() => virtualized.value ? virtualizer.value.getVirtualItems()
  : props.items.map((item, index) => ({ key: item.key, index, start: 0 })))
function measureRow(element: Element | ComponentPublicInstance | null) {
  if (element instanceof Element) virtualizer.value.measureElement(element)
}
function expandRow(item: Node, keys: string[]) {
  const remaining = props.expandedKeys.filter(key => key !== item.key)
  emit('expand', keys.includes(item.key) ? [...remaining, item.key] : remaining)
}
</script>

<template>
  <div ref="root" class="trace-steps" :data-step-count="items.length" :data-virtualized="virtualized" :style="virtualized ? { height: `${virtualizer.getTotalSize()}px` } : undefined">
  <div v-for="row in rows" :key="String(row.key)" :ref="measureRow" class="trace-step-row" :class="{ 'trace-step-last': row.index === items.length - 1 }" :data-index="row.index" :data-step-key="items[row.index].key" :style="virtualized ? { transform: `translateY(${row.start - scrollMargin}px)` } : undefined">
  <TraceStep :item="items[row.index]" :expanded="expandedKeys.includes(items[row.index].key)" :loading="!!nodeLoading[items[row.index].lastSequence]" :failure="nodeFailure[items[row.index].lastSequence]" :node-id="nodeId" @expand="keys => expandRow(items[row.index], keys)" @retry="node => emit('retry', node)">
    <template #tool-output="{ node, expanded }"><slot name="tool-output" :node="node" :expanded="expanded" /></template>
  </TraceStep>
  </div>
  </div>
  <TraceReport v-if="task.id !== 'main' && showReport && task.report" :session-id="sessionId" :report="task.report" />
</template>

<style scoped>
.trace-steps{position:relative;min-width:0;padding:.5rem 0;overflow-anchor:none}
.trace-steps[data-virtualized="true"]{overflow:clip}
.trace-steps[data-virtualized="true"]>.trace-step-row{position:absolute;top:0;left:0;width:100%}
.trace-step-row{min-width:0}.trace-step-row :deep(.antd-thought-chain){padding:0}
.trace-step-row:not(.trace-step-last) :deep(.antd-thought-chain-node-icon::after){display:block}
</style>
