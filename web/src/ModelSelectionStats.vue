<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Input as VInput, Pagination as VPagination, Select as VSelect, Tabs as VTabs } from './ui'
import type { ModelSelectionStats } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import ConfirmPopover from './ConfirmPopover.vue'
import { useStateQuery } from './composables/useStateQuery'

const { t, locale } = useI18n({ useScope: 'global' })
const workspace = useWorkspace()
const resource = useStateQuery<ModelSelectionStats>(workspace.client, workspace.stateSync, '/model-selection/stats', ['selection-stats', 'models'], 60 * 60_000)
const { data: stats, loading, error } = resource
const resetting = ref(false)
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
async function reset() {
  if (resetting.value) return
  const scope = tab.value
  resetting.value = true
  resource.cancel()
  error.value = ''
  try {
    resource.apply(await workspace.client.request<ModelSelectionStats>('/model-selection/stats', { method: 'DELETE', body: JSON.stringify({ scope }) }))
    resetOpen.value = false
    page.value = 1
    if (scope === 'quality') workspace.conversation.feedback.value = {}
  } catch (cause) { error.value = String(cause) }
  finally { resetting.value = false }
}
</script>

<template>
  <section class="model-selection-stats" :aria-label="t('selectionStats')">
    <div class="selection-stats-heading"><h3>{{ t('selectionStats') }}</h3></div>
    <p class="selection-stats-hint">{{ t('statsReasoningHint') }}</p>
    <v-tabs v-model:active-key="tab" :items="[{ value: 'speed', label: t('speedStats') }, { value: 'quality', label: t('qualityStats') }]">
    <p class="selection-stats-scope">{{ tab === 'speed' ? t('speedStatsScope', { days: stats?.speedWindowDays ?? 30 }) : t('qualityStatsScope', { count: stats?.qualityWindowSize ?? 200 }) }}</p>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error" :disabled="loading || resetting" @click="resource.refresh()">{{ t('retry') }}</v-button>
    <div class="selection-stats-controls">
      <v-input v-model:value="search" :placeholder="t('searchStatsModels')" :aria-label="t('searchStatsModels')" allow-clear />
      <label v-if="tab === 'speed'" for="selection-stats-input-length" class="visually-hidden">{{ t('statsInputLength') }}</label>
      <v-select v-if="tab === 'speed'" id="selection-stats-input-length" v-model:value="bucket" :options="bucketOptions" :aria-label="t('statsInputLength')" />
    </div>
    <div class="selection-stats-summary"><small>{{ t('statsSummary', { models: models.length, samples }) }}</small>
      <confirm-popover :show="resetOpen" :message="t(tab === 'speed' ? 'resetSpeedStatsConfirm' : 'resetQualityStatsConfirm')" :cancel-text="t('cancel')" :confirm-text="resetLabel" :busy="resetting" :error="error" popover-class="selection-stats-reset-popover" @update:show="resetOpen = $event" @confirm="reset">
        <template #trigger><v-button danger :disabled="!stats || loading || resetting">{{ resetLabel }}</v-button></template>
      </confirm-popover>
    </div>
    <p v-if="loading && !visibleModels.length" role="status">{{ t('statsLoading') }}</p>
    <v-empty v-else-if="!visibleModels.length && !error" class="vista-empty-state selection-stats-empty" :description="t(search ? 'statsNoMatches' : 'statsNoModels')" role="status" />
    <ul v-if="visibleModels.length" class="selection-stats-list" :aria-busy="loading || resetting">
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
    <v-pagination v-if="models.length > pageSize" v-model:current="page" :page-size="pageSize" :total="models.length" :show-size-changer="false" :show-less-items="true" />
    </v-tabs>
  </section>
</template>
