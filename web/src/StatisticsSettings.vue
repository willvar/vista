<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Empty as VEmpty, Modal as VModal, Popover, Select as VSelect } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import { useStatistics } from './composables/useStatistics'
import StatisticsTrend from './StatisticsTrend.vue'
import { useEscapeDismiss } from './composables/useEscape'
import { calendarDate } from './client/statistics'
import { formatDateTime } from './ui/datetime'
import { tokenScale } from './state/tokenScale'

const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const period = ref(30)
const { data, loading, error, refresh } = useStatistics(client, stateSync, period)
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const resetOpen = ref(false)
const resetBusy = ref(false)
const resetError = ref('')
watch(resetOpen, value => emit('modal-open', value), { flush: 'sync' })
onUnmounted(() => emit('modal-open', false))
function confirmReset() {
  resetError.value = ''
  resetOpen.value = true
}
async function reset() {
  if (resetBusy.value) return
  resetBusy.value = true
  resetError.value = ''
  try {
    await client.request('/statistics/reset', { method: 'POST', body: JSON.stringify({ confirm: 'reset-statistics' }) })
    resetOpen.value = false
  } catch (cause) {
    resetError.value = String(cause)
  } finally {
    resetBusy.value = false
  }
}
defineExpose({ headerAction: computed(() => ({ label: 'statisticsReset', run: confirmReset, disabled: resetBusy.value, loading: resetBusy.value })) })
const periodOptions = computed(() => [7,30,90].map(value => ({ value, label: t('statisticsPeriodDays',{count:value}) })))
const number = (value: number) => new Intl.NumberFormat(locale.value).format(value)
const unknownTokens = computed(() => !!data.value?.requests && data.value.requests === data.value.unreported)
const tokenEquivalent = computed(() => tokenScale(data.value?.tokens ?? 0, locale.value))
const tokenDetailsOpen = ref(false)
useEscapeDismiss(tokenDetailsOpen)
watch([period, locale, unknownTokens], () => { tokenDetailsOpen.value = false })
function focusTokenDetails(event: FocusEvent) {
  if ((event.target as HTMLElement).matches(':focus-visible')) tokenDetailsOpen.value = true
}
const dateLabel = (key: string) => new Intl.DateTimeFormat(locale.value,{month:'short',day:'numeric'}).format(calendarDate(key))
const selectedDate = ref('')
const selectedDay = computed(() => data.value?.days.find(day => day.date === selectedDate.value) ?? data.value?.days.at(-1))
watch(period, () => { selectedDate.value = '' })
const hasData = computed(() => !!data.value && (data.value.activeMinutes > 0 || data.value.requests > 0))
const heatSelection = ref(0)
const maxHeat = computed(() => Math.max(0,...(data.value?.hours.flat() ?? [])))
const weekdays = computed(() => Array.from({length:7},(_,i) => new Intl.DateTimeFormat(locale.value,{weekday:'short'}).format(new Date(2026,0,5+i))))
const hourLabel = (hour: number) => `${String(hour).padStart(2,'0')}:00`
const heatValue = (index: number) => data.value?.hours[Math.floor(index/24)]?.[index%24] ?? 0
const heatLabel = (index: number) => `${weekdays.value[Math.floor(index/24)]} ${hourLabel(index%24)}–${hourLabel(index%24+1)} · ${t('statisticsAverageMinutesValue',{count:new Intl.NumberFormat(locale.value,{maximumFractionDigits:1}).format(heatValue(index))})}`
function moveHeat(event: KeyboardEvent,index: number) {
  const delta = {ArrowRight:1,ArrowLeft:-1,ArrowDown:24,ArrowUp:-24}[event.key]
  if (!delta) return
  event.preventDefault()
  const next = Math.max(0,Math.min(167,index+delta))
  heatSelection.value = next
  document.querySelector<HTMLButtonElement>(`[data-statistics-hour="${next}"]`)?.focus()
}
const legacyPeriod = computed(() => data.value && calendarDate(data.value.days[0]!.date).getTime() < Date.parse(data.value.usageSince))
</script>

