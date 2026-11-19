<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TraceNodeView as Node } from './ui/traceTypes'
import type { RunTraceNode } from './state/runTrace'
import type { TraceLookupRow, TraceLookupStep } from './state/runTraceLookups'
import type { TimelineLayout } from './state/conversationTimeline'
import TraceStep from './TraceStep.vue'
import TraceLookupGroup from './TraceLookupGroup.vue'
import TimelineItem from './TimelineItem.vue'

const props = defineProps<{
  rows: TraceLookupRow<Node>[]; stepCount: number; expandedKeys: string[]; layout: Record<string, TimelineLayout>; paused: boolean; placeholders: Set<string>
  nodeLoading: Record<number, boolean>; nodeFailure: Record<number, string>; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; toggleLookup: [row: TraceLookupRow<Node>]; retry: [node: RunTraceNode]; measure: [element: Element | null] }>()
const { t } = useI18n()
const visible = computed(() => props.rows.flatMap(row => props.layout[row.key] ? [{ ...row, layout: props.layout[row.key]! }] : []))
function expandRow(item: Node, keys: string[]) {
  const remaining = props.expandedKeys.filter(key => key !== item.key)
  emit('expand', keys.includes(item.key) ? [...remaining, item.key] : remaining)
}
function lookupHint(step?: TraceLookupStep) {
  if (!step) return ''
  const label = step.hint ? t(step.hint.key, step.hint.params || {}) : t('traceCompleted')
  return step.reason ? `${label}: ${step.reason}` : label
}

</script>

<template>
  <div class="trace-steps" :data-step-count="stepCount">
    <TimelineItem v-for="row in visible" :key="row.key" :layout="row.layout" :paused="paused" :placeholder="placeholders.has(String(row.layout.key))" @measure="element => emit('measure', element)"
      class="timeline-row trace-task trace-task-group" :class="[row.members ? 'trace-lookup-row' : 'trace-step-row', { 'trace-step-last': row.index === stepCount - 1 }]"
      :data-step-key="row.key" :data-timeline-sequence="row.item.sequence"
      :data-task-id="row.item.taskId" :data-agent-role="row.item.taskId === 'main' ? 'main' : 'subagent'">
      <slot v-if="!row.lookup" name="before-step" :node="row.item" :index="row.index" />
      <TraceLookupGroup v-if="row.members" :members="row.members" :open="!!row.open" @toggle="emit('toggleLookup', row)" />
      <TraceStep v-else :item="row.item" :expanded="expandedKeys.includes(row.item.key)" :loading="!!nodeLoading[row.item.lastSequence]" :failure="nodeFailure[row.item.lastSequence]" :node-id="nodeId"
        :lookup-hint="lookupHint(row.lookup)" @expand="keys => expandRow(row.item, keys)" @retry="node => emit('retry', node)">
        <template #tool-output="{ node, expanded }"><slot name="tool-output" :node="node" :expanded="expanded" /></template>
      </TraceStep>
      <slot v-if="!row.open" name="after-step" :node="row.members?.at(-1)?.item || row.item" />
    </TimelineItem>
  </div>
</template>

<style scoped>
.trace-steps{display:contents;min-width:0;overflow-anchor:none}
.trace-lookup-row,.trace-step-row{min-width:0}.trace-step-row :deep(.ui-thought-chain){padding:0}
.trace-step-row:not(.trace-step-last) :deep(.ui-thought-chain-node-icon::after){display:block}
</style>
