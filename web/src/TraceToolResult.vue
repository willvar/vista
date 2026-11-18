<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RunTraceNode } from './state/runTrace'
import { renderMarkdown } from './ui/markdown'
import PlainTextOutput from './PlainTextOutput.vue'
import FileDiffOutput from './FileDiffOutput.vue'
import TraceResultCard from './TraceResultCard.vue'

type ToolResult = Pick<RunTraceNode, 'body' | 'markdown' | 'search' | 'instructions' | 'diff' | 'diffs' | 'truncated'> & {
  error?: string; resultSummaryKey?: string; resultSummaryParams?: Record<string, string | number>
}
const props = withDefaults(defineProps<{ node: ToolResult; showError?: boolean }>(), { showError: true })
const { t } = useI18n()
const kind = computed(() => props.node.search ? 'search' : props.node.instructions ? 'instructions' : props.node.markdown ? 'markdown' : 'text')
const title = computed(() => t(props.node.search ? 'traceSearchResults' : props.node.instructions ? 'traceInstructionResults' : props.node.markdown ? 'traceResult' : 'traceOutput'))
const summary = computed(() => {
  const { search, instructions, resultSummaryKey, resultSummaryParams } = props.node
  if (search) {
    const count = search.count ?? search.paths?.length ?? search.matches.filter(match => !match.context).length
    return search.filesScanned === undefined ? t('traceSearchCount', { count }) : t('traceSearchSummary', { matches: count, files: search.filesScanned })
  }
  if (instructions) return t(resultSummaryKey || (instructions.length ? 'traceInstructionFiles' : 'traceNoInstructionFiles'), { count: instructions.length })
  return resultSummaryKey ? t(resultSummaryKey, resultSummaryParams || {}) : ''
})
const searchGroups = computed(() => {
  type Match = NonNullable<ToolResult['search']>['matches'][number]
  const groups = new Map<string, Match[]>()
  for (const path of props.node.search?.paths || []) groups.set(path, [])
  for (const match of props.node.search?.matches || []) {
    if (!groups.has(match.path)) groups.set(match.path, [])
    groups.get(match.path)!.push(match)
  }
  return [...groups].map(([path, matches]) => ({ path, matches }))
})
const copyText = computed(() => {
  const { search, instructions, body } = props.node
  if (search) return [summary.value, ...searchGroups.value.map(group => [group.path, ...group.matches.map(match => `${match.line == null ? '' : `${match.line}: `}${match.text}`)].join('\n')),
    ...(search.truncated ? [t('traceSearchPartial')] : []), ...(search.warnings || []).map(warning => `${warning.path}: ${warning.error}`)].join('\n\n')
  if (instructions) return [summary.value, ...instructions.map(file => file.path + (file.bytes == null ? '' : ` · ${t('traceFileBytes', { bytes: file.bytes })}`))].join('\n')
  return body
})
</script>

<template>
  <FileDiffOutput v-if="node.diff" :diff="node.diff" />
  <div v-else-if="node.diffs?.length" class="trace-file-diffs"><FileDiffOutput v-for="diff in node.diffs" :key="diff.path" :diff="diff" /></div>
  <TraceResultCard v-else-if="node.search || node.instructions || node.body" :title="title" :summary="summary" :text="copyText" :kind="kind">
    <template v-if="node.search">
      <small v-if="!(node.search.count ?? node.search.paths?.length ?? node.search.matches.length)">{{ t('traceNoMatches') }}</small>
      <div class="trace-search-results">
        <section v-for="group in searchGroups" :key="group.path" class="trace-search-file">
          <header class="trace-search-path"><code>{{ group.path }}</code></header>
          <ul v-if="group.matches.length">
            <li v-for="(match, index) in group.matches" :key="index" class="trace-search-row" :class="{ 'trace-search-context': match.context }">
              <span class="trace-search-line">{{ match.line }}</span><pre class="trace-output">{{ match.text }}</pre>
            </li>
          </ul>
        </section>
      </div>
      <small v-if="node.search.truncated" class="trace-search-warning">{{ t('traceSearchPartial') }}</small>
      <p v-for="warning in node.search.warnings" :key="warning.path" class="trace-search-warning"><code>{{ warning.path }}</code>: {{ warning.error }}</p>
    </template>
    <ul v-else-if="node.instructions" class="trace-instruction-files"><li v-for="file in node.instructions" :key="file.path"><code>{{ file.path }}</code><small v-if="file.bytes != null">{{ t('traceFileBytes', { bytes: file.bytes }) }}</small></li></ul>
    <div v-else-if="node.markdown" class="markdown" v-html="renderMarkdown(node.body)"></div>
    <PlainTextOutput v-else :content="node.body" />
  </TraceResultCard>
  <small v-else-if="node.resultSummaryKey" class="trace-result-summary">{{ t(node.resultSummaryKey, node.resultSummaryParams || {}) }}</small>
  <p v-if="showError && node.error" class="tool-output-error error">{{ node.error }}</p>
</template>

<style scoped>
.trace-file-diffs{display:grid;gap:.65rem}
.trace-result-summary{font-size:15px;color:var(--text-secondary)}
.trace-search-results{min-width:0}.trace-search-file+.trace-search-file{margin-top:.75rem}
.trace-search-path{padding:.4rem .5rem;border-bottom:1px solid var(--border);border-radius:4px 4px 0 0;background:var(--surface-subtle);font-size:13px;overflow-wrap:anywhere}
.trace-search-path code{font-family:var(--font-mono,monospace)}
.trace-search-file ul{list-style:none;padding:0;margin:.25rem 0 0}
.trace-search-row{display:grid;grid-template-columns:minmax(3ch,max-content) minmax(0,1fr);gap:.6rem;padding:.15rem .25rem;align-items:start}
.trace-search-row .trace-output{margin:0;font-family:var(--font-mono,monospace);font-size:13px;line-height:1.7}
.trace-search-line{padding-inline:.25rem;color:var(--muted);font:13px/1.7 var(--font-mono,monospace);text-align:right;user-select:none}
.trace-search-context .trace-output{color:var(--muted)}
.trace-search-warning{display:block;margin:.6rem 0 0;color:var(--muted);font-size:13px;overflow-wrap:anywhere}
.trace-instruction-files{list-style:none;padding:0;margin:0}.trace-instruction-files li{display:flex;align-items:baseline;justify-content:space-between;gap:.4rem .75rem;flex-wrap:wrap;padding:.45rem .25rem;border-bottom:1px solid var(--border-subtle)}.trace-instruction-files li:last-child{border-bottom:0}.trace-instruction-files code{font:13px/1.7 var(--font-mono,monospace);overflow-wrap:anywhere}.trace-instruction-files small{color:var(--muted);font-size:12px;white-space:nowrap}
</style>
