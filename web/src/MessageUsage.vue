<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Popover, Button, Select } from 'antdv-next'
import { InfoCircleOutlined } from '@antdv-next/icons'
import type { ModelResultView as Result } from './client/types'
import { useEscapeDismiss } from './composables/useEscape'
import { formatTime } from './ui/datetime.ts'
const props = defineProps<{ results: Result[]; modelName?: string }>()
const { t, te, locale } = useI18n()
const compactNumber = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
function statusLabel(status: string) { const key = 'status' + status.replace(/(^|_)([a-z])/g, (_, __: string, letter: string) => letter.toUpperCase()); return te(key) ? t(key) : status }
function rate(result: Result) {
  return result.usage && Number.isFinite(result.usage?.outputTokens) && result.usage?.outputTokens >= 0 && Number.isFinite(result.durationMs) && result.durationMs! > 0 ? result.usage?.outputTokens * 1000 / result.durationMs! : null
}
function tps(result: Result) { return rate(result)?.toFixed(1) ?? null }
function isChild(result: Result) { return result.taskId ? result.taskId !== 'main' : !!result.depth }
function isFailure(result: Result) { return /failed|error|cancel|interrupted|blocked|content_filter/i.test(result.finishReason) }
function finishLabel(result: Result) {
  if (['stop', 'end_turn', 'completed'].includes(result.finishReason)) return t('statusSucceeded')
  if (['tool_calls', 'tool_use'].includes(result.finishReason)) return t('usageTools')
  if (['length', 'max_tokens'].includes(result.finishReason)) return t('usageOutputLimit')
  return statusLabel(result.finishReason)
}
function reported(value: number | undefined) { return typeof value === 'number' && Number.isFinite(value) ? compactNumber.format(value) : '—' }
function exact(value: number | undefined) { return typeof value === 'number' && Number.isFinite(value) ? value.toLocaleString(locale.value) : '—' }
function timeLabel(result: Result) {
  return result.createdAt ? formatTime(result.createdAt, locale.value) : '—'
}
const open = ref(false)
useEscapeDismiss(open)
const usageRoot = ref<HTMLElement | null>(null)
let lockedMessages: HTMLElement | null = null
function lockMessageScroll(locked: boolean) {
  if (locked) {
    const messages = usageRoot.value?.closest<HTMLElement>('.messages') || null
    if (lockedMessages && lockedMessages !== messages) lockedMessages.classList.remove('usage-scroll-locked')
    lockedMessages = messages
    lockedMessages?.classList.add('usage-scroll-locked')
    return
  }
  lockedMessages?.classList.remove('usage-scroll-locked')
  lockedMessages = null
}
watch(open, visible => lockMessageScroll(visible), { flush: 'post' })
onUnmounted(() => lockMessageScroll(false))
const sort = ref<'newest' | 'oldest' | 'tps'>('newest')
const filter = ref<'all' | 'main' | 'subtasks' | 'failures'>('all')
const page = ref(0)
const pageSize = 20
const sortOptions = computed(() => [{ label: t('usageRecent'), value: 'newest' }, { label: t('usageOldest'), value: 'oldest' }, { label: t('usageFastest'), value: 'tps' }])
const filterOptions = computed(() => [{ label: t('usageAllTasks'), value: 'all' }, { label: t('usageMainTask'), value: 'main' }, { label: t('usageSubtasks'), value: 'subtasks' }, { label: t('usageFailures'), value: 'failures' }])
const displayResults = computed(() => props.results)
const ordered = computed(() => displayResults.value.filter(result => filter.value === 'all' || filter.value === 'main' && !isChild(result) || filter.value === 'subtasks' && isChild(result) || filter.value === 'failures' && isFailure(result)).sort((a, b) => {
  if (sort.value === 'tps') return (rate(b) ?? -1) - (rate(a) ?? -1) || b.sequence - a.sequence
  const delta = a.sequence - b.sequence
  return sort.value === 'oldest' ? delta : -delta
}))
const pages = computed(() => Math.max(1, Math.ceil(ordered.value.length / pageSize)))
const visible = computed(() => ordered.value.slice(page.value * pageSize, (page.value + 1) * pageSize))
const mainCalls = computed(() => displayResults.value.filter(result => !isChild(result)).length)
const reportedCalls = computed(() => displayResults.value.filter(result => result.usage).length)
watch([sort, filter], () => { page.value = 0 })
watch(pages, count => { page.value = Math.min(page.value, count - 1) })
const total = computed(() => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, reasoningTokens: 0 }
  for (const result of displayResults.value) if (result.usage) for (const key of Object.keys(usage) as (keyof typeof usage)[]) usage[key] += result.usage[key] || 0
  return usage
})
const averageTPS = computed(() => {
  const measured = displayResults.value.filter(result => tps(result) !== null)
  if (!measured.length || measured.length !== displayResults.value.length) return null
  return (total.value.outputTokens * 1000 / measured.reduce((sum, result) => sum + result.durationMs!, 0)).toFixed(1)
})
</script>
<template>
  <div ref="usageRoot" class="message-model-results" :class="{ 'usage-open': open }">
    <span v-if="props.modelName" class="message-model-name">{{ props.modelName }}</span>
    <template v-if="displayResults.length">
      <span v-if="displayResults.some(result => result.usage)" class="token-usage" :title="t('tokenUsage', { input: exact(total.inputTokens), output: exact(total.outputTokens), cached: exact(total.cacheReadTokens), reasoning: exact(total.reasoningTokens) })">· {{ t('tokenUsage', { input: reported(total.inputTokens), output: reported(total.outputTokens), cached: reported(total.cacheReadTokens), reasoning: reported(total.reasoningTokens) }) }}</span>
      <span v-else>{{ t('usageUnavailable') }}</span>
      <span class="model-tps" :title="t(averageTPS === null ? 'modelTPSUnavailableHelp' : 'modelTPSHelp')">{{ t('modelTPS', { value: averageTPS ?? '—' }) }}</span>
      <Popover v-model:open="open" placement="top" trigger="click" root-class="usage-popover" :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
        <template #content>
          <section class="usage-details" :aria-label="t('usageDetails')" @wheel.stop @touchmove.stop>
            <div class="usage-summary">
              <div class="usage-summary-heading"><strong>{{ t('usageSummary') }}</strong><span>{{ t('usageCalls', { count: displayResults.length }) }}</span><small>{{ t('usageMainCalls', { count: mainCalls }) }}<template v-if="displayResults.length > mainCalls"> · {{ t('usageChildCalls', { count: displayResults.length - mainCalls }) }}</template></small></div>
              <dl><div><dt>{{ t('usageInput') }}</dt><dd :title="exact(total.inputTokens)">{{ reported(total.inputTokens) }}</dd></div><div><dt>{{ t('usageOutput') }}</dt><dd :title="exact(total.outputTokens)">{{ reported(total.outputTokens) }}</dd></div><div><dt>{{ t('usageCache') }}</dt><dd :title="exact(total.cacheReadTokens)">{{ reported(total.cacheReadTokens) }}</dd></div><div><dt>{{ t('usageReasoning') }}</dt><dd :title="exact(total.reasoningTokens)">{{ reported(total.reasoningTokens) }}</dd></div></dl>
              <small>{{ t('usageReportedCalls', { count: reportedCalls, total: displayResults.length }) }}<span :title="t(averageTPS === null ? 'modelTPSUnavailableHelp' : 'modelTPSHelp')">{{ t('modelTPS', { value: averageTPS ?? '—' }) }}</span></small>
            </div>
            <div class="usage-controls"><Select v-model:value="filter" size="small" :options="filterOptions" :aria-label="t('usageFilter')" /><Select v-model:value="sort" size="small" :options="sortOptions" :aria-label="t('usageSort')" /></div>
            <div class="usage-table-scroll" tabindex="0">
              <table class="usage-table"><caption class="usage-visually-hidden">{{ t('usageDetails') }}</caption><thead><tr><th scope="col">{{ t('usageTime') }}</th><th scope="col">{{ t('usageStatus') }}</th><th scope="col">{{ t('usageInput') }}</th><th scope="col">{{ t('usageOutput') }}</th><th scope="col">{{ t('usageCache') }}</th><th scope="col">{{ t('usageReasoning') }}</th><th scope="col">{{ t('usageTPS') }}</th></tr></thead>
                <tbody><tr v-for="result in visible" :key="result.sequence" :data-turn="result.turn"><td><time :datetime="result.createdAt" :title="result.createdAt || undefined">{{ timeLabel(result) }}</time></td><td :class="{ 'usage-failure': isFailure(result) }" :title="result.finishReason">{{ finishLabel(result) }}</td><td :title="exact(result.usage?.inputTokens)">{{ reported(result.usage?.inputTokens) }}</td><td :title="exact(result.usage?.outputTokens)">{{ reported(result.usage?.outputTokens) }}</td><td :title="exact(result.usage?.cacheReadTokens)">{{ reported(result.usage?.cacheReadTokens) }}</td><td :title="exact(result.usage?.reasoningTokens)">{{ reported(result.usage?.reasoningTokens) }}</td><td :title="t(tps(result) === null ? 'modelTPSUnavailableHelp' : 'modelTPSHelp')">{{ tps(result) ?? '—' }}</td></tr><tr v-if="!visible.length"><td colspan="7" class="usage-empty">{{ t('usageNoMatches') }}</td></tr></tbody>
              </table>
            </div>
            <div class="usage-pagination"><small>{{ t('usagePage', { start: ordered.length ? page * pageSize + 1 : 0, end: Math.min((page + 1) * pageSize, ordered.length), total: ordered.length }) }}</small><Button size="small" :disabled="page === 0" @click="page--">{{ t('usagePrevious') }}</Button><Button size="small" :disabled="page >= pages - 1" @click="page++">{{ t('usageNext') }}</Button></div>
          </section>
        </template>
        <Button type="text" size="small" :aria-label="t('usageDetails')" :aria-expanded="open"><InfoCircleOutlined /><span v-if="displayResults.length > 1">{{ t('modelCalls', { count: displayResults.length }) }}</span></Button>
      </Popover>
    </template>
  </div>
