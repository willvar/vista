<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Empty as VEmpty, Select as VSelect } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import { useStateQuery } from './composables/useStateQuery'

type Group = { model: string; proxyId: string; route: string; requestedTier: string; actualTier: string; outcome: string; requests: number;
  firstTokenMs: number | null; firstTokenSamples: number; connectMs: number | null; connectSamples: number; tlsMs: number | null; tlsSamples: number; outputTps: number | null; outputSamples: number }
const props = defineProps<{ modelKey: string; days: number; profiles: readonly { id: string; name: string }[] }>()
const { t, locale } = useI18n()
const { client, stateSync } = useWorkspace()
const { data, error, loading, refresh } = useStateQuery<{ version?: string; groups: Group[]; truncated: boolean }>(client, stateSync,
  `/connection-statistics?key=${encodeURIComponent(props.modelKey)}&days=${props.days}`, ['connection-statistics'], 60_000)
const model = ref(''), tier = ref(''), route = ref('')
const all = computed(() => data.value?.groups || [])
const tierLabel = (value: string) => value ? t(`processingTier_${value}`) : t('providerTierDefault')
const routeLabel = (group: Group) => group.proxyId ? `${props.profiles.find(profile => profile.id === group.proxyId)?.name || t('networkProxy')} · ${group.route.slice(0, 6)}` : t('networkDirect')
const options = (values: Map<string,string>) => [{ value: '', label: t('connectionUsageAll') }, ...Array.from(values, ([value, label]) => ({ value, label }))]
const modelOptions = computed(() => options(new Map(all.value.map(row => [row.model, row.model]))))
const tierOptions = computed(() => options(new Map(all.value.map(row => [row.requestedTier || 'inherit', tierLabel(row.requestedTier)]))))
const routeOptions = computed(() => options(new Map(all.value.map(row => [row.route, routeLabel(row)]))))
const rows = computed(() => all.value.filter(row => (!model.value || row.model === model.value) && (!route.value || row.route === route.value)
  && (!tier.value || (row.requestedTier || 'inherit') === tier.value)))
const number = (value: number, digits = 0) => new Intl.NumberFormat(locale.value, { maximumFractionDigits: digits }).format(value)
const outcomes = computed(() => {
  const totals: Record<string, number> = {}
  for (const row of rows.value) totals[row.outcome] = (totals[row.outcome] || 0) + row.requests
  return totals
})
const total = computed(() => Object.values(outcomes.value).reduce((a,b) => a+b,0))
const completed = computed(() => total.value - (outcomes.value.cancelled || 0))
const metrics = computed(() => ([['usageTTFT','firstTokenMs','firstTokenSamples'],['usageTPS','outputTps','outputSamples'],['connectionUsageConnect','connectMs','connectSamples'],['connectionUsageTLS','tlsMs','tlsSamples']] as const).map(([label,key,count]) => {
  let sum = 0, samples = 0
  for (const row of rows.value) {
    if (row[key] != null) { sum += row[key]! * row[count]; samples += row[count] }
  }
  return { label, samples, value: samples ? number(sum/samples, key === 'outputTps' ? 1 : 0) + (key === 'outputTps' ? ' tok/s' : ' ms') : '—' }
}))
</script>

