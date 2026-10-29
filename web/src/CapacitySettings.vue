<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, InputNumber as AInputNumber, Select as ASelect } from 'antdv-next'
import type { VistaClient } from './client/vista'
import type { MemoryConfig, ServerStatus } from './client/types'

const props = defineProps<{ client: VistaClient; status: ServerStatus | null }>()
const { t } = useI18n({ useScope: 'global' })
const presets: MemoryConfig[] = [
  { budgetMiB: 512, workerMiB: 384, concurrentRuns: 1 },
  { budgetMiB: 1024, workerMiB: 768, concurrentRuns: 2 },
  { budgetMiB: 2048, workerMiB: 1024, concurrentRuns: 4 },
  { budgetMiB: 4096, workerMiB: 2048, concurrentRuns: 8 },
  { budgetMiB: 8192, workerMiB: 4096, concurrentRuns: 20 },
]
const saved = ref<MemoryConfig>()
const draft = ref<MemoryConfig>({ ...presets[2]! })
const preset = ref<number | string>(2048)
const busy = ref(false)
const error = ref('')
const equal = (a: MemoryConfig, b: MemoryConfig) => a.budgetMiB === b.budgetMiB && a.workerMiB === b.workerMiB && a.concurrentRuns === b.concurrentRuns
const dirty = computed(() => !!saved.value && !equal(saved.value, draft.value))
const valid = computed(() => Object.values(draft.value).every(Number.isSafeInteger) && draft.value.budgetMiB >= 256 && draft.value.budgetMiB <= 1048576 && draft.value.workerMiB >= 128 && draft.value.workerMiB <= draft.value.budgetMiB && draft.value.concurrentRuns >= 1 && draft.value.concurrentRuns <= 128)
const options = computed(() => [...presets.map(item => ({ label: item.budgetMiB < 1024 ? `${item.budgetMiB} MiB` : `${item.budgetMiB / 1024} GiB`, value: item.budgetMiB })), { label: t('custom'), value: 'custom' }])
function receive(config: MemoryConfig) {
  if (!dirty.value) {
    draft.value = { ...config }
    preset.value = presets.find(item => equal(item, config))?.budgetMiB || 'custom'
  }
  saved.value = { ...config }
}
watch(() => props.status?.capacity?.config, value => { if (value) receive(value) }, { immediate: true })
function choose(value: number | string) {
  preset.value = value
  const config = presets.find(item => item.budgetMiB === value)
  if (config) draft.value = { ...config }
}
async function save() {
  if (!valid.value || !dirty.value || busy.value) return
  busy.value = true; error.value = ''
  try {
    const config = await props.client.request<MemoryConfig>('/memory-config', { method: 'PUT', body: JSON.stringify(draft.value) })
    saved.value = { ...config }; receive(config)
  } catch (cause) { error.value = String(cause) }
  finally { busy.value = false }
}
</script>
<template>
  <section class="memory-settings" aria-labelledby="memory-settings-heading">
    <h3 id="memory-settings-heading">{{ t('memorySettings') }}</h3>
    <p>{{ t('memorySettingsHint') }}</p>
    <div v-if="status?.capacity" class="memory-usage" role="status">{{ t('memoryUsage', { used: Math.ceil(status.capacity.residentBytes / 1048576), limit: status.capacity.config.budgetMiB }) }}</div>
    <form @submit.prevent="save">
      <label for="memory-preset">{{ t('memoryBudget') }}</label>
      <a-select id="memory-preset" :value="preset" :options="options" :disabled="busy || !saved" :aria-label="t('memoryBudget')" @update:value="choose($event as number | string)" />
      <div v-if="preset === 'custom'" class="memory-custom">
        <label>{{ t('memoryBudget') }} (MiB)<a-input-number v-model:value="draft.budgetMiB" :min="256" :max="1048576" :precision="0" :disabled="busy" :aria-label="t('memoryBudget')" /></label>
        <label>{{ t('memoryWorkerLimit') }} (MiB)<a-input-number v-model:value="draft.workerMiB" :min="128" :max="draft.budgetMiB" :precision="0" :disabled="busy" :aria-label="t('memoryWorkerLimit')" /></label>
        <label>{{ t('memoryConcurrentRuns') }}<a-input-number v-model:value="draft.concurrentRuns" :min="1" :max="128" :precision="0" :disabled="busy" :aria-label="t('memoryConcurrentRuns')" /></label>
      </div>
      <p v-else class="memory-limits">{{ t('memoryWorkerLimit') }}: {{ draft.workerMiB }} MiB · {{ t('memoryConcurrentRuns') }}: {{ draft.concurrentRuns }}</p>
      <a-alert v-if="error" type="error" :message="error" role="alert" />
      <a-button html-type="submit" :disabled="!dirty || !valid" :loading="busy">{{ t('saveChanges') }}</a-button>
    </form>
  </section>
</template>
<style scoped>
.memory-settings { padding: 1rem 0; border-bottom: 1px solid var(--border); }
.memory-settings h3 { margin: 0; font-size: 1rem; }
.memory-settings p { color: var(--muted); line-height: 1.6; margin: .5rem 0 .75rem; }
.memory-settings form, .memory-custom, .memory-custom label { display: flex; flex-direction: column; gap: .5rem; }
.memory-usage { margin: .75rem 0; font-variant-numeric: tabular-nums; }
.memory-settings .ant-input-number { width: 100%; }
.memory-settings .ant-btn { align-self: flex-start; }
</style>