</template>

<style scoped>
.usage-details{display:flex;flex-direction:column;gap:.55rem;width:min(880px,calc(100vw - 3rem));max-width:100%;max-height:min(600px,72dvh);overflow:hidden;color:var(--text-secondary);font-size:calc(.75rem + 2px);min-width:0}
.usage-summary{flex:none;padding:.5rem .6rem;border:1px solid var(--border);border-radius:8px;background:var(--surface)}
.usage-summary-heading{display:flex;align-items:baseline;gap:.35rem .7rem;flex-wrap:wrap}.usage-summary-heading strong{font-size:calc(.82rem + 2px)}.usage-summary-heading small{font-size:11.375px;color:var(--muted)}
.usage-summary dl{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:.4rem;margin:.4rem 0}.usage-summary dt{font-size:calc(.68rem + 2px);color:var(--muted)}.usage-summary dd{margin:0;font-variant-numeric:tabular-nums}.usage-summary>small{font-size:calc(.67rem + 2px);color:var(--muted)}
.usage-controls{display:flex;gap:.4rem;flex:none}.usage-controls :deep(.ant-select){width:10.5rem;max-width:50%;min-width:0}
.usage-table-scroll{min-height:0;overflow:auto;overscroll-behavior:contain;scrollbar-color:var(--scrollbar) transparent;border:1px solid var(--border);border-radius:7px}
.usage-table{border-collapse:collapse;min-width:640px;width:100%;font-size:calc(.72rem + 2px);line-height:1.3;font-variant-numeric:tabular-nums}.usage-table th{position:sticky;top:0;z-index:1;background:var(--surface-subtle);color:var(--text-secondary);font-weight:600;white-space:nowrap}.usage-table th,.usage-table td{padding:.3rem .4rem;text-align:right;border-bottom:1px solid var(--border)}.usage-table th:nth-child(-n+2),.usage-table td:nth-child(-n+2){text-align:left}.usage-table td{white-space:nowrap}.usage-table tr:last-child td{border-bottom:0}.usage-failure{color:var(--danger)}.usage-table td.usage-empty{text-align:center;padding:.8rem;color:var(--muted)}
.usage-pagination{display:flex;align-items:center;gap:.35rem;flex:none}.usage-pagination small{margin-right:auto;color:var(--muted);font-size:calc(.68rem + 2px)}.usage-visually-hidden{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}
@media(max-height:520px){.usage-details{max-height:76dvh;gap:.3rem}.usage-summary{padding:.25rem .4rem}.usage-summary dl{margin:.2rem 0}.usage-summary-heading strong{font-size:calc(.75rem + 2px)}.usage-table th,.usage-table td{padding:.2rem .35rem}}
</style>
