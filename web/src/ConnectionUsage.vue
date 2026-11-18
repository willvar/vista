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
    <p>{{ t('connectionUsageHint') }}</p>
    <p v-if="error" role="alert">{{ error }} <v-button @click="refresh">{{ t('retry') }}</v-button></p>
    <p v-else-if="loading && !data" role="status">{{ t('statisticsLoading') }}</p>
    <v-empty v-else-if="!all.length" :description="t('connectionUsageEmpty')" />
    <template v-else>
      <p v-if="data?.truncated">{{ t('connectionUsageTruncated') }}</p>
      <div class="connection-usage-filters">
        <label>{{ t('model') }}<v-select v-model:value="model" :options="modelOptions" :aria-label="t('model')" /></label>
        <label>{{ t('processingTierLabel') }}<v-select v-model:value="tier" :options="tierOptions" :aria-label="t('processingTierLabel')" /></label>
        <label>{{ t('connectionUsageRoute') }}<v-select v-model:value="route" :options="routeOptions" :aria-label="t('connectionUsageRoute')" /></label>
      </div>
      <dl class="connection-usage-metrics">
        <div><dt>{{ t('connectionUsageRequests') }}</dt><dd>{{ number(total) }}</dd></div>
        <div><dt>{{ t('connectionUsageSuccess') }}</dt><dd>{{ completed ? number((outcomes.succeeded || 0) / completed * 100, 1) + '%' : '—' }}</dd></div>
        <div v-for="item in metrics" :key="item.label"><dt>{{ t(item.label) }}</dt><dd>{{ item.value }} <small>{{ t('connectionUsageSamples', { count: number(item.samples) }) }}</small></dd></div>
      </dl>
      <ul v-if="rows.length" class="connection-usage-outcomes">
        <li v-for="(count, outcome) in outcomes" :key="outcome">{{ t(`connectionUsageOutcome_${outcome}`) }} · {{ number(count) }}</li>
      </ul>
      <div v-if="rows.length" class="connection-usage-table" tabindex="0" :aria-label="t('usageDetails')">
        <table><thead><tr><th>{{ t('model') }}</th><th>{{ t('connectionUsageRoute') }}</th><th>{{ t('connectionUsageRequested') }}</th><th>{{ t('connectionUsageActual') }}</th><th>{{ t('usageStatus') }}</th><th>{{ t('connectionUsageRequests') }}</th></tr></thead>
          <tbody><tr v-for="row in rows" :key="JSON.stringify([row.model,row.route,row.requestedTier,row.actualTier,row.outcome])">
            <td>{{ row.model }}</td><td>{{ routeLabel(row) }}</td><td>{{ tierLabel(row.requestedTier) }}</td><td>{{ row.actualTier ? tierLabel(row.actualTier) : t('processingTierUnconfirmed') }}</td><td>{{ t(`connectionUsageOutcome_${row.outcome}`) }}</td><td>{{ number(row.requests) }}</td>
          </tr></tbody>
        </table>
      </div>
    </template>
  </section>
</template>

<style scoped>
.connection-usage-filters,.connection-usage-metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,10rem),1fr)); gap:.65rem; }
.connection-usage-filters label { display:grid; gap:.25rem; min-width:0; }
.connection-usage-metrics { margin:1rem 0; }
.connection-usage-metrics dd { margin:.2rem 0 0; font-variant-numeric:tabular-nums; }
.connection-usage-outcomes { display:flex; flex-wrap:wrap; list-style:none; padding:0; gap:.5rem 1rem; }
.connection-usage-table { overflow:auto; max-height:18rem; }
.connection-usage-table table { border-collapse:collapse; min-width:100%; text-align:start; }
.connection-usage-table th,.connection-usage-table td { text-align:start; padding:.45rem; }
</style>
