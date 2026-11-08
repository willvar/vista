<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { JsonSchema, ControlElement, VerticalLayout } from '@jsonforms/core'
import { Alert as VAlert, Button as VButton } from './ui'
import SchemaForm from './SchemaForm.vue'
import type { FormIssue } from './schema/validation'
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
const preset = ref<number | string>(2048)
const saved = ref<MemoryConfig>()
const draft = ref<MemoryConfig>({ budgetMiB: 2048, workerMiB: 1024, concurrentRuns: 4 })
const busy = ref(false), error = ref(''), editorValid = ref(false), editorDirty = ref(false), editorEpoch = ref(0)
const equal = (a: MemoryConfig, b: MemoryConfig) => a.budgetMiB === b.budgetMiB && a.workerMiB === b.workerMiB && a.concurrentRuns === b.concurrentRuns
const presetFor = (value: MemoryConfig) => presets.find(item => equal(item, value))?.budgetMiB ?? 'custom'
const dirty = computed(() => !!saved.value && !equal(saved.value, draft.value))
const schema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, required: ['budgetMiB', 'workerMiB', 'concurrentRuns'], properties: {
  budgetMiB: { type: 'integer', title: `${t('memoryBudget')} (MiB)`, minimum: 256, maximum: 1048576 },
  workerMiB: { type: 'integer', title: `${t('memoryWorkerLimit')} (MiB)`, minimum: 128, maximum: 1048576 },
  concurrentRuns: { type: 'integer', title: t('memoryConcurrentRuns'), minimum: 1, maximum: 128 },
} }))
const uischema: VerticalLayout = { type: 'VerticalLayout', elements: ['budgetMiB', 'workerMiB', 'concurrentRuns'].map(field => ({ type: 'Control', scope: `#/properties/${field}` })) }
function validLimits(value: Record<string, unknown>): FormIssue[] { return typeof value.workerMiB === 'number' && typeof value.budgetMiB === 'number' && value.workerMiB > value.budgetMiB ? [{ path: '/workerMiB', message: t('configWorkerBudget') }] : [] }
const document = computed<Record<string, unknown>>({
  get: () => ({ ...draft.value }),
  set: value => { draft.value = { budgetMiB: value.budgetMiB as number, workerMiB: value.workerMiB as number, concurrentRuns: value.concurrentRuns as number }; preset.value = presetFor(draft.value) },
})
function receive(config: MemoryConfig, reset = false) {
  const editing = dirty.value || editorDirty.value || !!saved.value && preset.value !== presetFor(saved.value)
  if (reset || !editing) { draft.value = { ...config }; preset.value = presetFor(config); if (reset) editorEpoch.value++ }
  saved.value = { ...config }
}
watch(() => props.status?.capacity?.config, value => { if (value) receive(value) }, { immediate: true })
const presetDocument = computed<Record<string, unknown>>({
  get: () => ({ preset: preset.value }),
  set: value => {
    preset.value = value.preset as number | string
    const selected = presets.find(item => item.budgetMiB === preset.value)
    if (selected) { draft.value = { ...selected }; editorEpoch.value++ }
  },
})
const presetSchema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, properties: {
  preset: { type: ['integer', 'string'], title: t('memoryPreset'), enum: [...presets.map(item => item.budgetMiB), 'custom'] },
} }))
const presetUISchema = computed<ControlElement>(() => ({ type: 'Control', scope: '#/properties/preset', options: {
  enumLabels: Object.fromEntries([...presets.map(item => [item.budgetMiB, item.budgetMiB < 1024 ? `${item.budgetMiB} MiB` : `${item.budgetMiB / 1024} GiB`]), ['custom', t('custom')]]),
} }))
async function save() {
  if (!editorValid.value || !dirty.value || busy.value) return
  busy.value = true; error.value = ''
  try { receive(await props.client.request<MemoryConfig>('/memory-config', { method: 'PUT', body: JSON.stringify(draft.value) }), true) }
  catch (cause) { error.value = String(cause) }
  finally { busy.value = false }
}
</script>
<template>
  <section class="memory-settings" aria-labelledby="memory-settings-heading">
    <h3 id="memory-settings-heading">{{ t('memorySettings') }}</h3>
    <p>{{ t('memorySettingsHint') }}</p>
    <div v-if="status?.capacity" class="memory-usage" role="status">{{ t('memoryUsage', { used: Math.ceil(status.capacity.residentBytes / 1048576), limit: status.capacity.config.budgetMiB }) }}</div>
    <form @submit.prevent="save">
      <SchemaForm v-if="saved" id="capacity-preset" v-model="presetDocument" :schema="presetSchema" :uischema="presetUISchema" :disabled="busy" :reset-key="editorEpoch" />
      <SchemaForm v-if="saved" id="capacity-config" v-model="document" :schema="schema" :uischema="uischema" :validate="validLimits" :disabled="busy" :reset-key="editorEpoch"
        @validity="editorValid = $event" @dirty="editorDirty = $event" />
      <v-alert v-if="error" type="error" :message="error" role="alert" />
      <v-button type="submit" :disabled="!dirty || !editorValid" :loading="busy">{{ t('saveChanges') }}</v-button>
    </form>
  </section>
</template>
<style scoped>
.memory-settings { padding: 1rem 0; border-bottom: 1px solid var(--border); }
.memory-settings h3 { margin: 0; font-size: 1rem; }
.memory-settings p { color: var(--muted); line-height: 1.6; margin: .5rem 0 .75rem; }
.memory-settings form { display: flex; flex-direction: column; gap: .5rem; }
.memory-usage { margin: .75rem 0; font-variant-numeric: tabular-nums; }
.memory-settings form > .ui-btn { align-self: flex-start; }
</style>
