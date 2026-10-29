<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { RunTraceNode } from './state/runTrace'
import { renderMarkdown } from './ui/markdown'
import PlainTextOutput from './PlainTextOutput.vue'

type ToolResult = Pick<RunTraceNode, 'body' | 'markdown' | 'search' | 'instructions' | 'truncated'> & {
  error?: string; resultSummaryKey?: string; resultSummaryParams?: Record<string, string | number>
}
withDefaults(defineProps<{ node: ToolResult; showError?: boolean }>(), { showError: true })
const { t } = useI18n()
</script>

<template>
  <p v-if="showError && node.error" class="tool-output-error error">{{ node.error }}</p>
  <p v-if="node.resultSummaryKey && node.body && !node.search && !node.instructions" class="trace-result-summary">{{ t(node.resultSummaryKey, node.resultSummaryParams || {}) }}</p>
  <template v-if="node.search">
    <p class="trace-result-summary">{{ t('traceSearchSummary', { matches: node.search.matches.length, files: node.search.filesScanned ?? '—' }) }}</p>
    <small v-if="!node.search.matches.length">{{ t('traceNoMatches') }}</small>
    <ul class="trace-search-results"><li v-for="(match, index) in node.search.matches" :key="index"><code>{{ match.path }}{{ match.line ? `:${match.line}` : '' }}</code><pre class="trace-output">{{ match.text }}</pre></li></ul>
    <small v-if="node.search.truncated">{{ t('traceSearchPartial') }}</small>
  </template>
  <template v-else-if="node.instructions">
    <p class="trace-result-summary">{{ t(node.resultSummaryKey || (node.instructions.length ? 'traceInstructionFiles' : 'traceNoInstructionFiles'), { count: node.instructions.length }) }}</p>
    <ul class="trace-instruction-files"><li v-for="file in node.instructions" :key="file.path"><code>{{ file.path }}</code><small v-if="file.bytes != null">{{ t('traceFileBytes', { bytes: file.bytes }) }}</small></li></ul>
  </template>
  <div v-else-if="node.body && node.markdown" class="markdown" v-html="renderMarkdown(node.body)"></div>
  <PlainTextOutput v-else-if="node.body" :content="node.body" />
  <small v-else-if="node.resultSummaryKey" class="trace-result-summary">{{ t(node.resultSummaryKey, node.resultSummaryParams || {}) }}</small>
  <small v-if="node.truncated" class="trace-result-summary">{{ t('traceReturnedPartial') }}</small>
</template>

<style scoped>
.trace-result-summary{font-size:15px;color:var(--text-secondary)}.trace-search-results,.trace-instruction-files{list-style:none;padding:0;margin:.5rem 0}.trace-search-results li{margin:.65rem 0}.trace-search-results code,.trace-instruction-files code{font-size:15px;overflow-wrap:anywhere}.trace-instruction-files li{display:flex;gap:.5rem;flex-wrap:wrap;margin:.3rem 0}.trace-instruction-files small{font-size:14px;color:var(--muted)}.trace-search-results .trace-output{margin:.2rem 0}.tool-output-error{color:var(--danger);overflow-wrap:anywhere}
</style>
