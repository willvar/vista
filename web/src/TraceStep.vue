<script setup lang="ts">
import { computed, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from 'antdv-next'
import ThoughtChain from '@antdv-next/x/dist/thought-chain/index'
import type { ThoughtChainItemType } from '@antdv-next/x/dist/thought-chain/interface'
import type { RunTraceNode } from './state/runTrace'
import { renderMarkdown } from './ui/markdown'

type Node = RunTraceNode & ThoughtChainItemType
const props = defineProps<{
  item: Node; expanded: boolean; loading: boolean; failure?: string; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; retry: [node: RunTraceNode] }>()
const { t } = useI18n()
const slots = useSlots()
// Give tool output its own render effect. Position updates must not rerun the
// forwarded slot through every ThoughtChain, while output data stays reactive.
const ToolOutput = () => slots['tool-output']?.({ node: props.item, expanded: props.expanded })
// Row positions change during scrolling; the ThoughtChain inputs do not.
const items = computed(() => [props.item])
const expandedKeys = computed(() => props.expanded ? [props.item.key] : [])
const markdown = computed(() => renderMarkdown(props.item.body))
</script>

<template>
  <div v-if="item.kind === 'reasoning'" :id="nodeId(item.key)" class="trace-reasoning markdown" :aria-label="t('modelReasoning')" v-html="markdown"></div>
  <ThoughtChain v-else :items="items" :expanded-keys="expandedKeys" line="solid" @expand="keys => emit('expand', keys)">
    <template #title><span :id="nodeId(item.key)" :title="item.input?.command || item.summary">{{ item.title }}</span></template>
    <template #content>
      <!-- Keep a content VNode while closed so ThoughtChain retains its expand
           control. Expensive slot projection and Markdown wait until opened. -->
      <div v-if="item.collapsible !== false" class="trace-step-content">
        <template v-if="expanded">
          <small v-if="loading" class="trace-step-loading" role="status">{{ t('traceStepLoading') }}</small>
          <div v-else-if="failure" class="trace-step-error"><small role="alert">{{ t('traceStepUnavailable') }}</small><Button size="small" @click="emit('retry', item)">{{ t('retry') }}</Button></div>
          <ToolOutput v-if="item.kind === 'tool'" />
          <a v-else-if="item.kind === 'source'" :href="item.body" target="_blank" rel="noopener noreferrer">{{ item.body }}</a>
          <div v-else-if="item.kind === 'text' || item.markdown" class="markdown" v-html="markdown"></div>
          <pre v-else-if="item.body" class="trace-output">{{ item.body }}</pre>
        </template>
      </div>
    </template>
  </ThoughtChain>
</template>

<style scoped>
.trace-reasoning{margin:.35rem 0 .85rem;padding-inline-start:24px;color:var(--muted)}
.trace-step-loading{font-size:15px;color:var(--text-secondary)}.trace-step-error{display:flex;align-items:center;gap:.5rem;font-size:15px}
</style>
