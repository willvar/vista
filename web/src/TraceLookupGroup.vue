<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconChevronRight, IconLoader2, IconSearch } from '@tabler/icons-vue'
import { summarizeTraceLookups, type TraceLookupStep } from './state/runTraceLookups'
import { toolOperationLabelKey } from './state/runTrace'

const props = defineProps<{ members: TraceLookupStep[]; open: boolean }>()
const emit = defineEmits<{ toggle: [] }>()
const { t, locale } = useI18n()
const summary = computed(() => summarizeTraceLookups(props.members))
const pending = computed(() => summary.value.unsettled.filter(step => step.status === 'loading'))
const problems = computed(() => summary.value.unsettled.filter(step => step.status === 'problem'))
const units = { search: 'traceLookupSearches', file: 'traceLookupFiles', page: 'traceLookupPages', directory: 'traceLookupDirectories' } as const
const list = computed(() => new Intl.ListFormat(locale.value, { type: 'unit', style: 'short' }))
const counts = computed(() => {
  const parts = Object.entries(units).flatMap(([kind, key]) => {
    const count = summary.value.counts[kind as keyof typeof units]
    return count ? [t(key, { count }, count)] : []
  })
  // Some Chinese unit lists concatenate without any punctuation.
  return locale.value.startsWith('zh') ? parts.join('、') : list.value.format(parts)
})
const title = computed(() => [t(pending.value.length ? 'traceLookupActive' : counts.value ? 'traceLookupDone' : 'traceLookup'), counts.value].filter(Boolean).join(' · '))
function label(step: TraceLookupStep) {
  return step.kind === 'search' || step.kind === 'directory' ? `${t(toolOperationLabelKey(step.item.toolName))}: ${step.label}` : step.label
}
function hint(step: TraceLookupStep) { return step.hint ? t(step.hint.key, step.hint.params || {}) : '' }
</script>

<template>
  <button type="button" class="ui-thought-chain-node-header trace-lookup-button" :aria-expanded="open" @click="emit('toggle')">
    <span class="ui-thought-chain-node-icon" aria-hidden="true"><IconLoader2 v-if="pending.length" class="ui-icon-spin" /><IconSearch v-else /></span>
    <span class="trace-lookup-label">
      <span class="trace-lookup-title">{{ title }}</span>
      <span v-if="!open && summary.preview.length" class="trace-lookup-preview">
        <template v-for="(step, index) in summary.preview" :key="step.item.key">
          <span v-if="index" aria-hidden="true"> · </span><span :title="step.target">{{ label(step) }}<template v-if="step.kind === 'search' && step.hint"> ({{ hint(step) }})</template></span>
        </template>
        <template v-if="summary.omitted"> · {{ t('traceLookupMore', { count: summary.omitted }, summary.omitted) }}</template>
      </span>
      <span v-if="!open && pending.length" class="trace-lookup-preview" :title="pending.at(-1)!.target">
        {{ label(pending.at(-1)!) }} · {{ hint(pending.at(-1)!) }}<template v-if="pending.length > 1"> · {{ t('traceLookupMore', { count: pending.length - 1 }, pending.length - 1) }}</template>
      </span>
    </span>
    <IconChevronRight class="ui-thought-chain-node-collapse-icon" :class="{ expanded: open }" aria-hidden="true" />
  </button>
  <p v-for="step in open ? [] : problems" :key="step.item.key" class="trace-lookup-problem" :title="step.target">
    {{ label(step) }} · {{ hint(step) }}<template v-if="step.reason">: {{ step.reason }}</template>
  </p>
</template>

<style scoped>
.trace-lookup-button{align-items:flex-start;min-height:var(--control-height);padding-inline-end:4px;text-align:start;color:var(--text-secondary)}
.trace-lookup-button:hover{color:var(--text)}
.trace-lookup-title{display:block;font-size:15px;font-weight:500;line-height:1.5}
.trace-lookup-label{flex:1;min-width:0;overflow-wrap:anywhere}
.trace-lookup-preview{display:block;color:var(--text-secondary);font-size:13px;line-height:1.5}
.trace-lookup-problem{margin:.2rem 0 0;padding-inline-start:24px;color:var(--danger);font-size:13px;line-height:1.5;overflow-wrap:anywhere}
</style>