<template>
  <section class="settings-content statistics-settings" :aria-label="t('statisticsSettings')" :aria-busy="loading">
    <div class="statistics-toolbar">
      <p>{{ t('statisticsIntro') }}</p>
      <label for="statistics-period" class="visually-hidden">{{ t('statisticsPeriod') }}</label>
      <v-select id="statistics-period" v-model:value="period" :options="periodOptions" :aria-label="t('statisticsPeriod')" />
    </div>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error" @click="refresh">{{ t('retry') }}</v-button>
    <p v-if="loading && !data" role="status">{{ t('statisticsLoading') }}</p>
    <template v-if="data">
      <dl class="statistics-totals">
        <div><dt>{{ t('statisticsActiveDays') }}</dt><dd>{{ number(data.activeDays) }}<small>/ {{ period }}</small></dd></div>
        <div><dt>{{ t('statisticsActiveMinutes') }}</dt><dd>{{ number(data.activeMinutes) }}<small>{{ t('statisticsMinuteUnit') }}</small></dd></div>
        <div class="statistics-token-total">
          <dt>{{ t('statisticsTokenTotal') }}</dt>
          <dd v-if="unknownTokens">—</dd>
          <dd v-else>
            <Popover v-model:open="tokenDetailsOpen" placement="bottom" :trigger="['hover', 'click']" :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
              <template #content>
                <section class="statistics-token-details" :aria-label="t('statisticsTokenDetails')">
                  <strong class="statistics-exact-tokens">{{ t('statisticsTokensValue', { count: number(data.tokens) }) }}</strong>
                  <p>{{ t('statisticsScaleHint') }}</p>
                </section>
              </template>
              <button type="button" class="statistics-token-value" :aria-label="`${t('statisticsTokenDetails')}: ${t('statisticsTokensValue', { count: number(data.tokens) })}`" :aria-expanded="tokenDetailsOpen" @focus="focusTokenDetails">
                <i18n-t v-if="tokenEquivalent" :keypath="tokenEquivalent.label" tag="span" :plural="tokenEquivalent.count">
                  <template #count><strong>{{ tokenEquivalent.formattedCount }}</strong></template>
                </i18n-t>
                <strong v-else>0</strong>
              </button>
            </Popover>
          </dd>
        </div>
      </dl>
      <v-empty v-if="!hasData" class="vista-empty-state statistics-empty" :description="t('statisticsEmpty')" role="status" />
      <template v-else>
        <figure class="statistics-figure statistics-trend" :aria-label="t('statisticsTrend')">
          <div class="statistics-chart-heading"><h3>{{ t('statisticsTrend') }}</h3><div class="statistics-legend"><span><i class="token-key"></i>{{ t('statisticsTokenTotal') }}</span><span><i class="activity-key"></i>{{ t('statisticsActiveMinutes') }}</span></div></div>
          <StatisticsTrend :days="data.days" @select="selectedDate = $event" />
          <figcaption class="statistics-readout" aria-live="polite"><strong>{{ selectedDay ? dateLabel(selectedDay.date) : '' }}</strong><span>{{ t('statisticsTokensValue',{count:selectedDay?.requests && selectedDay.requests===selectedDay.unreported ? '—' : number(selectedDay?.tokens ?? 0)}) }}</span><span>{{ t('statisticsMinutesValue',{count:number(selectedDay?.activeMinutes ?? 0)}) }}</span><small v-if="selectedDay?.unreported">{{ t('statisticsUnreported',{count:number(selectedDay.unreported)}) }}</small></figcaption>
        </figure>
        <figure class="statistics-figure statistics-heatmap" :aria-label="t('statisticsHeatmap')">
          <div class="statistics-chart-heading"><h3>{{ t('statisticsHeatmap') }}</h3><div class="statistics-heat-legend" aria-hidden="true"><span>{{ t('statisticsLess') }}</span><i v-for="level in [0,1,2,3,4]" :key="level" :data-level="level"></i><span>{{ t('statisticsMore') }}</span></div></div>
          <div class="statistics-heat-scroll">
            <div class="statistics-heat-grid" role="group" :aria-label="t('statisticsChartHint')">
              <span></span><span v-for="hour in 24" :key="hour" class="statistics-hour-label" aria-hidden="true">{{ (hour-1)%3===0 ? String(hour-1).padStart(2,'0') : '' }}</span>
              <template v-for="(weekday,day) in weekdays" :key="day"><span class="statistics-weekday" aria-hidden="true">{{ weekday }}</span><button v-for="hour in 24" :key="hour" type="button" class="statistics-heat-cell" :data-statistics-hour="day*24+hour-1" :data-level="heatValue(day*24+hour-1) ? Math.min(4,1+Math.floor(heatValue(day*24+hour-1)/maxHeat*3)) : 0" :aria-label="heatLabel(day*24+hour-1)" :title="heatLabel(day*24+hour-1)" :tabindex="heatSelection===day*24+hour-1?0:-1" @pointerenter="heatSelection=day*24+hour-1" @focus="heatSelection=day*24+hour-1" @click="heatSelection=day*24+hour-1" @keydown="moveHeat($event,day*24+hour-1)"></button></template>
            </div>
          </div>
          <figcaption class="statistics-readout" aria-live="polite">{{ heatLabel(heatSelection) }}</figcaption>
        </figure>
      </template>
      <div class="statistics-notes">
        <p>{{ t('statisticsActivityHint') }}</p>
        <p>{{ t('statisticsUsageHint') }}<span v-if="data.unreported"> {{ t('statisticsUnreported',{count:number(data.unreported)}) }}</span></p>
        <p>{{ t('statisticsRecordedSince',{date:formatDateTime(data.activitySince,locale),days:data.retentionDays}) }}<span v-if="legacyPeriod"> {{ t('statisticsHistoryHint') }}</span></p>
        <p class="statistics-sampled">{{ data.timezone }} · {{ t('statisticsSampledAt',{time:formatDateTime(data.sampledAt,locale)}) }}</p>
      </div>
    </template>
  </section>
  <v-modal :open="resetOpen" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))"
    class="statistics-reset-modal" :title="t('statisticsResetTitle')" :ok-text="t('reset')" :cancel-text="t('cancel')"
    :confirm-loading="resetBusy" :ok-button-props="{ danger: true }" :cancel-button-props="{ disabled: resetBusy }"
    :closable="!resetBusy" :keyboard="!resetBusy" :mask-closable="!resetBusy"
    @ok="reset" @cancel="!resetBusy && (resetOpen = false)">
    <p>{{ t('statisticsResetWarning') }}</p>
    <v-alert v-if="resetError" type="error" role="alert" :message="resetError" />
  </v-modal>
