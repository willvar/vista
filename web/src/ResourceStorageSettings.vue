<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Spin as VSpin } from './ui'
import { DeleteOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import { formatDateTime } from './ui/datetime.ts'
import BatchOperationModal from './BatchOperationModal.vue'
import type { ResourceStorageStatus as Status } from './client/types'

const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const status = ref<Status>({ phase: 'idle', deletedCount: 0, deletedBytes: 0 })
const error = ref('')
const cleanOpen = ref(false)
const cleanPhase = ref<'confirm' | 'running' | 'complete'>('confirm')
const cleanFailure = ref('')
const cleanResult = ref('')
let cleanAccepted = false
const cleanMessage = computed(() => t('resourceStorageCleanConfirm', {
  files: t('resourceStorageCount', { count: status.value.stats?.cleanableCount || 0 }),
  size: size(status.value.stats?.cleanableBytes || 0),
}))
const submitting = ref(false)
const busy = computed(() => submitting.value || status.value.phase === 'running')
const cleanProgress = computed(() => status.value.deletedCount
  ? t('resourceStorageFreed', { count: status.value.deletedCount, size: size(status.value.deletedBytes) })
  : t('resourceStorageCleaning'))
const controller = new AbortController()
let revision = 0
let disposed = false
function size(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes / 1024, index = 0
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index++ }
  return `${value.toLocaleString(locale.value, { maximumFractionDigits: 1 })} ${units[index]}`
}
function accept(result: Status) {
  if (disposed) return
  status.value = result
  error.value = result.error || ''
  settleClean()
}
function settleClean() {
  if (!cleanOpen.value || cleanPhase.value !== 'running' || !cleanAccepted || busy.value) return
  cleanFailure.value = error.value
  cleanResult.value = t('resourceStorageFreed', { count: status.value.deletedCount, size: size(status.value.deletedBytes) })
  if (error.value) cleanPhase.value = 'complete'
  else cleanOpen.value = false
}
function showClean() {
  if (busy.value || !status.value.stats?.cleanableCount) return
  error.value = ''
  cleanFailure.value = ''
  cleanPhase.value = 'confirm'
  cleanOpen.value = true
}
function cancelClean() {
  if (cleanPhase.value !== 'running') cleanOpen.value = false
}
async function readStorage(signal = controller.signal, propagate = false) {
  const request = ++revision
  try {
    const result = await client.request<Status>('/resource-storage', { signal })
    if (!signal.aborted && request === revision) accept(result)
  } catch (failure) {
    if (!disposed && !signal.aborted && request === revision) error.value = String(failure)
    if (propagate) throw failure
  }
}
const unsubscribe = stateSync.subscribe(['storage'], async (_, signal, snapshots) => {
  if (snapshots.storage) { revision++; accept(snapshots.storage) }
  else await readStorage(signal, true)
})

async function start(action: 'scan' | 'clean') {
  if (busy.value) return
  if (action === 'scan') cleanOpen.value = false
  const request = ++revision
  submitting.value = true
  error.value = ''
  if (action === 'clean') { cleanAccepted = false; cleanPhase.value = 'running' }
  try {
    const result = await client.request<Status>('/resource-storage', { method: 'POST', body: JSON.stringify({ action }), signal: controller.signal })
    if (disposed) return
    if (request === revision) accept(result)
    if (action === 'clean') {
      cleanAccepted = result.action === 'clean'
      if (!cleanAccepted) {
        error.value = t('resourceStorageScanning')
        cleanPhase.value = 'confirm'
      }
    }
  }
  catch (failure) {
    if (!disposed) { error.value = String(failure); if (action === 'clean') cleanPhase.value = 'confirm' }
  }
  finally { submitting.value = false; if (!disposed) settleClean() }
}
watch(cleanOpen, value => emit('modal-open', value), { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; controller.abort(); unsubscribe(); emit('modal-open', false) })
const cards = computed(() => [
  { label: 'resourceStorageTotal', bytes: status.value.stats?.totalBytes, count: status.value.stats?.totalCount },
  { label: 'resourceStorageReferenced', bytes: status.value.stats?.referencedBytes, count: status.value.stats?.referencedCount },
  { label: 'resourceStorageCleanable', bytes: status.value.stats?.cleanableBytes, count: status.value.stats?.cleanableCount },
])
</script>

