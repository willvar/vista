<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Modal as VModal, Select as VSelect, Spin as VSpin } from './ui'
import SchemaForm from './SchemaForm.vue'
import { useLeaveConfirmation, useRouteLayer } from './composables/routeLayers'
import { useWorkspace } from './composables/useWorkspace'
import type { ResourceStorageConfiguration, ResourceStoragePreset, ResourceStorageRequest, ResourceStorageStatus } from '@vista/client'

const props = defineProps<{ configuration?: ResourceStorageConfiguration; snapshotEpoch: number }>()
const emit = defineEmits<{ changed: [value: ResourceStorageStatus, epoch: number]; 'modal-open': [value: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const layer = useRouteLayer('storage-config', to => to.name === 'settings-storage')
const { pending: discardOpen, ask: askDiscard, finish: finishDiscard } = useLeaveConfirmation()
const controller = new AbortController()
// The combobox needs nonempty option keys for keyboard navigation.
const localOption = '@local'
const activeId = computed(() => props.configuration?.activePresetId ?? '')
const presets = computed(() => props.configuration?.presets ?? [])
const job = computed(() => props.configuration?.migration)
const migrating = computed(() => ['syncing', 'failed', 'cancelling'].includes(job.value?.phase ?? ''))
const working = ref<ResourceStorageRequest['action']>()
const failure = ref(''), checked = ref(false)
const editor = ref<ResourceStoragePreset | null>(), nextId = ref(''), openedRevision = ref('')
const switchOpen = computed(() => layer.current.value === 'switch' && !!props.configuration)
const remove = ref<ResourceStoragePreset>(), removeRevision = ref('')
const draft = ref<Record<string, unknown>>({}), dirty = ref(false), valid = ref(false), resetKey = ref(0)
const blocked = computed(() => !!working.value || migrating.value)
const stale = computed(() => props.configuration?.revision !== openedRevision.value)
const locationLocked = computed(() => !!editor.value && editor.value.id === activeId.value)
const locationFields = ['endpoint', 'bucket', 'prefix'] as const
const fields = ['name', 'endpoint', 'bucket', 'region', 'prefix', 'accessKey', 'secretKey', 'sessionToken'] as const

function label(id?: string) {
  return id === '' ? t('storageLocal') : presets.value.find(preset => preset.id === id)?.name || t('storageObject')
}
const currentLabel = computed(() => props.configuration?.current.kind === 's3' ? label(props.configuration.activePresetId || undefined) : label(''))
const options = computed(() => [{ value: localOption, label: t('storageLocal') }, ...presets.value.map(preset => ({ value: preset.id, label: preset.name }))])
const canSwitch = computed(() => !blocked.value && !stale.value && nextId.value !== activeId.value && options.value.some(option => option.value === (nextId.value || localOption)))
function chooseDestination(value: unknown) {
  if (typeof value !== 'string') return
  nextId.value = value === localOption ? '' : value
  failure.value = ''
}

function loadEditor(id = layer.current.value) {
  const cfg = props.configuration
  if (!cfg || id === undefined) return
  failure.value = ''; checked.value = false
  openedRevision.value = cfg.revision
  nextId.value = activeId.value
  if (id === 'switch') { editor.value = undefined; return }
  const preset = presets.value.find(item => item.id === id)
  if (id && !preset) { layer.drop(); return }
  editor.value = preset || null
  draft.value = { name: preset?.name ?? '', endpoint: preset?.endpoint ?? '', bucket: preset?.bucket ?? '', region: preset?.region ?? '', prefix: preset?.prefix ?? '', accessKey: '', secretKey: '', sessionToken: '' }
  dirty.value = false; valid.value = false; resetKey.value++
}
let loaded: string | undefined
watch([layer.current, () => props.configuration?.revision, working], ([id, revision, action]) => {
  if (id !== undefined && !revision) return
  if (id === loaded) {
    if (id !== undefined && id !== 'switch' && revision && revision !== openedRevision.value && !dirty.value && !action) {
      // A new configuration has no saved fields to reload. Keep the input mounted during a late snapshot.
      if (id === '') openedRevision.value = revision
      else loadEditor(id)
    }
    return
  }
  loaded = id
  if (id === undefined) { editor.value = undefined; draft.value = {}; dirty.value = false; return }
  loadEditor(id)
}, { immediate: true })
watch([editor, switchOpen, remove, discardOpen], () => emit('modal-open', editor.value !== undefined || switchOpen.value || !!remove.value || discardOpen.value))
layer.onLeave(() => !working.value && (!dirty.value || askDiscard()))
onBeforeUnmount(() => { controller.abort(); finishDiscard(false); emit('modal-open', false) })

const schema = computed(() => ({ type: 'object' as const, additionalProperties: false, required: ['name', 'endpoint', 'bucket'], properties: {
  name: { type: 'string' as const, title: t('networkProfileName'), maxLength: 128, pattern: '\\S' },
  endpoint: { type: 'string' as const, title: t('storageEndpoint'), maxLength: 2048, pattern: '^https?://' },
  bucket: { type: 'string' as const, title: t('storageBucket'), minLength: 1, maxLength: 255 },
  region: { type: 'string' as const, title: t('storageRegion'), maxLength: 255 },
  prefix: { type: 'string' as const, title: t('storagePrefix'), maxLength: 1024 },
  accessKey: { type: 'string' as const, title: t('storageAccessKey'), maxLength: 4096, writeOnly: true },
  secretKey: { type: 'string' as const, title: t('storageSecretKey'), maxLength: 4096, writeOnly: true },
  sessionToken: { type: 'string' as const, title: t('storageSessionToken'), maxLength: 16384, writeOnly: true },
} }))
const ui = computed(() => ({ type: 'VerticalLayout' as const, elements: fields.map(field => ({ type: 'Control', scope: `#/properties/${field}`, options: { showErrorsOnBlur: true, ...(locationLocked.value && locationFields.some(value => value === field) ? { readonly: true } : {}) } })) }))
function credentialIssues(value: Record<string, unknown>) {
  const existing = editor.value
  const reuse = existing?.hasCredentials && locationFields.every(field => value[field] === (existing[field] ?? ''))
  if (reuse && !value.accessKey && !value.secretKey) return []
  return ['accessKey', 'secretKey'].flatMap(field => value[field] ? [] : [{ path: `/${field}`, message: t('configFieldRequired') }])
}

async function act(input: ResourceStorageRequest) {
  if (working.value) return
  const epoch = props.snapshotEpoch
  working.value = input.action; failure.value = ''; checked.value = false
  try {
    const result = await client.request<ResourceStorageStatus>('/resource-storage', { method: 'POST', body: JSON.stringify(input), signal: controller.signal })
    if (controller.signal.aborted) return
    emit('changed', result, epoch)
    if (input.action === 'check' || result.checkError) openedRevision.value = result.config?.revision || openedRevision.value
    if (result.checkError) { failure.value = t('storageVerificationFailed'); return }
    if (input.action === 'check') checked.value = true
    return true
  } catch (cause) {
    if (!controller.signal.aborted) failure.value = cause instanceof Error ? cause.message : String(cause)
  } finally { working.value = undefined }
}
async function edit(action: 'save' | 'check') {
  if (blocked.value || !valid.value || stale.value || action === 'save' && !dirty.value) return
  const close = layer.captureClose()
  const config = {
    name: String(draft.value.name), endpoint: String(draft.value.endpoint), bucket: String(draft.value.bucket),
    region: String(draft.value.region), prefix: String(draft.value.prefix), accessKey: String(draft.value.accessKey),
    secretKey: String(draft.value.secretKey), sessionToken: String(draft.value.sessionToken),
  }
  if (await act({ action, config, presetId: editor.value?.id, revision: openedRevision.value }) && action === 'save') {
    dirty.value = false; close()
  }
}
async function migrate() {
  if (!canSwitch.value) return
  const close = layer.captureClose()
  if (await act({ action: 'migrate', presetId: nextId.value, revision: openedRevision.value })) close()
}
async function removePreset() {
  if (!remove.value || blocked.value) return
  if (await act({ action: 'remove', presetId: remove.value.id, revision: removeRevision.value })) remove.value = undefined
}
async function reload() {
  if (!dirty.value || await askDiscard()) loadEditor()
}
function openRemove(preset: ResourceStoragePreset) {
  remove.value = preset; removeRevision.value = props.configuration?.revision || ''; failure.value = ''
}
function closeRemove() {
  if (!working.value) { remove.value = undefined; failure.value = '' }
}
function migrationAction(action: 'cancel' | 'retry') {
  if (job.value) void act({ action, migrationId: job.value.id })
}
</script>

<template>
  <section v-if="configuration" class="storage-destination" :aria-label="t('storageMethod')">
    <div class="storage-row">
      <strong class="storage-current">{{ t('storageCurrent', { location: currentLabel }) }}</strong>
      <v-button v-if="!migrating" :disabled="blocked || !presets.length" @click="layer.open('switch')">{{ t('storageSwitch') }}</v-button>
    </div>
    <p class="storage-hint">{{ t('storageScopeHint') }}</p>
    <div v-if="job && migrating" class="storage-migration" role="status" aria-live="polite">
      <strong v-if="configuration.targetPresetId !== undefined"><bdi>{{ currentLabel }}</bdi> → <bdi>{{ label(configuration.targetPresetId) }}</bdi></strong>
      <div class="storage-actions">
        <v-spin v-if="job.phase !== 'failed'" />
        <span>{{ t(job.phase === 'cancelling' ? 'storageCancelling' : job.phase === 'failed' ? 'storageMigrationFailed' : 'storageMigrating', { count: job.copiedCount }) }}</span>
      </div>
      <div class="storage-actions">
        <v-button v-if="job.phase === 'failed'" :disabled="!!working" @click="migrationAction('retry')">{{ t('retry') }}</v-button>
        <v-button v-if="job.phase === 'syncing' || job.phase === 'failed'" :disabled="!!working" @click="migrationAction('cancel')">{{ t('cancel') }}</v-button>
      </div>
    </div>
    <v-alert v-if="failure && layer.current.value === undefined && !remove" type="error" :message="failure" />
    <div class="storage-row storage-presets-header">
      <h3>{{ t('storageProfiles') }}</h3>
      <v-button :disabled="blocked" @click="layer.open()">{{ t('storageAdd') }}</v-button>
    </div>
    <p class="storage-hint">{{ t(presets.length ? 'storagePresetHint' : 'storageNoPresets') }}</p>
    <div v-for="preset in presets" :key="preset.id" class="storage-row storage-preset">
      <div class="storage-preset-name"><strong>{{ preset.name }}</strong><small>DOFS<span v-if="preset.id === activeId"> · {{ t('storageInUse') }}</span></small></div>
      <div class="storage-actions">
        <v-button :disabled="blocked" @click="layer.open(preset.id)">{{ t('edit') }}</v-button>
        <v-button danger :disabled="blocked || preset.id === activeId" @click="openRemove(preset)">{{ t('delete') }}</v-button>
      </div>
    </div>
  </section>

  <v-modal :open="editor !== undefined" :title="t(editor ? 'storageEdit' : 'storageAdd')" :footer="null" centered width="min(720px, calc(100vw - 2rem))" :z-index="1200"
    :close-disabled="!!working" :keyboard="!working && !discardOpen" :mask-closable="!working && !discardOpen" destroy-on-hidden
    :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="layer.close">
    <form v-if="editor !== undefined" class="storage-editor" @submit.prevent="edit('save')">
      <v-alert v-if="stale" type="warning" role="alert" :message="t('configEditorChanged')" />
      <v-button v-if="stale" type="button" :disabled="!!working" @click="reload">{{ t('configEditorReload') }}</v-button>
      <p v-if="locationLocked" class="storage-hint">{{ t('storageLocationLocked') }}</p>
      <p class="storage-hint">{{ t('storageCredentialsHint') }}</p>
      <SchemaForm id="dofs-storage" v-model="draft" :schema="schema" :uischema="ui" :validate="credentialIssues" :disabled="blocked || stale" :key="resetKey" draft-updates
        @validity="valid = $event" @dirty="dirty = $event" @update:model-value="checked = false; failure = ''" />
      <p v-if="checked" role="status">{{ t('storageCheckPassed') }}</p>
      <v-alert v-if="failure" type="error" role="alert" :message="failure" />
      <div class="storage-actions storage-footer">
        <v-button type="button" :disabled="!!working" @click="layer.close">{{ t('cancel') }}</v-button>
        <v-button type="button" :disabled="blocked || !valid || stale" :loading="working === 'check'" @click="edit('check')">{{ t('storageCheck') }}</v-button>
        <v-button type="submit" variant="primary" :disabled="blocked || !valid || !dirty || stale" :loading="working === 'save'">{{ t('saveChanges') }}</v-button>
      </div>
    </form>
  </v-modal>

  <v-modal :open="switchOpen" :title="t('storageSwitch')" :z-index="1200" :close-disabled="!!working" :keyboard="!working" :mask-closable="!working" @cancel="layer.close">
    <div v-if="switchOpen" class="storage-editor">
      <v-alert v-if="stale" type="warning" :message="t('configEditorChanged')" />
      <v-button v-if="stale" :disabled="!!working" @click="reload">{{ t('configEditorReload') }}</v-button>
      <label for="storage-target">{{ t('storageDestination') }}</label>
      <v-select id="storage-target" :aria-label="t('storageDestination')" :value="nextId || localOption" :options="options" :disabled="blocked || stale" @update:value="chooseDestination" />
      <strong v-if="nextId !== activeId"><bdi>{{ currentLabel }}</bdi> → <bdi>{{ label(nextId) }}</bdi></strong>
      <p class="storage-hint">{{ t('storageMigrationConfirm') }}</p>
      <v-alert v-if="failure" type="error" role="alert" :message="failure" />
    </div>
    <template #footer>
      <v-button :disabled="!!working" @click="layer.close">{{ t('cancel') }}</v-button>
      <v-button variant="primary" :loading="working === 'migrate'" :disabled="!canSwitch" @click="migrate">{{ t('storageApply') }}</v-button>
    </template>
  </v-modal>

  <v-modal :open="!!remove" :title="t('delete')" :z-index="1200" :close-disabled="!!working" :keyboard="!working" :mask-closable="!working" @cancel="closeRemove">
    <p>{{ t('storageRemovePreset', { name: remove?.name }) }}</p>
    <v-alert v-if="failure" type="error" role="alert" :message="failure" />
    <template #footer>
      <v-button :disabled="!!working" @click="closeRemove">{{ t('cancel') }}</v-button>
      <v-button danger :disabled="blocked" :loading="working === 'remove'" @click="removePreset">{{ t('delete') }}</v-button>
    </template>
  </v-modal>

  <v-modal :open="discardOpen" :title="t('networkDiscard')" :z-index="1400" @cancel="finishDiscard(false)">
    <p>{{ t('configEditorDiscard') }}</p>
    <template #footer>
      <v-button @click="finishDiscard(false)">{{ t('cancel') }}</v-button>
      <v-button danger @click="finishDiscard(true)">{{ t('networkDiscard') }}</v-button>
    </template>
  </v-modal>
</template>

<style scoped>
.storage-destination,.storage-editor,.storage-migration { display:flex; flex-direction:column; gap:.75rem; min-width:0; }
.storage-row,.storage-actions { display:flex; align-items:center; flex-wrap:wrap; gap:.6rem; min-width:0; }
.storage-row { justify-content:space-between; }
.storage-preset { border-top:1px solid var(--border); padding-block:.6rem; }
.storage-preset-name,.storage-current,.storage-editor strong,.storage-migration strong { min-width:0; overflow-wrap:anywhere; }
.storage-preset-name small { display:block; color:var(--muted); }
.storage-presets-header { margin-top:.5rem; }
.storage-hint { margin:0; color:var(--text-secondary); line-height:1.6; }
.storage-footer { justify-content:flex-end; }
@media (max-width:830px) { .storage-preset { flex-direction:column; align-items:flex-start; } }
</style>
