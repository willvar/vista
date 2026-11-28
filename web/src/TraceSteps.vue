<script setup lang="ts">
import { computed } from 'vue'
import type { TraceNodeView as Node } from './ui/traceTypes'
import type { RunTraceNode } from './state/runTrace'
import type { TimelineLayout } from './state/conversationTimeline'
import TraceStep from './TraceStep.vue'
import TimelineItem from './TimelineItem.vue'

const props = defineProps<{
  items: Node[]; expandedKeys: string[]; layout: Record<string, TimelineLayout>; paused: boolean; placeholders: Set<string>
  nodeLoading: Record<number, boolean>; nodeFailure: Record<number, string>; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; retry: [node: RunTraceNode]; measure: [element: Element | null] }>()
const rows = computed(() => props.items.flatMap((item, index) => props.layout[item.key] ? [{ item, index, layout: props.layout[item.key]! }] : []))
function expandRow(item: Node, keys: string[]) {
  const remaining = props.expandedKeys.filter(key => key !== item.key)
  emit('expand', keys.includes(item.key) ? [...remaining, item.key] : remaining)
}
</script>

<template>
  <div class="trace-steps" :data-step-count="items.length">
    <TimelineItem v-for="row in rows" :key="row.item.key" :layout="row.layout" :paused="paused" :placeholder="placeholders.has(String(row.layout.key))" @measure="element => emit('measure', element)"
      class="timeline-row trace-step-row trace-task trace-task-group" :class="{ 'trace-step-last': row.index === items.length - 1 }"
      :data-step-key="row.item.key" :data-timeline-sequence="row.item.sequence"
      :data-task-id="row.item.taskId" :data-agent-role="row.item.taskId === 'main' ? 'main' : 'subagent'">
        <slot name="before-step" :node="row.item" :index="row.index" />
        <TraceStep :item="row.item" :expanded="expandedKeys.includes(row.item.key)" :loading="!!nodeLoading[row.item.lastSequence]" :failure="nodeFailure[row.item.lastSequence]" :node-id="nodeId"
          @expand="keys => expandRow(row.item, keys)" @retry="node => emit('retry', node)">
          <template #tool-output="{ node, expanded }"><slot name="tool-output" :node="node" :expanded="expanded" /></template>
        </TraceStep>
        <slot name="after-step" :node="row.item" />
    </TimelineItem>
  </div>
</template>

<style scoped>
.trace-steps{display:contents;min-width:0;overflow-anchor:none}
.trace-step-row{min-width:0}.trace-step-row :deep(.ui-thought-chain){padding:0}
.trace-step-row:not(.trace-step-last) :deep(.ui-thought-chain-node-icon::after){display:block}
</style>
