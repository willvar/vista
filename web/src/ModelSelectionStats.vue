<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Input as AInput, Pagination as APagination, Select as ASelect, Tabs as ATabs, TabPane as ATabPane } from 'antdv-next'
import type { ModelSelectionStats } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import ConfirmPopover from './ConfirmPopover.vue'

const { t, locale } = useI18n({ useScope: 'global' })
const workspace = useWorkspace()
const stats = ref<ModelSelectionStats | null>(null)
const loading = ref(false)
const resetting = ref(false)
const error = ref('')
const resetOpen = ref(false)
const tab = ref<'speed' | 'quality'>('speed')
const search = ref('')
const bucket = ref(-1)
const page = ref(1)
const pageSize = 10
const resetLabel = computed(() => t(tab.value === 'speed' ? 'resetSpeedStats' : 'resetQualityStats'))
const bucketOptions = computed(() => [
  { value: -1, label: t('statsAllInputs') },
  ...[0, 1, 2, 3].map(value => ({ value, label: t(`statsInputBucket${value}`) })),
])
const models = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  return (stats.value?.models || []).map(model => ({
    ...model, speed: bucket.value < 0 ? model.speed : model.speed.buckets.find(item => item.inputBucket === bucket.value)
      ?? { count: 0, failures: 0, medianMs: null, p90Ms: null, lowConfidence: true },
  })).filter(model => [model.modelId, workspace.modelNameById(model.modelId), ...model.providers].join(' ').toLocaleLowerCase().includes(query))
    .sort((a, b) => (tab.value === 'speed'
      ? (a.speed.medianMs ?? Infinity) - (b.speed.medianMs ?? Infinity)
      : Number(b.quality.count > 0) - Number(a.quality.count > 0) || b.quality.score - a.quality.score)
      || a.modelId.localeCompare(b.modelId))
})
const visibleModels = computed(() => models.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const samples = computed(() => models.value.reduce((sum, model) => sum + (tab.value === 'speed' ? model.speed.count : model.quality.count), 0))
watch([search, bucket, tab], () => { page.value = 1; resetOpen.value = false })
function duration(ms: number | null) {
  return ms === null ? '—' : t('statsSeconds', { seconds: new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(ms / 1000) })
}
function score(value: number) { return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 1 }).format(value * 100) }
async function load() {
  if (loading.value || resetting.value) return
  loading.value = true
  error.value = ''
  try { stats.value = await workspace.client.request<ModelSelectionStats>('/model-selection/stats') }
  catch (cause) { error.value = String(cause) }
  finally { loading.value = false }
}
async function reset() {
  if (resetting.value) return
  const scope = tab.value
  resetting.value = true
  error.value = ''
  try {
    stats.value = await workspace.client.request<ModelSelectionStats>('/model-selection/stats', { method: 'DELETE', body: JSON.stringify({ scope }) })
    resetOpen.value = false
    page.value = 1
    if (scope === 'quality') workspace.conversation.feedback.value = {}
  } catch (cause) { error.value = String(cause) }
  finally { resetting.value = false }
}
onMounted(load)
</script>

<template>
  <section class="model-selection-stats" :aria-label="t('selectionStats')">
    <div class="selection-stats-heading"><h3>{{ t('selectionStats') }}</h3><a-button size="small" :loading="loading" :disabled="resetting" @click="load">{{ t('refresh') }}</a-button></div>
    <p class="selection-stats-hint">{{ t('statsReasoningHint') }}</p>
    <a-tabs v-model:active-key="tab">
      <a-tab-pane key="speed" :tab="t('speedStats')" />
      <a-tab-pane key="quality" :tab="t('qualityStats')" />
    </a-tabs>
    <p class="selection-stats-scope">{{ tab === 'speed' ? t('speedStatsScope', { days: stats?.speedWindowDays ?? 30 }) : t('qualityStatsScope', { count: stats?.qualityWindowSize ?? 200 }) }}</p>
    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <div class="selection-stats-controls">
      <a-input v-model:value="search" :placeholder="t('searchStatsModels')" :aria-label="t('searchStatsModels')" allow-clear />
      <label v-if="tab === 'speed'" for="selection-stats-input-length" class="visually-hidden">{{ t('statsInputLength') }}</label>
      <a-select v-if="tab === 'speed'" id="selection-stats-input-length" v-model:value="bucket" :options="bucketOptions" :aria-label="t('statsInputLength')" />
    </div>
    <div class="selection-stats-summary"><small>{{ t('statsSummary', { models: models.length, samples }) }}</small>
      <confirm-popover :show="resetOpen" :message="t(tab === 'speed' ? 'resetSpeedStatsConfirm' : 'resetQualityStatsConfirm')" :cancel-text="t('cancel')" :confirm-text="resetLabel" :busy="resetting" :error="error" popover-class="selection-stats-reset-popover" @update:show="resetOpen = $event" @confirm="reset">
        <template #trigger><a-button size="small" danger :disabled="!stats || loading || resetting">{{ resetLabel }}</a-button></template>
      </confirm-popover>
    </div>
    <p v-if="!visibleModels.length" class="selection-stats-empty" role="status">{{ loading ? t('statsLoading') : t(search ? 'statsNoMatches' : 'statsNoModels') }}</p>
    <ul v-else class="selection-stats-list" :aria-busy="loading || resetting">
      <li v-for="model in visibleModels" :key="model.modelId" :data-model-id="model.modelId" class="selection-stats-model">
        <div class="selection-stats-model-name"><strong>{{ workspace.modelNameById(model.modelId) }}</strong><small>{{ model.providers.join(' · ') }}</small></div>
        <dl v-if="tab === 'speed'" class="selection-stats-metrics">
          <div><dt>{{ t('statsMedian') }}</dt><dd>{{ duration(model.speed.medianMs) }}</dd></div>
          <div><dt>{{ t('statsP90') }}</dt><dd>{{ duration(model.speed.p90Ms) }}</dd></div>
          <div><dt>{{ t('statsSamples') }}</dt><dd>{{ model.speed.count }}<small v-if="!model.speed.count">{{ t('statsUnmeasured') }}</small><small v-else-if="model.speed.lowConfidence">{{ t('statsFewSamples') }}</small></dd></div>
          <div><dt>{{ t('statsFailures') }}</dt><dd>{{ model.speed.failures }}</dd></div>
        </dl>
        <dl v-else class="selection-stats-metrics">
          <div><dt>{{ t('statsQualityScore') }}</dt><dd>{{ model.quality.count ? score(model.quality.score) : '—' }}</dd></div>
          <div><dt>{{ t('statsUpvotes') }}</dt><dd>{{ model.quality.up }}</dd></div>
          <div><dt>{{ t('statsDownvotes') }}</dt><dd>{{ model.quality.down }}</dd></div>
          <div><dt>{{ t('statsRatings') }}</dt><dd>{{ model.quality.count }}<small v-if="!model.quality.count">{{ t('statsUnrated') }}</small><small v-else-if="model.quality.lowConfidence">{{ t('statsFewSamples') }}</small></dd></div>
        </dl>
      </li>
    </ul>
    <a-pagination v-if="models.length > pageSize" v-model:current="page" size="small" :page-size="pageSize" :total="models.length" :show-size-changer="false" :show-less-items="true" />
  </section>
</template>
