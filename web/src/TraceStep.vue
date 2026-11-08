<script setup lang="ts">
import { computed, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from './ui'
import { CheckCircleOutlined, LoadingOutlined, CloseCircleOutlined, StopOutlined } from '@antdv-next/icons'
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
const canReadFull = computed(() => props.item.kind === 'tool' && !!(props.item.truncated || props.item.outputRef || props.item.artifactIds.length))
const markdown = computed(() => renderMarkdown(props.item.body))
</script>

<template>
  <div v-if="item.kind === 'reasoning'" :id="nodeId(item.key)" class="trace-reasoning markdown" :aria-label="t('modelReasoning')" v-html="markdown"></div>
  <div v-else class="ui-thought-chain trace-process-message">
    <div class="ui-thought-chain-node" :class="`ui-thought-chain-node-${item.status || 'default'}`">
      <span class="ui-thought-chain-node-icon" aria-hidden="true"><LoadingOutlined v-if="item.status === 'loading'" class="ui-icon-spin" /><CloseCircleOutlined v-else-if="item.status === 'error'" /><StopOutlined v-else-if="item.status === 'abort'" /><CheckCircleOutlined v-else /></span>
      <div class="ui-thought-chain-node-box">
        <div class="ui-thought-chain-node-title"><span :id="nodeId(item.key)" :title="item.input?.command || item.summary">{{ item.title }}</span><Button v-if="canReadFull" class="trace-read-full" variant="link" :aria-expanded="expanded" @click="emit('expand', expanded ? [] : [item.key])">{{ t(expanded ? 'traceHideFull' : 'traceReadFull') }}</Button></div>
        <div v-if="item.description" class="ui-thought-chain-node-description">{{ item.description }}</div>
        <div class="ui-thought-chain-node-content trace-step-content">
          <small v-if="loading" class="trace-step-loading" role="status">{{ t('traceStepLoading') }}</small>
          <div v-if="failure" class="trace-step-error"><small role="alert">{{ t('traceStepUnavailable') }}</small><Button @click="emit('retry', item)">{{ t('retry') }}</Button></div>
          <ToolOutput v-if="item.kind === 'tool'" />
          <a v-else-if="item.kind === 'source'" :href="item.body" target="_blank" rel="noopener noreferrer">{{ item.body }}</a>
          <div v-else-if="item.kind === 'text' || item.markdown" class="markdown" v-html="markdown"></div>
          <pre v-else-if="item.body" class="trace-output">{{ item.body }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.trace-reasoning{margin:.35rem 0 .85rem;color:var(--muted);font-size:var(--font-size-body);line-height:1.65}
.trace-process-message{color:var(--muted);font-size:var(--font-size-body)}
.ui-thought-chain-node-title{display:flex;flex-wrap:wrap;align-items:baseline;gap:.15rem .75rem;font-size:var(--font-size-body);font-weight:500;color:var(--text-secondary);line-height:1.5}
.ui-thought-chain-node-description{font-size:14px;color:var(--muted);line-height:1.6}
.trace-step-content{color:var(--muted)}.trace-step-loading{font-size:15px;color:var(--text-secondary)}.trace-step-error{display:flex;align-items:center;gap:.5rem;font-size:15px}
.trace-read-full{flex:none}
</style>