<template>
  <div class="settings-content resource-storage-settings">
    <div><h3>{{ t('resourceStorageTitle') }}</h3><p class="storage-description">{{ t('resourceStorageHint') }}</p></div>
    <div class="storage-cards">
      <div v-for="card in cards" :key="card.label" class="storage-card">
        <span>{{ t(card.label) }}</span><strong>{{ card.bytes === undefined ? '—' : size(card.bytes) }}</strong>
        <small>{{ card.count === undefined ? '—' : t('resourceStorageCount', { count: card.count }) }}</small>
      </div>
    </div>
    <p class="storage-description">{{ t('resourceStorageRetention') }}</p>
    <div class="storage-actions">
      <v-button :disabled="busy || !status.stats?.cleanableCount" @click="showClean"><template #icon><DeleteOutlined aria-hidden="true" /></template>{{ t('resourceStorageClean') }}</v-button>
    </div>
    <div v-if="!cleanOpen" class="storage-progress" role="status" aria-live="polite">
      <template v-if="busy"><v-spin /><span>{{ t(status.action === 'clean' ? 'resourceStorageCleaning' : 'resourceStorageScanning') }}<template v-if="status.action === 'clean' && status.deletedCount"> {{ t('resourceStorageFreed', { count: status.deletedCount, size: size(status.deletedBytes) }) }}</template></span></template>
      <span v-else-if="(status.action === 'clean' || status.deletedCount) && !error">{{ t('resourceStorageFreed', { count: status.deletedCount, size: size(status.deletedBytes) }) }}</span>
    </div>
    <small v-if="status.stats" class="storage-description">{{ t('resourceStorageScannedAt', { time: formatDateTime(status.stats.scannedAt, locale) }) }}</small>
    <v-alert v-if="error && !cleanOpen" type="error" show-icon :message="error" />
    <v-button v-if="error && !cleanOpen" :disabled="busy" @click="start('scan')">{{ t('retry') }}</v-button>
  </div>
  <BatchOperationModal :open="cleanOpen" :title="t('resourceStorageClean')" :phase="cleanPhase"
    :total="status.stats?.cleanableCount ?? 0" :progress-label="cleanPhase === 'complete' ? cleanResult : cleanProgress" :confirm-text="t('delete')" danger
    :confirm-disabled="busy" :error="cleanFailure" @confirm="start('clean')" @cancel="cancelClean">
    <template #confirmation><p class="clean-confirmation">{{ cleanMessage }}</p><v-alert v-if="error" type="error" show-icon :message="error" /></template>
  </BatchOperationModal>
</template>

<style scoped>
.resource-storage-settings{display:flex;flex-direction:column;gap:20px}
.resource-storage-settings h3{margin:0 0 8px;font-size:18px}
.storage-description{margin:0;color:var(--text-secondary);line-height:1.65}
.storage-cards{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}
.storage-card{display:flex;flex-direction:column;gap:10px;min-width:0;padding:18px;border:1px solid var(--border);border-radius:12px;background:var(--surface)}
.storage-card>span,.storage-card>small{color:var(--text-secondary)}
.storage-card>small,small.storage-description{font-size:var(--font-size-small)}
.storage-card>strong{font-size:24px;font-weight:bold;overflow-wrap:anywhere}
.storage-actions{display:flex;flex-wrap:wrap;gap:10px}
.storage-progress{display:flex;align-items:center;gap:10px;min-height:24px;color:var(--text-secondary)}
.clean-confirmation{margin:0 0 1rem;overflow-wrap:anywhere}
@media(max-width:600px){.storage-cards{grid-template-columns:1fr}.storage-card{display:grid;grid-template-columns:1fr auto;align-items:center;padding:14px}.storage-card>strong{grid-column:2;grid-row:1/3}.storage-card>small{grid-column:1}}
</style>
