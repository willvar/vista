<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Spin as ASpin } from 'antdv-next'
import { DeleteOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import { formatDateTime } from './ui/datetime.ts'
import ConfirmPopover from './ConfirmPopover.vue'
import type { ResourceStorageStatus as Status } from './client/types'

const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const status = ref<Status>({ phase: 'idle', deletedCount: 0, deletedBytes: 0 })
const error = ref('')
const cleanOpen = ref(false)
const cleanMessage = computed(() => t('resourceStorageCleanConfirm', {
  files: t('resourceStorageCount', { count: status.value.stats?.cleanableCount || 0 }),
  size: size(status.value.stats?.cleanableBytes || 0),
}))
const submitting = ref(false)
const busy = computed(() => submitting.value || status.value.phase === 'running')
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
let initialScan: Promise<void> | undefined
const unsubscribe = stateSync.subscribe(['storage'], async (_, signal, snapshots) => {
  if (snapshots.storage) { revision++; accept(snapshots.storage) }
  else if (initialScan) await initialScan
  else await readStorage(signal, true)
})

async function start(action: 'scan' | 'clean') {
  if (busy.value) return
  if (action === 'scan') cleanOpen.value = false
  const request = ++revision
  submitting.value = true
  error.value = ''
  try {
    const result = await client.request<Status>('/resource-storage', { method: 'POST', body: JSON.stringify({ action }), signal: controller.signal })
    if (request === revision) accept(result)
    if (action === 'clean') cleanOpen.value = false
  }
  catch (failure) { if (!disposed) error.value = String(failure) }
  finally { submitting.value = false }
}
onMounted(() => { initialScan = start('scan').finally(() => { initialScan = undefined }) })
watch(cleanOpen, value => emit('modal-open', value), { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; controller.abort(); unsubscribe(); emit('modal-open', false) })
const cards = computed(() => [
  { label: 'resourceStorageTotal', bytes: status.value.stats?.totalBytes, count: status.value.stats?.totalCount },
  { label: 'resourceStorageReferenced', bytes: status.value.stats?.referencedBytes, count: status.value.stats?.referencedCount },
  { label: 'resourceStorageCleanable', bytes: status.value.stats?.cleanableBytes, count: status.value.stats?.cleanableCount },
])
defineExpose({ headerAction: computed(() => ({ label: 'resourceStorageRefresh', run: () => start('scan'), disabled: busy.value, loading: busy.value && status.value.action === 'scan' })) })
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
      <ConfirmPopover :show="cleanOpen" :message="cleanMessage" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" :error="error" popover-class="resource-clean-popover" @update:show="cleanOpen = $event; error = ''" @confirm="start('clean')">
        <template #trigger><a-button :disabled="busy || !status.stats?.cleanableCount"><template #icon><DeleteOutlined aria-hidden="true" /></template>{{ t('resourceStorageClean') }}</a-button></template>
      </ConfirmPopover>
    </div>
    <div class="storage-progress" role="status" aria-live="polite">
      <template v-if="busy"><a-spin size="small" /><span>{{ t(status.action === 'clean' ? 'resourceStorageCleaning' : 'resourceStorageScanning') }}<template v-if="status.deletedCount"> {{ t('resourceStorageFreed', { count: status.deletedCount, size: size(status.deletedBytes) }) }}</template></span></template>
      <span v-else-if="status.action === 'clean' && !error">{{ t('resourceStorageFreed', { count: status.deletedCount, size: size(status.deletedBytes) }) }}</span>
    </div>
    <small v-if="status.stats" class="storage-description">{{ t('resourceStorageScannedAt', { time: formatDateTime(status.stats.scannedAt, locale) }) }}</small>
    <a-alert v-if="error && !cleanOpen" type="error" show-icon :title="error" />
  </div>
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
@media(max-width:600px){.storage-cards{grid-template-columns:1fr}.storage-card{display:grid;grid-template-columns:1fr auto;align-items:center;padding:14px}.storage-card>strong{grid-column:2;grid-row:1/3}.storage-card>small{grid-column:1}}
</style>
