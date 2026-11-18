<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Modal as VModal, Alert as VAlert } from './ui'
import type { VistaClient } from './client/vista'
import type { GenerationKind, GenerationSetting, ModelSettings } from './client/types'

const props = defineProps<{ settings: ModelSettings; client: VistaClient }>()
const emit = defineEmits<{ saved: [ModelSettings] }>()
const { t } = useI18n()
const kinds = ['image', 'video', 'decision'] as const
const labels = { image: 'generationImage', video: 'generationVideo', decision: 'generationDecision' } as const
const active = ref<GenerationKind>()
const draft = ref<GenerationSetting>()
const apiKey = ref('')
const revision = ref('')
const busy = ref(false)
const failure = ref('')
const candidates = ref<{ id: string; name: string; input: string[] }[]>([])
const workflowName = ref('')
const workflow = ref<{ workflow?: unknown; promptNode?: string; outputNode?: string; imageNode?: string; cloud?: boolean }>({})
const choices = computed(() => props.settings.generationAdapters?.filter(adapter => active.value && adapter.kinds.includes(active.value)) ?? [])
const adapter = computed(() => choices.value.find(item => item.id === draft.value?.adapter))
const saved = (kind: GenerationKind) => props.settings.generation?.find(item => item.kind === kind)
const connections = computed(() => props.settings.models.filter(row => row.connectionKey === row.key && !row.disabled &&
  (row.providerId === draft.value?.adapter || row.providerId === 'togetherai' && draft.value?.adapter === 'together' || row.providerId === 'azure' && draft.value?.adapter === 'microsoft-foundry')))
const conflict = computed(() => !!active.value && props.settings.settingsRevision !== revision.value)
function open(kind: GenerationKind) {
  active.value = kind
  draft.value = { kind, adapter: '', model: '', ...saved(kind) }
  apiKey.value = ''
  failure.value = ''
  revision.value = props.settings.settingsRevision || ''
  workflow.value = { ...(draft.value.options as typeof workflow.value | undefined) }
  workflowName.value = ''
}
function changeAdapter(event: Event) {
  if (!draft.value) return
  draft.value = { kind: draft.value.kind, adapter: (event.target as HTMLSelectElement).value, model: '' }
  apiKey.value = ''
  workflow.value = {}
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
  if (!value || busy.value || conflict.value) return
  busy.value = true
  failure.value = ''
  try {
    const generation = clear ? { kind: value.kind, adapter: '', model: '' } : {
      kind: value.kind, adapter: value.adapter, model: value.model.trim(),
      ...(value.connection ? { connection: value.connection } : { baseUrl: value.baseUrl?.trim(), ...(apiKey.value ? { apiKey: apiKey.value } : {}) }),
      ...(value.adapter === 'comfy' ? { options: workflow.value } : {}),
    }
    const result = await props.client.request<ModelSettings>('/model-config', {
      method: 'PATCH', body: JSON.stringify({ generation, revision: revision.value }),
    })
    emit('saved', result)
    active.value = undefined
  } catch (error) { failure.value = String(error) }
  finally { apiKey.value = ''; busy.value = false }
}
</script>

<template>
  <section v-if="settings.generationAdapters?.length" class="generation-settings">
    <h3>{{ t('generationModels') }}</h3>
    <div v-for="kind in kinds" :key="kind" class="generation-binding">
      <div><strong>{{ t(labels[kind]) }}</strong><div>{{ saved(kind)?.model || t('generationNotConfigured') }}</div></div>
      <v-button :aria-label="t(labels[kind])" @click="open(kind)">{{ t('edit') }}</v-button>
    </div>
    <v-modal v-if="draft" :open="!!active" centered :width="480" :title="t(labels[draft.kind])"
      :ok-text="t('saveChanges')" :cancel-text="t('cancel')" :confirm-loading="busy" :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
      :ok-button-props="{ disabled: conflict || !draft.adapter || !draft.model.trim() || (draft.adapter === 'openai-codex' && !draft.connection) || (draft.adapter === 'comfy' && ((!draft.hasWorkflow && !workflow.workflow) || !workflow.promptNode || !workflow.outputNode)) }"
      @cancel="active = undefined" @ok="save()">
      <v-alert v-if="failure || conflict" type="error" role="alert" :message="failure || t('generationSettingsChanged')" />
      <div class="generation-fields">
        <label>{{ t('provider') }}<select :value="draft.adapter" :disabled="busy || conflict" @change="changeAdapter"><option value="" disabled>{{ t('chooseProviderHint') }}</option><option v-for="item in choices" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
        <template v-if="adapter">
          <label v-if="connections.length || draft.connection">{{ t('modelConnections') }}<select v-model="draft.connection" :disabled="busy || conflict"><option value="">{{ t('generationSeparateConnection') }}</option><option v-for="row in connections" :key="row.key" :value="row.key">{{ row.name || row.id }}</option></select></label>
          <label>{{ t('model') }}<input v-model="draft.model" :disabled="busy || conflict" list="generation-candidates" maxlength="200" /></label>
          <datalist id="generation-candidates"><option v-for="row in candidates" :key="row.id" :value="row.id">{{ row.name }}</option></datalist>
          <p v-if="draft.kind === 'video' && !adapter.frame">{{ t('generationNoFirstFrame') }}</p>
          <template v-if="!draft.connection">
            <label>{{ t('baseUrl') }}<input v-model="draft.baseUrl" :disabled="busy || conflict" :placeholder="adapter.baseUrl" maxlength="2048" /></label>
            <label>{{ t('apiKey') }}<input v-model="apiKey" :disabled="busy || conflict" type="password" autocomplete="new-password" :placeholder="draft.hasApiKey ? t('generationKeepKey') : undefined" /></label>
          </template>
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