</template>

<style scoped>
.statistics-settings { display:flex; flex-direction:column; gap:1.5rem; }
.statistics-settings > * { flex-shrink:0; }
.statistics-toolbar { display:flex; align-items:center; justify-content:space-between; gap:1rem; }
.statistics-toolbar p { margin:0; color:var(--text-secondary); flex:1; }
.statistics-toolbar :deep(.ui-select) { width:10rem; min-width:9rem; max-width:100%; flex:0 0 auto; }
.statistics-totals { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:1rem; margin:0; }
.statistics-totals > div { min-width:0; padding:1rem; border-radius:var(--radius-lg,12px); background:var(--surface-subtle); }
.statistics-totals dt { color:var(--text-secondary); font-size:.875rem; }
.statistics-totals dd { margin:.5rem 0 0; font-size:1.65rem; font-weight:600; font-variant-numeric:tabular-nums; line-height:1.25; }
.statistics-totals small { margin-left:.4rem; color:var(--muted); font-size:.75rem; font-weight:400; }
.statistics-token-value { display:block; width:100%; border:0; padding:0; background:transparent; color:inherit; font:inherit; font-size:.9rem; font-weight:400; line-height:1.5; text-align:start; word-break:keep-all; overflow-wrap:anywhere; cursor:pointer; border-radius:4px; }
.statistics-token-value strong { font-size:1.65rem; font-weight:600; }
.statistics-token-value:hover { color:var(--accent); }
.statistics-token-value:focus-visible { outline:var(--focus-outline, 2px solid var(--accent)); outline-offset:4px; }
.statistics-token-details { display:grid; gap:.5rem; max-width:24rem; color:var(--text-secondary); line-height:1.6; overflow-wrap:anywhere; }
.statistics-token-details p { margin:0; }
.statistics-exact-tokens { color:var(--text); font-size:1rem; font-weight:600; font-variant-numeric:tabular-nums; }
.statistics-figure { margin:0; min-width:0; }
.statistics-trend { --statistics-activity:#a879de; }
.statistics-chart-heading { display:flex; align-items:center; justify-content:space-between; gap:1rem; margin-bottom:1rem; }
.statistics-chart-heading h3 { font-size:1rem; margin:0; font-weight:550; }
.statistics-legend,.statistics-heat-legend { display:flex; align-items:center; gap:.65rem; font-size:.75rem; color:var(--text-secondary); }
.statistics-legend span { display:inline-flex; align-items:center; gap:.35rem; }
.statistics-legend i { width:.7rem; height:.5rem; border-radius:2px; background:var(--accent); }
.statistics-legend .activity-key { height:2px; background:var(--statistics-activity); }
.statistics-readout { display:flex; align-items:center; flex-wrap:wrap; gap:.35rem 1rem; min-height:2.5rem; margin-top:.6rem; padding:.5rem .75rem; background:var(--surface-subtle); border-radius:8px; color:var(--text-secondary); font-size:.8rem; font-variant-numeric:tabular-nums; }
.statistics-readout strong { color:var(--text); font-weight:500; }
.statistics-readout small { flex-basis:100%; color:var(--muted); }
.statistics-heat-scroll { overflow-x:auto; overscroll-behavior-x:contain; padding:2px 2px .4rem; }
.statistics-heat-grid { display:grid; grid-template-columns:minmax(2.75rem,max-content) repeat(24,minmax(15px,1fr)); grid-template-rows:1rem repeat(7,1fr); gap:4px; min-width:520px; }
.statistics-hour-label,.statistics-weekday { font-size:.7rem; color:var(--muted); }
.statistics-hour-label { text-align:center; }
.statistics-weekday { align-self:center; padding-right:.5rem; }
.statistics-heat-cell,.statistics-heat-legend i { background:var(--surface-subtle); border:0; border-radius:3px; }
.statistics-heat-cell { aspect-ratio:1; padding:0; cursor:crosshair; min-width:0; transition:filter .15s ease; }
.statistics-heat-cell:hover { filter:brightness(1.15); }
.statistics-heat-cell:focus-visible { outline:var(--focus-outline, 2px solid var(--accent)); outline-offset:1px; }
.statistics-settings [data-level="1"] { background:color-mix(in srgb,var(--accent) 24%,var(--surface-subtle)); }
.statistics-settings [data-level="2"] { background:color-mix(in srgb,var(--accent) 46%,var(--surface-subtle)); }
.statistics-settings [data-level="3"] { background:color-mix(in srgb,var(--accent) 72%,var(--surface-subtle)); }
.statistics-settings [data-level="4"] { background:var(--accent); }
.statistics-heat-legend { gap:.25rem; }
.statistics-heat-legend i { width:.65rem; height:.65rem; }
.statistics-heat-legend span:first-child { margin-right:.2rem; }
.statistics-heat-legend span:last-child { margin-left:.2rem; }
.statistics-notes { display:grid; gap:.5rem; color:var(--text-secondary); font-size:.8rem; line-height:1.65; }
.statistics-notes p { margin:0; }
.statistics-sampled { color:var(--muted); }
@media (max-width:600px) {
  .statistics-toolbar { align-items:flex-start; flex-direction:column; gap:.75rem; }
  .statistics-totals { grid-template-columns:repeat(2,minmax(0,1fr)); gap:.5rem; }
  .statistics-totals > div { padding:.75rem .5rem; }
  .statistics-token-total { grid-column:1/-1; }
  .statistics-totals dd { font-size:1.25rem; }
  .statistics-token-value strong { font-size:1.25rem; }
  .statistics-totals dt { font-size:.75rem; }
  .statistics-totals small { display:block; margin:.25rem 0 0; }
  .statistics-chart-heading { flex-wrap:wrap; gap:.5rem; }
}
@media (prefers-reduced-motion:reduce) { .statistics-heat-cell { transition:none; } }
</style>
