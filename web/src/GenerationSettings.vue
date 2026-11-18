<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Modal as VModal, Alert as VAlert } from './ui'
import type { VistaClient } from './client/vista'
import type { GenerationKind, GenerationSetting, ModelSetting, ModelSettings } from './client/types'

const props = defineProps<{ settings: ModelSettings; client: VistaClient }>()
const emit = defineEmits<{ saved: [ModelSettings]; connect: [] }>()
const { t } = useI18n()
const kinds = ['image', 'video', 'decision'] as const
const labels = { image: 'generationImage', video: 'generationVideo', decision: 'generationDecision' } as const
const active = ref<GenerationKind>()
const draft = ref<GenerationSetting>()
const revision = ref('')
const busy = ref(false)
const failure = ref('')
const candidates = ref<{ id: string; name: string; input: string[] }[]>([])
const workflowName = ref('')
const workflow = ref<{ workflow?: unknown; promptNode?: string; outputNode?: string; imageNode?: string; cloud?: boolean }>({})
const saved = (kind: GenerationKind) => props.settings.generation?.find(item => item.kind === kind)
const connectionRef = (row: ModelSetting) => row.connectionId || row.key
const connectionName = (row: ModelSetting) => row.name || row.providerId || row.id
const connectionsFor = (kind: GenerationKind) => props.settings.models.filter(row => row.generationAdapters?.[kind]?.length && !row.disabled)
const connections = computed(() => active.value ? connectionsFor(active.value) : [])
const connection = computed(() => connections.value.find(row => connectionRef(row) === draft.value?.connection))
const choices = computed(() => (props.settings.generationAdapters || []).filter(adapter =>
  !!active.value && connection.value?.generationAdapters?.[active.value]?.includes(adapter.id)))
const adapter = computed(() => choices.value.find(item => item.id === draft.value?.adapter))
const conflict = computed(() => !!active.value && props.settings.settingsRevision !== revision.value)
function bindingLabel(kind: GenerationKind) {
  const binding = saved(kind)
  if (!binding) return t('generationNotConfigured')
  const row = props.settings.models.find(row => connectionRef(row) === binding.connection && row.generationAdapters?.[kind]?.includes(binding.adapter))
  return `${row ? connectionName(row) : t('generationNeedConnection')} · ${binding.model}`
}
function open(kind: GenerationKind) {
  const binding = saved(kind)
  active.value = kind
  draft.value = { kind, adapter: '', model: '', ...binding }
  failure.value = ''
  revision.value = props.settings.settingsRevision || ''
  workflow.value = { ...(draft.value.options as typeof workflow.value | undefined) }
  workflowName.value = ''
  if (!binding && connections.value.length === 1) changeConnection(connectionRef(connections.value[0]!))
}
function connectProvider() {
  active.value = undefined
  emit('connect')
}
function changeConnection(ref: string) {
  if (!draft.value) return
  const kind = draft.value.kind
  const ids = connections.value.find(row => connectionRef(row) === ref)?.generationAdapters?.[kind] ?? []
  draft.value = { kind, connection: ref, adapter: ids.length === 1 ? ids[0]! : '', model: '' }
  workflow.value = {}
  workflowName.value = ''
}
function changeAdapter(event: Event) {
  if (!draft.value) return
  draft.value = { ...draft.value, adapter: (event.target as HTMLSelectElement).value, model: '', hasWorkflow: false }
  workflow.value = {}
  workflowName.value = ''
}
watch(() => [draft.value?.adapter, active.value] as const, ([id, kind], _, cleanup) => {
  candidates.value = []
  if (!id || !kind) return
  const controller = new AbortController()
  cleanup(() => controller.abort())
  const query = new URLSearchParams({ adapter: id, kind })
  void props.client.request<typeof candidates.value | null>(`/generation-models?${query}`, { signal: controller.signal }).then(items => {
    if (!controller.signal.aborted) candidates.value = items || []
  }).catch(() => { /* An explicit provider model ID remains available. */ })
})
async function loadWorkflow(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  try {
    if (file.size > 16 * 1024) throw new Error(t('generationWorkflowInvalid'))
    const payload = JSON.parse(await file.text())
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error(t('generationWorkflowInvalid'))
    workflow.value.workflow = payload
    workflowName.value = file.name
    failure.value = ''
  } catch { failure.value = t('generationWorkflowInvalid') }
}
async function save(clear = false) {
  const value = draft.value
  if (!value || busy.value || conflict.value || (!clear && (!connection.value || !adapter.value))) return
  busy.value = true
  failure.value = ''
  try {
    const generation = clear ? { kind: value.kind, model: '' } : {
      kind: value.kind, adapter: value.adapter, model: value.model.trim(), connection: value.connection,
      ...(value.adapter === 'comfy' ? { options: workflow.value } : {}),
    }
    const result = await props.client.request<ModelSettings>('/model-config', {
      method: 'PATCH', body: JSON.stringify({ generation, revision: revision.value }),
    })
    emit('saved', result)
    active.value = undefined
  } catch (error) { failure.value = String(error) }
  finally { busy.value = false }
}
</script>

