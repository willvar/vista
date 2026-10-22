<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Checkbox as ACheckbox, Input as AInput, Modal as AModal, Select as ASelect, Spin as ASpin } from 'antdv-next'
import { ReloadOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'

type ImportConnection = { id: string; name: string; providerId?: string; baseUrl?: string; reason?: string; description?: string; location?: string; warning?: string; disabled?: boolean; fileCount?: number; credentialSource?: string; credentialGroup?: string }
type Preview = { configPaths: string[]; credentialPaths?: string[]; directory?: string; fingerprint: string; connections: ImportConnection[]; plugins?: string[]; entries?: ImportConnection[] }
const props = withDefaults(defineProps<{ kind?: 'providers' | 'mcp' | 'skills' }>(), { kind: 'providers' })
const titleKey = computed(() => props.kind === 'providers' ? 'providerImport' : props.kind === 'mcp' ? 'mcpImport' : 'skillsImport')
const endpoint = computed(() => props.kind === 'providers' ? '/provider-import' : '/extension-import')
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const emit = defineEmits<{ 'modal-open': [open: boolean]; imported: [count: number] }>()
const open = ref(false)
const source = ref('opencode')
const configPath = ref('')
const credentialPath = ref('')
const directory = ref('')
const preview = ref<Preview>()
const selected = ref<string[]>([])
const reading = ref(false)
const importing = ref(false)
const error = ref('')
const busy = computed(() => reading.value || importing.value)
let request: AbortController | undefined
const reasons: Record<string, string> = {
  existing: 'providerImportExisting', credentials: 'providerImportCredentials',
  oauth: 'providerImportOAuth', unsupported: 'providerImportUnsupported', invalid: 'providerImportInvalid',
  reference: 'providerImportReference', limit: 'providerImportLimit',
}
const extensionReasons: Record<string, string> = { existing: 'extensionImportExisting', unsupported: 'extensionImportUnsupported', invalid: 'extensionImportInvalid', files: 'extensionImportFiles', command: 'extensionImportCommand', remote: 'extensionImportRemote', format: 'extensionImportFormat', duplicate: 'extensionImportDuplicate', reference: 'providerImportReference' }
function reasonKey(reason: string) { return (props.kind === 'providers' ? reasons : extensionReasons)[reason] || 'extensionImportUnsupported' }
function requestBody(action: string) {
  return { source: source.value, action, configPath: configPath.value, ...(props.kind === 'providers' ? { credentialPath: credentialPath.value } : { kind: props.kind, directory: directory.value }) }
}
function invalidate() { preview.value = undefined; selected.value = []; error.value = '' }
function toggle(id: string, checked: boolean) {
  const group = preview.value?.connections.find(entry => entry.id === id)?.credentialGroup
  const alternatives = new Set(group ? preview.value?.connections.filter(entry => entry.credentialGroup === group).map(entry => entry.id) : [id])
  selected.value = checked ? [...selected.value.filter(value => !alternatives.has(value)), id] : selected.value.filter(value => value !== id)
}
function cancel() {
  if (importing.value) return
  request?.abort()
  request = undefined
  reading.value = false
  open.value = false
  emit('modal-open', false)
}
async function read() {
  if (busy.value) return
  invalidate()
  reading.value = true
  const controller = new AbortController()
  request = controller
  try {
    const result = await client.request<Preview>(endpoint.value, {
      method: 'POST', signal: controller.signal,
      body: JSON.stringify(requestBody('preview')),
    })
    if (controller.signal.aborted) return
    preview.value = { ...result, connections: result.connections || result.entries || [] }
    selected.value = preview.value.connections.filter(entry => !entry.reason && !entry.credentialGroup).map(entry => entry.id)
  } catch (failure) { if (!controller.signal.aborted) error.value = failure instanceof Error ? failure.message : String(failure) }
  finally { if (request === controller) { reading.value = false; request = undefined } }
}
async function confirm() {
  if (busy.value || !preview.value || !selected.value.length) return
  importing.value = true
  error.value = ''
  const controller = new AbortController()
  request = controller
  try {
    const result = await client.request<{ imported: number }>(endpoint.value, {
      method: 'POST', signal: controller.signal,
      body: JSON.stringify({ ...requestBody('import'),
        fingerprint: preview.value.fingerprint, ids: selected.value }),
    })
    emit('imported', result.imported)
    open.value = false
    emit('modal-open', false)
  } catch (failure) { if (!controller.signal.aborted) error.value = failure instanceof Error ? failure.message : String(failure) }
  finally { importing.value = false; if (request === controller) request = undefined }
}
function show() {
  if (open.value) return
  source.value = 'opencode'
  configPath.value = ''
  credentialPath.value = ''
  directory.value = ''
  invalidate()
  open.value = true
  emit('modal-open', true)
}
onBeforeUnmount(() => { request?.abort(); emit('modal-open', false) })
defineExpose({ show })
</script>

<template>
  <a-modal :open="open" centered :z-index="1200" width="min(580px, calc(100vw - 2rem))"
    class="provider-import-modal" :title="t(titleKey)" :ok-text="t('providerImportConfirm', { count: selected.length })" :cancel-text="t('cancel')"
    :confirm-loading="importing" :ok-button-props="{ disabled: busy || !selected.length }" :cancel-button-props="{ disabled: importing }"
    :closable="!importing" :keyboard="!importing" :mask-closable="!importing"
    :styles="{ body: { maxHeight: 'calc(100dvh - 12rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }"
    @ok="confirm" @cancel="cancel">
    <div class="provider-import-content" :aria-busy="busy">
      <a-alert type="info" show-icon :message="t(props.kind === 'providers' ? 'providerImportRecommendation' : 'extensionImportRecommendation')" />
      <label class="provider-import-source"><span>{{ t('providerImportSource') }}</span><a-select v-model:value="source" :disabled="busy" :options="[{ label: 'OpenCode', value: 'opencode' }]" :aria-label="t('providerImportSource')" /></label>
      <p class="provider-import-hint">{{ t(props.kind === 'providers' ? 'providerImportScope' : 'extensionImportScope') }}<template v-if="preview?.plugins?.length"> {{ t('providerImportAgentHint') }}</template></p>
      <details class="provider-import-paths">
        <summary>{{ t('providerImportPaths') }}</summary>
        <label><span>{{ t('providerImportConfigPath') }}</span><a-input v-model:value="configPath" :disabled="busy" :placeholder="preview?.configPaths.join(', ') || t('providerImportAutomatic')" :aria-label="t('providerImportConfigPath')" @update:value="invalidate" /></label>
        <label v-if="props.kind === 'providers'"><span>{{ t('providerImportAuthPath') }}</span><a-input v-model:value="credentialPath" :disabled="busy" :placeholder="preview?.credentialPaths?.join(', ') || t('providerImportAutomatic')" :aria-label="t('providerImportAuthPath')" @update:value="invalidate" /></label>
        <template v-if="props.kind !== 'providers'"><label><span>{{ t('extensionImportDirectory') }}</span><a-input v-model:value="directory" :disabled="busy" :placeholder="preview?.directory || t('providerImportAutomatic')" :aria-label="t('extensionImportDirectory')" @update:value="invalidate" /></label><p class="provider-import-hint">{{ t(props.kind === 'mcp' ? 'mcpImportDirectoryHint' : 'skillsImportDirectoryHint') }}</p></template>
      </details>
      <div class="provider-import-read"><a-button :disabled="busy" :loading="reading" @click="read"><template #icon><ReloadOutlined aria-hidden="true" /></template>{{ t('providerImportRead') }}</a-button></div>
      <div v-if="reading" class="provider-import-loading" role="status"><a-spin size="small" />{{ t('providerImportReading') }}</div>
      <template v-else-if="preview">
        <p v-if="preview.connections.some(entry => entry.credentialGroup && !entry.reason)" class="provider-import-hint">{{ t('providerImportCredentialConflict') }}</p>
        <p v-if="!preview.connections.length" class="provider-import-hint" role="status">{{ t(props.kind === 'providers' ? 'providerImportEmpty' : 'extensionImportEmpty') }}</p>
        <div v-else class="provider-import-list" :aria-label="t(props.kind === 'providers' ? 'providerImportConnections' : titleKey)">
          <a-checkbox v-for="entry in preview.connections" :key="entry.id" :checked="selected.includes(entry.id)" :disabled="!!entry.reason || busy" @change="toggle(entry.id, $event.target.checked)">
            <span class="provider-import-entry"><strong>{{ entry.name }}</strong><small>{{ entry.baseUrl || entry.location || entry.providerId || entry.credentialGroup || entry.id }}</small><small v-if="entry.credentialSource">{{ t('providerImportCredentialSource', { source: entry.credentialSource }) }}</small><small v-if="entry.description">{{ entry.description }}</small><small v-if="entry.fileCount">{{ t('extensionImportFileCount', { count: entry.fileCount }) }}</small><small v-if="entry.disabled">{{ t('skillDisabled') }}</small><small v-if="entry.warning">{{ t(entry.warning === 'oauth' ? 'extensionImportOAuth' : 'extensionImportAuth') }}</small><small v-if="entry.reason" class="provider-import-reason">{{ t(reasonKey(entry.reason)) }}</small></span>
          </a-checkbox>
        </div>
      </template>
      <a-alert v-if="importing" type="info" role="status" :message="t(props.kind === 'providers' ? 'providerImportProgress' : 'extensionImportProgress')" />
      <a-alert v-if="error" type="error" role="alert" :message="error" />
    </div>
  </a-modal>
</template>

<style>
.provider-import-content { display: flex; flex-direction: column; gap: 16px; }
.provider-import-source { display: flex; align-items: center; gap: 16px; }
.provider-import-source > .ant-select { flex: 1; min-width: 0; }
.provider-import-hint { margin: 0; color: var(--text-secondary); line-height: 1.65; }
.provider-import-paths > summary { cursor: pointer; color: var(--text-secondary); }
.provider-import-paths > label { display: flex; flex-direction: column; gap: 6px; margin-top: 12px; }
.provider-import-list { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: 10px; }
.provider-import-list > .ant-checkbox-wrapper { align-items: flex-start; margin: 0; padding: 12px; }
.provider-import-list > .ant-checkbox-wrapper + .ant-checkbox-wrapper { border-top: 1px solid var(--border); }
.provider-import-list .ant-checkbox { margin-top: 4px; }
.provider-import-list .ant-checkbox + span { min-width: 0; }
.provider-import-entry { display: flex; flex-direction: column; gap: 3px; overflow-wrap: anywhere; }
.provider-import-entry > strong { font-weight: 600; }
.provider-import-entry > small { color: var(--text-secondary); font-size: var(--font-size-small); }
.provider-import-loading { display: flex; align-items: center; gap: 10px; color: var(--text-secondary); }
</style>
