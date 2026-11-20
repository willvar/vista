<script setup lang="ts">
import { computed, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Modal as VModal, Alert as VAlert, Select as VSelect } from './ui'
import type { VistaClient, createWorkspaceSync, GenerationKind, GenerationOption, ModelSettings } from '@vista/client'
import { displayModelName, modelPickerOptions } from './composables/usePresentation'

const props = defineProps<{ settings: ModelSettings; client: VistaClient; stateSync: ReturnType<typeof createWorkspaceSync>; disabled?: boolean }>()
const emit = defineEmits<{ saved: [ModelSettings]; 'workflow-open': [boolean] }>()
const { t, locale } = useI18n()
const kinds = ['image', 'video', 'decision'] as const
const labels = { image: 'generationImage', video: 'generationVideo', decision: 'generationDecision' } as const
const candidates = ref<GenerationOption[]>()
const candidateError = ref('')
const saveError = ref('')
const busy = ref(false)
const workflowChoice = ref<GenerationOption>()
const revision = ref('')
const workflowName = ref('')
const workflow = ref<{ workflow?: unknown; promptNode?: string; outputNode?: string; imageNode?: string; cloud?: boolean }>({})
const conflict = computed(() => !!workflowChoice.value && props.settings.settingsRevision !== revision.value)
const saved = (kind: GenerationKind) => props.settings.generation?.find(item => item.kind === kind)
const valueKey = (connection: string | undefined, model: string) => JSON.stringify([connection || '', model])
const byKind = computed(() => Object.fromEntries(kinds.map(kind => {
  const rows = (candidates.value || []).filter(row => row.kind === kind)
  const options = modelPickerOptions(rows.map(row => ({ key: valueKey(row.connection, row.model), id: row.model, modelName: row.name, connectionId: row.connection, name: row.connectionName, provider: '' })), locale.value,
    model => model.name || '', model => `${model.name} · ${displayModelName(model)}`)
  return [kind, { rows, options }]
})) as Record<GenerationKind, { rows: GenerationOption[]; options: ReturnType<typeof modelPickerOptions> }>)
const selected = (kind: GenerationKind) => {
  const binding = saved(kind)
  return binding ? byKind.value[kind].rows.find(row => row.connection === binding.connection && row.model === binding.model) : undefined
}
const legacyLabel = (kind: GenerationKind) => {
  const binding = saved(kind)
  if (!binding) return ''
  const account = props.settings.models.find(row => (row.connectionId || row.key) === binding.connection)
  return account?.name ? `${account.name} · ${binding.model}` : binding.model
}
let rosterRevision = ''
const stop = props.stateSync.subscribe(['models', 'catalog'], async (topics, signal, snapshots) => {
  const catalog = snapshots.catalog
  const catalogRevision = catalog && !catalog.updating ? `${catalog.revision}:${catalog.modelsDevRevision || ''}` : ''
  if (candidates.value && !topics.includes('models') && (!catalogRevision || catalogRevision === rosterRevision)) return
  try {
    const result = await props.client.request<GenerationOption[]>('/generation-models', { signal })
    if (signal.aborted) return
    candidates.value = result
    candidateError.value = ''
    if (catalogRevision) rosterRevision = catalogRevision
  } catch (cause) {
    if (!signal.aborted) candidateError.value = String(cause)
    throw cause
  }
})
onScopeDispose(() => { stop(); emit('workflow-open', false) })

function closeWorkflow() { workflowChoice.value = undefined; emit('workflow-open', false) }
function editWorkflow(choice: GenerationOption) {
  const prior = saved(choice.kind)
  const same = prior?.model === choice.model && prior.connection === choice.connection
  workflow.value = same ? { ...(prior?.options as typeof workflow.value | undefined) } : {}
  workflowName.value = ''
  revision.value = props.settings.settingsRevision || ''
  workflowChoice.value = choice
  saveError.value = ''
  emit('workflow-open', true)
}
async function loadWorkflow(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0], current = workflowChoice.value
  if (!file || !current) return
  try {
    if (file.size > 16 * 1024) throw new Error(t('generationWorkflowInvalid'))
    const payload = JSON.parse(await file.text())
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error(t('generationWorkflowInvalid'))
    if (current !== workflowChoice.value) return
    workflow.value.workflow = payload
    workflowName.value = file.name
    saveError.value = ''
  } catch { if (current === workflowChoice.value) saveError.value = t('generationWorkflowInvalid') }
}
async function persist(kind: GenerationKind, choice?: GenerationOption) {
  if (busy.value || conflict.value || props.disabled) return
  busy.value = true
  saveError.value = ''
  try {
    const generation = choice ? { kind, model: choice.model, connection: choice.connection, ...(choice.workflow ? { options: workflow.value } : {}) } : { kind, model: '' }
    const result = await props.client.request<ModelSettings>('/model-config', {
      method: 'PATCH', body: JSON.stringify({ generation, revision: choice?.workflow ? revision.value : props.settings.settingsRevision }),
    })
    closeWorkflow()
    emit('saved', result)
  } catch (error) { saveError.value = error instanceof Error ? error.message : String(error) }
  finally { busy.value = false }
}
function choose(kind: GenerationKind, value: unknown) {
  if (value === undefined || value === '') { if (saved(kind)) void persist(kind); return }
  const choice = byKind.value[kind].rows.find(row => valueKey(row.connection, row.model) === value)
  if (!choice) return
  if (choice.workflow) editWorkflow(choice)
  else if (choice !== selected(kind)) void persist(kind, choice)
}
const keepsWorkflow = computed(() => !!workflowChoice.value && !!saved(workflowChoice.value.kind)?.hasWorkflow &&
  saved(workflowChoice.value.kind)?.connection === workflowChoice.value.connection && saved(workflowChoice.value.kind)?.model === workflowChoice.value.model)
