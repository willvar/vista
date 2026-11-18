<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Modal as VModal, Spin as VSpin } from './ui'
import SchemaForm from './SchemaForm.vue'
import ConfirmPopover from './ConfirmPopover.vue'
import { useWorkspace } from './composables/useWorkspace'
import type { ResourceStorageConfiguration, ResourceStorageStatus } from './client/types'

const props = defineProps<{ configuration?: ResourceStorageConfiguration; busy: boolean }>()
const emit = defineEmits<{ changed: [value: ResourceStorageStatus]; 'modal-open': [value: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const draft = ref<Record<string, unknown>>({ kind: 'local', endpoint: '', bucket: '', region: '', prefix: '', accessKey: '', secretKey: '', sessionToken: '' })
const pendingChange = ref(false)
const revision = ref(''), dirty = ref(false), valid = ref(false), working = ref(false), conflict = ref(false)
const reloadOpen = ref(false)
const confirmation = ref(false), failure = ref(''), checked = ref(false), resetKey = ref(0)
const controller = new AbortController()
let disposed = false
onBeforeUnmount(() => { disposed = true; controller.abort(); emit('modal-open', false) })
const job = computed(() => props.configuration?.migration)
const migrating = computed(() => ['syncing', 'failed', 'cancelling'].includes(job.value?.phase ?? ''))
const disabled = computed(() => working.value || props.busy || migrating.value)
const currentLabel = computed(() => props.configuration?.current.kind === 's3' ? t('storageObject') : t('storageLocal'))
function reset() {
 const value = props.configuration?.current
 if (!value) return
 const object = value.kind === 's3' ? value : props.configuration?.objectStorage
 draft.value = { kind: value.kind, endpoint: object?.endpoint ?? '', bucket: object?.bucket ?? '', region: object?.region ?? '', prefix: object?.prefix ?? '', accessKey: '', secretKey: '', sessionToken: '' }
 revision.value = props.configuration!.revision
 dirty.value = false; pendingChange.value = false; conflict.value = false; checked.value = false; resetKey.value++
}
watch(() => props.configuration, value => {
 if (!value) return
 if (working.value) { if (value.revision !== revision.value) conflict.value = true; return }
 if (!revision.value || !dirty.value && !pendingChange.value) reset()
 else if (value.revision !== revision.value) conflict.value = true
}, { immediate: true })
watch(confirmation, value => emit('modal-open', value))
const schema = computed(() => ({
 type: 'object' as const, additionalProperties: false, required: ['kind'],
 properties: {
  kind: { type: 'string' as const, enum: ['local', 's3'], title: t('storageMethod') },
  endpoint: { type: 'string' as const, maxLength: 2048, title: t('storageEndpoint') },
  bucket: { type: 'string' as const, maxLength: 255, title: t('storageBucket') },
  region: { type: 'string' as const, maxLength: 255, title: t('storageRegion') },
  prefix: { type: 'string' as const, maxLength: 1024, title: t('storagePrefix') },
  accessKey: { type: 'string' as const, maxLength: 4096, writeOnly: true, title: t('storageAccessKey') },
  secretKey: { type: 'string' as const, maxLength: 4096, writeOnly: true, title: t('storageSecretKey') },
  sessionToken: { type: 'string' as const, maxLength: 16384, writeOnly: true, title: t('storageSessionToken') },
 },
 allOf: [{ if: { properties: { kind: { const: 's3' } } }, then: { required: ['endpoint', 'bucket'], properties: { endpoint: { minLength: 1, pattern: '^https?://' }, bucket: { minLength: 1 } } } }],
}))
const uischema = computed(() => ({ type: 'VerticalLayout', elements: [
 { type: 'Control', scope: '#/properties/kind', options: { enumLabels: { local: t('storageLocal'), s3: t('storageObject') } } },
 { type: 'VerticalLayout', rule: { effect: 'SHOW', condition: { scope: '#/properties/kind', schema: { const: 's3' } } }, elements: [
  ...['endpoint', 'bucket', 'region', 'prefix', 'accessKey', 'secretKey', 'sessionToken'].map(field => ({ type: 'Control', scope: `#/properties/${field}` })),
 ] },
] }))
async function act(action: 'check' | 'migrate' | 'cancel' | 'retry') {
 if (working.value) return
 working.value = true; failure.value = ''; checked.value = false
 try {
  const body = action === 'check' || action === 'migrate'
   ? { action, config: draft.value.kind === 'local' ? { kind: 'local' } : draft.value, revision: revision.value }
   : { action, migrationId: job.value?.id }
  const result = await client.request<ResourceStorageStatus>('/resource-storage', { method: 'POST', body: JSON.stringify(body), signal: controller.signal })
  if (disposed) return
  if (result.config) revision.value = result.config.revision
  draft.value = { ...draft.value, accessKey: '', secretKey: '', sessionToken: '' }
  pendingChange.value = action === 'check' || !!result.checkError
  resetKey.value++
  emit('changed', result)
  conflict.value = false
  if (result.checkError) failure.value = result.checkError
  else { confirmation.value = false; if (action === 'check') checked.value = true }
  if (action === 'migrate' && !result.checkError && !['syncing', 'failed', 'cancelling'].includes(result.config?.migration?.phase ?? '')) { pendingChange.value = false; dirty.value = false }

 } catch (error) { if (!disposed && !controller.signal.aborted) failure.value = String(error) }
 finally { working.value = false; if (!pendingChange.value && !dirty.value) reset() }
}
</script>

<template>
 <section class="storage-destination" :aria-label="t('storageMethod')">
  <p v-if="configuration" class="storage-current">{{ t('storageCurrent', { location: currentLabel }) }}</p>
  <template v-if="configuration">
   <SchemaForm id="resource-storage" v-model="draft" :schema="schema" :uischema="uischema" :disabled="disabled" :reset-key="resetKey" draft-updates @validity="valid = $event" @dirty="dirty = $event" />
   <p class="storage-hint">{{ t('storageScopeHint') }}</p>
   <p v-if="draft.kind === 's3'" class="storage-hint">{{ t('storageCredentialsHint') }}</p>
   <v-alert v-if="conflict" type="warning" :message="t('configEditorChanged')" />
   <div class="storage-destination-actions">
    <v-button v-if="!migrating" :disabled="disabled || !valid || conflict" @click="act('check')">{{ t('storageCheck') }}</v-button>
    <v-button v-if="!migrating" variant="primary" :disabled="disabled || !valid || conflict || !dirty && !pendingChange" @click="confirmation = true">{{ t('storageApply') }}</v-button>
    <ConfirmPopover v-if="conflict" popover-class="storage-destination-reload-popover" :show="reloadOpen" :message="t('configEditorDiscard')" :cancel-text="t('cancel')" :confirm-text="t('configEditorReload')" :busy="disabled" @update:show="reloadOpen = $event" @confirm="reset(); reloadOpen = false">
     <template #trigger><v-button :disabled="disabled">{{ t('configEditorReload') }}</v-button></template>
    </ConfirmPopover>
    <v-button v-if="job?.phase === 'failed'" :disabled="working" @click="act('retry')">{{ t('retry') }}</v-button>
    <v-button v-if="job && ['syncing', 'failed'].includes(job.phase)" :disabled="working" @click="act('cancel')">{{ t('cancel') }}</v-button>
   </div>
   <p v-if="checked" role="status">{{ t('storageCheckPassed') }}</p>
   <div v-if="job && migrating" class="storage-migration" role="status" aria-live="polite">
    <v-spin v-if="job.phase === 'syncing' || job.phase === 'cancelling'" />
    <span>{{ t(job.phase === 'cancelling' ? 'storageCancelling' : job.phase === 'failed' ? 'storageMigrationFailed' : 'storageMigrating', { count: job.copiedCount }) }}</span>
   </div>
   <v-alert v-if="failure" type="error" :message="failure" />
  </template>
 </section>
 <v-modal :open="confirmation" :title="t('storageApply')" :close-disabled="working" @cancel="!working && (confirmation = false)">
  <p>{{ t('storageMigrationConfirm') }}</p>
  <v-alert v-if="failure" type="error" :message="failure" />
  <template #footer>
   <v-button :disabled="working" @click="confirmation = false">{{ t('cancel') }}</v-button>
   <v-button variant="primary" :disabled="working" :loading="working" @click="act('migrate')">{{ t('storageApply') }}</v-button>
  </template>
 </v-modal>
</template>

<style scoped>
.storage-destination{display:flex;flex-direction:column;gap:12px;min-width:0}
.storage-current{margin:0;font-weight:600}
.storage-hint{margin:0;color:var(--text-secondary);line-height:1.65}
.storage-destination-actions{display:flex;flex-wrap:wrap;gap:10px}
.storage-destination-actions :deep(button){max-width:100%;white-space:normal;height:auto;min-height:var(--control-height)}
.storage-migration{display:flex;gap:10px;align-items:center}
</style>