<template>
  <section class="connection-usage" :aria-busy="loading">
    <p v-if="error" role="alert">{{ error }} <v-button @click="refresh">{{ t('retry') }}</v-button></p>
    <p v-else-if="loading && !data" role="status">{{ t('statisticsLoading') }}</p>
    <v-empty v-else-if="!all.length" :description="t('connectionUsageEmpty')" />
    <template v-else>
      <div class="connection-usage-filters">
        <div><label for="connection-usage-model">{{ t('model') }}</label><v-select id="connection-usage-model" v-model:value="model" :options="modelOptions" :title="model || undefined" /></div>
        <div><label for="connection-usage-tier">{{ t('connectionUsageRequested') }}</label><v-select id="connection-usage-tier" v-model:value="tier" :options="tierOptions" /></div>
        <div><label for="connection-usage-route">{{ t('connectionUsageRoute') }}</label><v-select id="connection-usage-route" v-model:value="route" :options="routeOptions" /></div>
      </div>
      <template v-if="rows.length">
        <dl class="connection-usage-metrics">
          <div><dt>{{ t('connectionUsageRequests') }}</dt><dd>{{ number(total) }}</dd></div>
          <div><dt>{{ t('connectionUsageSuccess') }}</dt><dd>{{ completed ? number((outcomes.succeeded || 0) / completed * 100, 1) + '%' : '—' }}</dd></div>
          <div v-for="item in metrics" :key="item.label"><dt>{{ t(item.label) }}</dt><dd>{{ item.value }}</dd><dd class="connection-usage-samples">{{ t('connectionUsageSamples', { count: number(item.samples) }) }}</dd></div>
        </dl>
        <ul class="connection-usage-outcomes">
          <li v-for="(count, outcome) in outcomes" :key="outcome">{{ t(`connectionUsageOutcome_${outcome}`) }} · {{ number(count) }}</li>
        </ul>
        <div class="connection-usage-table" tabindex="0" role="region" :aria-label="t('usageDetails')">
          <table>
            <thead><tr>
              <th scope="col" class="connection-usage-model">{{ t('model') }}</th>
              <th scope="col">{{ t('connectionUsageRoute') }}</th>
              <th scope="col">{{ t('connectionUsageRequested') }}</th>
              <th scope="col">{{ t('connectionUsageActual') }}</th>
              <th scope="col">{{ t('usageStatus') }}</th>
              <th scope="col">{{ t('connectionUsageRequests') }}</th>
            </tr></thead>
            <tbody><tr v-for="row in rows" :key="JSON.stringify([row.model, row.route, row.requestedTier, row.actualTier, row.outcome])">
              <td>{{ row.model }}</td><td>{{ routeLabel(row) }}</td><td>{{ tierLabel(row.requestedTier) }}</td>
              <td>{{ row.actualTier ? tierLabel(row.actualTier) : t('processingTierUnconfirmed') }}</td>
              <td>{{ t(`connectionUsageOutcome_${row.outcome}`) }}</td><td>{{ number(row.requests) }}</td>
            </tr></tbody>
          </table>
        </div>
      </template>
      <v-empty v-else :description="t('traceNoMatches')" />
      <p v-if="data?.truncated" class="connection-usage-note">{{ t('connectionUsageTruncated') }}</p>
      <p class="connection-usage-note">{{ t('connectionUsageHint') }}</p>
    </template>
  </section>
</template>

<style scoped>
.connection-usage { display: grid; gap: 12px; min-width: 0; }
.connection-usage p { margin: 0; overflow-wrap: anywhere; }
.connection-usage-note { color: var(--muted); font-size: var(--font-size-small); }
.connection-usage-filters, .connection-usage-metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
}
.connection-usage-filters { align-items: end; }
.connection-usage-filters > div { display: grid; gap: 4px; min-width: 0; }
.connection-usage-filters label, .connection-usage-metrics dt { color: var(--muted); font-size: var(--font-size-small); }
.connection-usage-filters .ui-select { width: 100%; }
.connection-usage-metrics > div {
  min-width: 0;
  padding: 8px 12px;
  background: var(--surface-subtle);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  overflow-wrap: anywhere;
}
.connection-usage-metrics dd { margin: 2px 0 0; font-weight: 600; font-variant-numeric: tabular-nums; }
.connection-usage-metrics .connection-usage-samples { color: var(--muted); font-size: var(--font-size-small); font-weight: 400; }
.connection-usage-outcomes { display: flex; flex-wrap: wrap; gap: 4px 12px; list-style: none; padding: 0; margin: 0; color: var(--text-secondary); font-size: var(--font-size-small); }
.connection-usage-table { overflow: auto; max-height: min(18rem, 52dvh); border: 1px solid var(--border); border-radius: 8px; }
.connection-usage-table table { table-layout: fixed; border-spacing: 0; width: 100%; min-width: 37rem; font-size: var(--font-size-small); }
.connection-usage-table th, .connection-usage-table td { text-align: start; padding: 6px 8px; vertical-align: top; overflow-wrap: anywhere; }
.connection-usage-table th { position: sticky; top: 0; background: var(--surface-subtle); border-bottom: 1px solid var(--border); }
.connection-usage-table tbody tr + tr td { border-top: 1px solid var(--border-subtle); }
.connection-usage-table .connection-usage-model { width: 32%; }
.connection-usage-table th:last-child, .connection-usage-table td:last-child { text-align: end; width: 10%; font-variant-numeric: tabular-nums; }
@media (max-width: 560px) {
  .connection-usage-filters, .connection-usage-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .connection-usage-filters > :first-child { grid-column: 1 / -1; }
}
</style>