</script>

<template>
  <section class="generation-settings">
    <h3>{{ t('generationModels') }}</h3>
    <slot />
    <div v-for="kind in kinds" :key="kind" class="generation-binding" :data-generation-purpose="kind">
      <label :for="`generation-${kind}-model`">{{ t(labels[kind]) }}</label>
      <div class="generation-selection">
        <v-select :id="`generation-${kind}-model`" :aria-label="t(labels[kind])" :value="selected(kind) ? valueKey(selected(kind)!.connection, selected(kind)!.model) : undefined"
          :options="byKind[kind].options" show-search option-filter-prop="searchText" option-label-prop="fullLabel" allow-clear
          :disabled="busy || disabled || !byKind[kind].options.length" :placeholder="t(candidates ? 'chooseModel' : 'uiLoading')" @update:value="choose(kind, $event)" />
        <small v-if="candidates && !byKind[kind].options.length" class="generation-note">{{ t(settings.models.length ? 'generationNoCandidates' : 'generationNeedConnection') }}</small>
        <small v-if="selected(kind)?.noFirstFrame" class="generation-note">{{ t('generationNoFirstFrame') }}</small>
        <div v-if="saved(kind) && !selected(kind) && candidates" class="generation-legacy">
          <span>{{ t('generationSavedUnavailable', { model: legacyLabel(kind) }) }}</span>
          <v-button :disabled="busy || disabled" @click="persist(kind)">{{ t('generationClear') }}</v-button>
        </div>
        <v-button v-if="selected(kind)?.workflow" :disabled="busy || disabled" @click="editWorkflow(selected(kind)!)">{{ t('edit') }}</v-button>
      </div>
    </div>
    <v-alert v-if="candidateError || saveError && !workflowChoice" type="error" role="alert" :message="candidateError || saveError" />
    <v-modal v-if="workflowChoice" :open="true" centered :width="480" :title="t(labels[workflowChoice.kind])"
      :ok-text="t('saveChanges')" :cancel-text="t('cancel')" :confirm-loading="busy" :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
      :ok-button-props="{ disabled: conflict || (!keepsWorkflow && !workflow.workflow) || !workflow.promptNode || !workflow.outputNode }"
      @cancel="closeWorkflow" @ok="persist(workflowChoice.kind, workflowChoice)">
      <v-alert v-if="saveError || conflict" type="error" role="alert" :message="saveError || t('generationSettingsChanged')" />
      <div class="generation-fields">
        <label>{{ t('generationWorkflow') }}<input type="file" accept=".json,application/json" @change="loadWorkflow" /></label>
        <span v-if="workflowName">{{ workflowName }}</span>
        <label>{{ t('generationPromptNode') }}<input v-model="workflow.promptNode" /></label>
        <label>{{ t('generationOutputNode') }}<input v-model="workflow.outputNode" /></label>
        <label v-if="workflowChoice.kind === 'video'">{{ t('generationImageNode') }}<input v-model="workflow.imageNode" /></label>
        <label class="workflow-cloud"><input v-model="workflow.cloud" type="checkbox" /> Comfy Cloud</label>
      </div>
    </v-modal>
  </section>
</template>

<style scoped>
.generation-settings{margin-block:14px 20px}.generation-binding,:slotted(.generation-binding){margin:0;display:grid;grid-template-columns:minmax(80px,1fr) minmax(0,3fr);align-items:baseline;gap:12px;padding:8px 0}.generation-binding>label{font-weight:500}.generation-selection{display:grid;gap:5px;min-width:0}.generation-note{color:var(--muted)}.generation-legacy{display:flex;align-items:center;justify-content:space-between;gap:8px;overflow-wrap:anywhere}.generation-fields{display:grid;gap:12px;margin-block:12px}.generation-fields label:not(.workflow-cloud){display:grid;gap:4px}.generation-fields input:not([type=checkbox]){width:100%;min-height:34px;padding:4px 8px;color:var(--text);background:var(--surface);border:1px solid var(--border);border-radius:6px}
</style>