<template>
  <section v-if="settings.generationAdapters?.length" class="generation-settings">
    <h3>{{ t('generationModels') }}</h3>
    <div v-for="kind in kinds" :key="kind" class="generation-binding">
      <div><strong>{{ t(labels[kind]) }}</strong><div>{{ bindingLabel(kind) }}</div></div>
      <v-button :aria-label="t(labels[kind])" @click="open(kind)">{{ t('edit') }}</v-button>
    </div>
    <v-modal v-if="draft" :open="!!active" centered :width="480" :title="t(labels[draft.kind])"
      :ok-text="t('saveChanges')" :cancel-text="t('cancel')" :confirm-loading="busy" :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
      :ok-button-props="{ disabled: conflict || !connection || !adapter || !draft.model.trim() || (draft.adapter === 'comfy' && ((!draft.hasWorkflow && !workflow.workflow) || !workflow.promptNode || !workflow.outputNode)) }"
      @cancel="active = undefined" @ok="save()">
      <v-alert v-if="failure || conflict" type="error" role="alert" :message="failure || t('generationSettingsChanged')" />
      <div class="generation-fields">
        <template v-if="connections.length">
          <label>{{ t('modelConnections') }}<select :value="connection ? draft.connection : ''" :disabled="busy || conflict" @change="changeConnection(($event.target as HTMLSelectElement).value)"><option value="" disabled>{{ t('chooseProvider') }}</option><option v-for="row in connections" :key="connectionRef(row)" :value="connectionRef(row)">{{ connectionName(row) }}</option></select></label>
          <label v-if="choices.length > 1">{{ t('generationAPIFormat') }}<select :value="draft.adapter" :disabled="busy || conflict" @change="changeAdapter"><option value="" disabled>{{ t('generationAPIFormat') }}</option><option v-for="item in choices" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        </template>
        <div v-else><p>{{ t('generationNeedConnection') }}</p><v-button :disabled="busy" @click="connectProvider">{{ t('connectProvider') }}</v-button></div>
        <template v-if="adapter">
          <label>{{ t('model') }}<input v-model="draft.model" :disabled="busy || conflict" list="generation-candidates" maxlength="200" /></label>
          <datalist id="generation-candidates"><option v-for="row in candidates" :key="row.id" :value="row.id">{{ row.name }}</option></datalist>
          <p v-if="draft.kind === 'video' && !adapter.frame">{{ t('generationNoFirstFrame') }}</p>
          <template v-if="adapter.id === 'comfy'">
            <label>{{ t('generationWorkflow') }}<input type="file" accept=".json,application/json" @change="loadWorkflow" /></label>
            <span v-if="workflowName">{{ workflowName }}</span>
            <label>{{ t('generationPromptNode') }}<input v-model="workflow.promptNode" /></label>
            <label>{{ t('generationOutputNode') }}<input v-model="workflow.outputNode" /></label>
            <label v-if="draft.kind === 'video'">{{ t('generationImageNode') }}<input v-model="workflow.imageNode" /></label>
            <label class="workflow-cloud"><input v-model="workflow.cloud" type="checkbox" /> Comfy Cloud</label>
          </template>
        </template>
        <v-button v-if="saved(draft.kind) && !conflict" :disabled="busy" @click="save(true)">{{ t('generationClear') }}</v-button>
      </div>
    </v-modal>
  </section>
</template>

<style scoped>
.generation-settings{margin-block:14px 20px}.generation-binding{display:flex;align-items:center;justify-content:space-between;gap:12px;border-block-end:1px solid var(--border);padding:8px 0}.generation-binding div div{color:var(--muted);overflow-wrap:anywhere}.generation-fields{display:grid;gap:12px;margin-block:12px}.generation-fields label:not(.workflow-cloud){display:grid;gap:4px}.generation-fields input:not([type=checkbox]),.generation-fields select{width:100%;min-height:34px;padding:4px 8px;color:var(--text);background:var(--surface);border:1px solid var(--border);border-radius:6px}
</style>
