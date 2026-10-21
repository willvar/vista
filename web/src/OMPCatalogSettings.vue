<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Modal as AModal } from 'antdv-next'
import { HistoryOutlined } from '@antdv-next/icons'
import type { VistaClient } from './client/vista'
import type { OMPCatalogInfo } from './client/types'
import { formatDateTime } from './ui/datetime.ts'

const props = defineProps<{ client: VistaClient }>()
const emit = defineEmits<{ updated: [] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const catalog = ref<OMPCatalogInfo>()
const loading = ref(true)
const updating = ref(false)
const historyOpen = ref(false)
const requestError = ref('')
const checking = computed(() => updating.value || !!catalog.value?.updating)
const error = computed(() => requestError.value || (checking.value ? '' : catalog.value?.checkError))
const controller = new AbortController()
let poll: ReturnType<typeof setInterval> | undefined
let reading = false
let generation = 0
onUnmounted(() => { controller.abort(); clearInterval(poll) })

function receive(info: OMPCatalogInfo) {
  const previous = catalog.value
  catalog.value = info
  requestError.value = ''
  if (previous && info.checkedAt && info.checkedAt !== previous.checkedAt && !info.updating && !info.checkError) emit('updated')
}

async function readCatalog() {
  if (reading || updating.value || controller.signal.aborted) return
  reading = true
  const requestGeneration = generation
  try {
    const info = await props.client.request<OMPCatalogInfo>('/model-catalog', { signal: controller.signal })
    if (!controller.signal.aborted && requestGeneration === generation) receive(info)
  } catch (cause) {
    if (!controller.signal.aborted && requestGeneration === generation) requestError.value = cause instanceof Error ? cause.message : String(cause)
  } finally { reading = false; loading.value = false }
}

onMounted(() => {
  void readCatalog()
  poll = setInterval(() => { void readCatalog() }, 5000)
})

async function update() {
  if (loading.value || checking.value) return
  generation++
  updating.value = true
  requestError.value = ''
  try {
    const info = await props.client.request<OMPCatalogInfo>('/model-catalog', { method: 'POST', signal: controller.signal })
    if (controller.signal.aborted) return
    receive(info)
  } catch (cause) {
    if (!controller.signal.aborted) {
      requestError.value = cause instanceof Error ? cause.message : String(cause)
      // The check's persisted failure includes its next automatic retry time.
      updating.value = false
      void readCatalog()
    }
  }
  finally { updating.value = false }
}

function updatedAt(value: string) { return formatDateTime(value, locale.value) }
function openHistory() { historyOpen.value = true; void readCatalog() }
defineExpose({ headerAction: computed(() => ({ label: checking.value ? 'modelCatalogUpdating' : 'modelCatalogCheck', run: update, disabled: loading.value || checking.value, loading: checking.value })) })
</script>

<template>
  <section class="omp-catalog-setting" :aria-label="t('modelCatalog')" :aria-busy="loading || checking">
    <div class="omp-catalog-heading">
      <div>
        <strong>{{ t('modelCatalog') }}</strong>
        <small v-if="catalog">{{ t('modelCatalogCount', { count: catalog.modelCount.toLocaleString(locale) }) }} · OMP <span :title="catalog.revision">{{ catalog.revision.slice(0, 7) }}</span><template v-if="catalog.modelsDevRevision"> · models.dev <span :title="catalog.modelsDevRevision">{{ catalog.modelsDevRevision.slice(0, 7) }}</span></template></small>
      </div>
      <div class="omp-catalog-actions">
        <a-button type="text" size="small" :disabled="loading" @click="openHistory"><HistoryOutlined aria-hidden="true" />{{ t('modelCatalogHistory') }}</a-button>
      </div>
    </div>
    <small v-if="catalog" class="omp-catalog-updated">{{ catalog.updatedAt ? t('modelCatalogUpdatedAt', { time: updatedAt(catalog.updatedAt) }) : t('modelCatalogBundled') }}</small>
    <a-alert v-if="error" type="error" role="alert" :message="error" />
  </section>
  <a-modal v-model:open="historyOpen" centered :z-index="1200" width="min(640px, calc(100vw - 2rem))" :title="t('modelCatalogHistory')" :footer="null" :styles="{ body: { maxHeight: '70dvh', overflowY: 'auto' } }">
    <div class="omp-catalog-history">
      <div class="omp-catalog-history-summary">
        <p>{{ t('modelCatalogHint') }}</p>
        <small v-if="catalog?.checkedAt">{{ t('modelCatalogCheckedAt', { time: updatedAt(catalog.checkedAt) }) }}</small>
        <small v-if="catalog?.nextCheckAt">{{ t('modelCatalogNextCheckAt', { time: updatedAt(catalog.nextCheckAt) }) }}</small>
        <p>{{ t('modelCatalogSchedule') }}</p>
        <small>{{ t('modelCatalogHistoryHint') }}</small>
      </div>
      <ol v-if="catalog?.history?.length" class="omp-catalog-history-list">
        <li v-for="record in catalog.history" :key="record.checkedAt" class="omp-catalog-history-record">
          <div class="omp-catalog-history-heading">
            <time :datetime="record.checkedAt">{{ updatedAt(record.checkedAt) }}</time>
            <span class="omp-catalog-outcome" :class="{ 'is-failed': record.error, 'is-changed': record.changed && !record.error }">{{ t(record.error ? 'modelCatalogFailed' : record.changed ? 'modelCatalogChanged' : 'modelCatalogUnchanged') }}</span>
          </div>
          <small>{{ t(record.automatic ? 'modelCatalogAutomaticCheck' : 'modelCatalogManualCheck') }} · {{ t('modelCatalogCount', { count: record.modelCount.toLocaleString(locale) }) }}</small>
          <small class="omp-catalog-history-versions">OMP <span :title="record.revision">{{ record.revision.slice(0, 7) }}</span><template v-if="record.modelsDevRevision"> · models.dev <span :title="record.modelsDevRevision">{{ record.modelsDevRevision.slice(0, 7) }}</span></template></small>
          <p v-if="record.error" class="omp-catalog-history-error">{{ record.error }}</p>
        </li>
      </ol>
      <p v-else class="omp-catalog-history-empty">{{ t('modelCatalogHistoryEmpty') }}</p>
    </div>
  </a-modal>
</template>

<style scoped>
.omp-catalog-setting { padding: 1rem 0; margin-bottom: .5rem; border-bottom: 1px solid var(--border); }
.omp-catalog-heading { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: .75rem; }
.omp-catalog-heading>div { display: flex; flex-direction: column; gap: .25rem; min-width: 0; }
.omp-catalog-heading>div:first-child { flex: 1 1 12rem; }
.omp-catalog-heading .omp-catalog-actions { flex-direction: row; flex-shrink: 0; gap: .5rem; margin-left: auto; }
.omp-catalog-heading .ant-btn { flex-shrink: 0; }
.omp-catalog-setting p { margin: .5rem 0; color: var(--muted); font-size: var(--font-size-body); line-height: 1.5; }
.omp-catalog-setting small { color: var(--muted); font-size: var(--font-size-small); }
.omp-catalog-updated { display: block; margin-top: .5rem; }
.omp-catalog-setting .ant-alert { margin-top: .75rem; }
.omp-catalog-setting :deep(.ant-alert-title) { white-space: pre-wrap; overflow-wrap: anywhere; }
.omp-catalog-history { color: var(--text); }
.omp-catalog-history-summary { display: flex; flex-direction: column; gap: .35rem; padding-bottom: 1rem; }
.omp-catalog-history-summary p { margin: 0 0 .35rem; color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; }
.omp-catalog-history small { color: var(--muted); font-size: var(--font-size-small); line-height: 1.5; }
.omp-catalog-history-list { list-style: none; margin: 0; padding: 0; }
.omp-catalog-history-record { display: flex; flex-direction: column; gap: .3rem; padding: 1rem 0; border-top: 1px solid var(--border); }
.omp-catalog-history-heading { display: flex; align-items: baseline; justify-content: space-between; gap: .75rem; font-size: var(--font-size-body); }
.omp-catalog-outcome { color: var(--muted); flex-shrink: 0; }
.omp-catalog-outcome.is-changed { color: var(--accent); }
.omp-catalog-outcome.is-failed, .omp-catalog-history-error { color: var(--danger); }
.omp-catalog-history-error { margin: .25rem 0 0; white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--font-size-body); line-height: 1.6; }
.omp-catalog-history-empty { margin: 0; padding: 1.5rem 0; border-top: 1px solid var(--border); color: var(--muted); text-align: center; font-size: var(--font-size-body); }
@media (max-width: 420px) { .omp-catalog-heading { align-items: flex-start; } }
</style>
