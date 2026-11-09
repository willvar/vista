<script setup lang="ts">
import { computed, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from './ui'
import { IconChevronRight, IconCircleCheck, IconLoader2, IconCircleX, IconPlayerStop } from '@tabler/icons-vue'
import type { TraceNodeView as Node } from './ui/traceTypes'
import type { RunTraceNode } from './state/runTrace'
import { compactTraceText, reasoningDisplayText } from './state/runTrace'
import { renderMarkdown } from './ui/markdown'

const props = defineProps<{
  item: Node; expanded: boolean; loading: boolean; failure?: string; nodeId: (key: string) => string
}>()
const emit = defineEmits<{ expand: [keys: string[]]; retry: [node: RunTraceNode] }>()
const { t } = useI18n()
const slots = useSlots()
const ToolOutput = () => slots['tool-output']?.({ node: props.item, expanded: props.expanded })
const collapsible = computed(() => ['tool', 'source'].includes(props.item.kind))
const markdown = computed(() => renderMarkdown(props.item.kind === 'reasoning' ? reasoningDisplayText(props.item.body) : props.item.body))
const inputText = computed(() => props.item.kind === 'tool' ? props.item.input?.command || props.item.summary || '' : props.item.kind === 'source' ? props.item.body : '')
const inputPreview = computed(() => compactTraceText(inputText.value))
const longInput = computed(() => inputPreview.value !== inputText.value.replace(/\s+/g, ' ').trim() || inputText.value.includes('\n'))
function toggle() { if (collapsible.value) emit('expand', props.expanded ? [] : [props.item.key]) }
</script>

<template>
  <div v-if="item.kind === 'reasoning'" :id="nodeId(item.key)" class="trace-reasoning markdown" :aria-label="t('modelReasoning')" v-html="markdown"></div>
  <div v-else class="ui-thought-chain trace-process-message">
    <div class="ui-thought-chain-node" :class="`ui-thought-chain-node-${item.status || 'default'}`">
      <div class="ui-thought-chain-node-box">
        <component :is="collapsible ? 'button' : 'div'" :type="collapsible ? 'button' : undefined" class="ui-thought-chain-node-header" :aria-expanded="collapsible ? expanded : undefined" :aria-controls="collapsible ? `${nodeId(item.key)}-content` : undefined" :title="inputText || undefined" @click="toggle">
          <span class="trace-step-label"><span class="ui-thought-chain-node-icon" aria-hidden="true"><IconLoader2 v-if="item.status === 'loading'" class="ui-icon-spin" /><IconCircleX v-else-if="item.status === 'error'" /><IconPlayerStop v-else-if="item.status === 'abort'" /><IconCircleCheck v-else /></span><span class="ui-thought-chain-node-title"><span :id="nodeId(item.key)">{{ item.title }}<template v-if="inputText">: <span class="trace-input-preview" :class="{ 'trace-input-code': item.input?.command }">{{ inputPreview }}</span></template></span></span></span>
          <IconChevronRight v-if="collapsible" class="ui-thought-chain-node-collapse-icon" :class="{ expanded }" aria-hidden="true" />
        </component>
        <div v-if="!collapsible || expanded" :id="`${nodeId(item.key)}-content`" class="trace-step-details">
        <pre v-if="inputText && longInput" class="trace-input-full" :class="{ 'trace-input-code': item.input?.command }">{{ inputText }}</pre>
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
  </div>
</template>

<style scoped>
.trace-reasoning{margin:.35rem 0 .85rem;color:var(--muted);font-size:15px;line-height:1.7}
.trace-reasoning :deep(strong){font-weight:500}
.trace-process-message{color:var(--muted);font-size:var(--font-size-body)}
.ui-thought-chain-node{display:block}
.ui-thought-chain-node-header{align-items:center;min-height:var(--control-height);padding-inline-end:4px;color:var(--text-secondary);line-height:1.5}
.trace-step-details{min-width:0;padding-inline-start:24px}
button.ui-thought-chain-node-header:hover{color:var(--text)}
.trace-step-label{display:flex;align-items:flex-start;gap:8px;flex:1;min-width:0;font-size:15px;line-height:1.5;overflow-wrap:anywhere}
.ui-thought-chain-node-title{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;min-width:0;font-size:15px;font-weight:500;line-height:1.5}
.ui-thought-chain-node-icon{align-items:center;justify-content:center;height:1.5em;margin-top:0}
.ui-thought-chain-node-header>.ui-thought-chain-node-collapse-icon{align-self:center;margin-top:0}
.ui-thought-chain-node-description{margin:.25rem 0 0;font-size:13px;color:var(--muted);line-height:1.6}
.trace-input-preview{min-width:0;overflow-wrap:anywhere;font-size:14px;font-weight:400;color:var(--text-secondary)}
.trace-input-full{margin:.5rem 0 0;white-space:pre-wrap;overflow-wrap:anywhere;font-family:inherit;font-size:14px;line-height:1.65;color:var(--text-secondary)}
.trace-input-code{font-family:var(--font-family-code,monospace)}
.trace-step-content{color:var(--muted)}.trace-step-loading{font-size:15px;color:var(--text-secondary)}.trace-step-error{display:flex;align-items:center;gap:.5rem;font-size:15px}
@media(prefers-reduced-motion:reduce){.ui-thought-chain-node-collapse-icon{transition:none}}
</style>
