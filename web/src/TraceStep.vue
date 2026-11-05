<script setup lang="ts">
import { computed, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from './ui'
import { Collapsible } from '@ark-ui/vue/collapsible'
import { CheckCircleOutlined, LoadingOutlined, CloseCircleOutlined, StopOutlined, RightOutlined } from '@antdv-next/icons'
import type { TraceNodeView as Node } from './ui/traceTypes'
import type { RunTraceNode } from './state/runTrace'
import { renderMarkdown } from './ui/markdown'

const props = defineProps<{
  item: Node; expanded: boolean; loading: boolean; failure?: string; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; retry: [node: RunTraceNode] }>()
const { t } = useI18n()
const slots = useSlots()
// Give tool output its own render effect. Position updates must not rerun the
// forwarded slot through every step, while output data stays reactive.
const ToolOutput = () => slots['tool-output']?.({ node: props.item, expanded: props.expanded })
const markdown = computed(() => renderMarkdown(props.item.body))
</script>

<template>
  <div v-if="item.kind === 'reasoning'" :id="nodeId(item.key)" class="trace-reasoning markdown" :aria-label="t('modelReasoning')" v-html="markdown"></div>
  <Collapsible.Root v-else class="ui-thought-chain" :open="expanded" lazy-mount unmount-on-exit @open-change="emit('expand', $event.open ? [item.key] : [])">
    <div class="ui-thought-chain-node" :class="`ui-thought-chain-node-${item.status || 'default'}`">
      <span class="ui-thought-chain-node-icon" aria-hidden="true"><LoadingOutlined v-if="item.status === 'loading'" class="ui-icon-spin" /><CloseCircleOutlined v-else-if="item.status === 'error'" /><StopOutlined v-else-if="item.status === 'abort'" /><CheckCircleOutlined v-else /></span>
      <div class="ui-thought-chain-node-box">
        <Collapsible.Trigger v-if="item.collapsible !== false" class="ui-thought-chain-node-header">
          <span class="ui-thought-chain-node-title"><span :id="nodeId(item.key)" :title="item.input?.command || item.summary">{{ item.title }}</span></span>
          <RightOutlined class="ui-thought-chain-node-collapse-icon" :class="{ expanded }" aria-hidden="true" />
        </Collapsible.Trigger>
        <div v-else class="ui-thought-chain-node-title"><span :id="nodeId(item.key)" :title="item.input?.command || item.summary">{{ item.title }}</span></div>
        <div v-if="item.description" class="ui-thought-chain-node-description">{{ item.description }}</div>
        <Collapsible.Content v-if="item.collapsible !== false" class="ui-thought-chain-node-content trace-step-content ui-collapse">
          <small v-if="loading" class="trace-step-loading" role="status">{{ t('traceStepLoading') }}</small>
          <div v-else-if="failure" class="trace-step-error"><small role="alert">{{ t('traceStepUnavailable') }}</small><Button @click="emit('retry', item)">{{ t('retry') }}</Button></div>
          <ToolOutput v-if="item.kind === 'tool'" />
          <a v-else-if="item.kind === 'source'" :href="item.body" target="_blank" rel="noopener noreferrer">{{ item.body }}</a>
          <div v-else-if="item.kind === 'text' || item.markdown" class="markdown" v-html="markdown"></div>
          <pre v-else-if="item.body" class="trace-output">{{ item.body }}</pre>
        </Collapsible.Content>
      </div>
    </div>
  </Collapsible.Root>
</template>

<style scoped>
.trace-reasoning{margin:.35rem 0 .85rem;padding-inline-start:24px;color:var(--muted)}
.trace-step-loading{font-size:15px;color:var(--text-secondary)}.trace-step-error{display:flex;align-items:center;gap:.5rem;font-size:15px}
</style>
