<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeftOutlined } from '@antdv-next/icons'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Checkbox as VCheckbox, Input as VInput, Select as VSelect } from '../ui'
import WorkspaceModal from '../WorkspaceModal.vue'
import BatchOperationModal from '../BatchOperationModal.vue'
import ConfirmPopover from '../ConfirmPopover.vue'
import SessionIndicators from '../SessionIndicators.vue'
import SessionPin from '../SessionPin.vue'
import ListPagination from '../ListPagination.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { usePagedList } from '../composables/usePagedList'
import { formatDateTime } from '../ui/datetime.ts'
import type { Session, SessionPage } from '../client/types'

const router = useRouter()
const { t, locale } = useI18n({ useScope: 'global' })
const {
  openSessions, closedSessions, totalSessions, serverStatus, busy, debugMode, stateSync,
  backToWorkspace, action, beginRename, cancelRename, saveRename, closeConfirmation, requestSessionClose,
  renamingSessionID, sessionTitleDraft, deleteTarget, updateSessionDelete, confirmDelete,
  workerAvailable, displaySessionTitle,
  client, route, selected, conversation,
} = useWorkspace()
const sessionSearch = ref('')
const searchQuery = ref('')
const sessionFilter = ref<'all' | 'open' | 'closed'>('all')
const page = ref(1)
const pageSize = ref(10)
const { data: sessionPage, loading: archiveLoading, error: archiveError, ready, cancel: cancelArchiveRead, refresh: refreshArchive } = usePagedList<SessionPage>({
  client, sync: stateSync, topic: 'sessions', page,
  path: current => {
    const query = new URLSearchParams({ page: String(current), pageSize: String(pageSize.value) })
    if (sessionFilter.value !== 'all') query.set('open', String(sessionFilter.value === 'open'))
    if (searchQuery.value) { query.set('search', searchQuery.value); query.set('untitledLabel', t('untitledSession')) }
    return `/sessions?${query}`
  },
})
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(sessionSearch, value => {
  clearTimeout(searchTimer)
  selectedIDs.value = new Set()
  searchTimer = setTimeout(() => { searchQuery.value = value.trim() }, 250)
})
watch([searchQuery, sessionFilter, pageSize], () => { page.value = 1 }, { flush: 'sync' })
onUnmounted(() => clearTimeout(searchTimer))
// Keep the active title editor mounted while remote updates replace the page.
const editingRows = ref<Session[] | null>(null)
const liveStats = computed(() => ({
  total: totalSessions.value,
  open: serverStatus.value?.sessions.open ?? openSessions.value.length,
  closed: closedSessions.value,
  workers: serverStatus.value?.sessions.workers ?? 0,
}))
// Freeze presentation only. Shared SSE and paginated data keep reconciling.
const batchView = shallowRef<{
  rows: Session[]; page: number; pageSize: number; total: number
  stats: typeof liveStats.value; workers: Map<string, boolean | undefined>
} | null>(null)
const batchCompleted = ref(0)
const managedSessions = computed(() => batchView.value?.rows ?? editingRows.value ?? (ready.value ? sessionPage.value?.items ?? [] : []))
const managedStats = computed(() => batchView.value?.stats ?? liveStats.value)
const managedPagination = computed(() => batchView.value ?? { page: page.value, pageSize: pageSize.value, total: sessionPage.value?.total ?? 0 })
function managedWorkerAvailable(id: string) {
  return batchView.value ? batchView.value.workers.get(id) : serverStatus.value ? workerAvailable(id) : undefined
}
watch(renamingSessionID, id => { editingRows.value = id ? managedSessions.value.slice() : null }, { flush: 'sync' })
function changePage(current: number, size: number) {
  selectedIDs.value = new Set()
  pageSize.value = size
  page.value = current
}
const selectedIDs = ref(new Set<string>())
const selectedSessions = computed(() => managedSessions.value.filter(item => selectedIDs.value.has(item.id)))
const selectedOpen = computed(() => selectedSessions.value.filter(item => item.open))
const selectedClosed = computed(() => selectedSessions.value.filter(item => !item.open))
const allVisibleSelected = computed(() => managedSessions.value.length > 0 && selectedSessions.value.length === managedSessions.value.length)
type BatchKind = 'open' | 'close' | 'delete'
type BatchRequest = { kind: BatchKind; targets: Session[]; confirmation?: string }
const batchRequest = shallowRef<BatchRequest | null>(null)
const batchOpen = ref(false)
const batchPhase = ref<'confirm' | 'running' | 'complete'>('confirm')
const batchFailures = ref<Array<{ id: string; title: string; message: string }>>([])
const batchTitle = computed(() => {
  const request = batchRequest.value
  if (batchPhase.value === 'confirm' && request?.kind === 'close') {
    return t(request.targets.length > 1 ? 'closeSelectedSessionsConfirmTitle' : 'closeSessionConfirmTitle', { count: request.targets.length })
  }
  return t(request?.kind === 'open' ? 'openSelected' : request?.kind === 'close' ? 'closeSelected' : 'deleteSelected')
})
watch(() => managedSessions.value.map(item => item.id), ids => {
  const visible = new Set(ids)
  selectedIDs.value = new Set([...selectedIDs.value].filter(id => visible.has(id)))
})
function selectSession(id: string, checked: boolean) {
  if (busy.value) return
  const next = new Set(selectedIDs.value)
  if (checked) next.add(id)
  else next.delete(id)
  selectedIDs.value = next
}
function selectAllVisible(checked: boolean) {
  if (busy.value) return
  selectedIDs.value = checked ? new Set(managedSessions.value.map(item => item.id)) : new Set()
}
function dismissBatch() {
  if (batchPhase.value !== 'running') batchOpen.value = false
}
function beginBatch(kind: BatchKind) {
  if (busy.value || batchOpen.value) return
  const targets = (kind === 'open' ? selectedClosed.value : kind === 'close' ? selectedOpen.value : selectedSessions.value).slice()
  if (!targets.length) return
  const request: BatchRequest = { kind, targets }
  const confirm = (message: string) => {
    batchRequest.value = { ...request, confirmation: message }
    batchPhase.value = 'confirm'
    batchOpen.value = true
  }
  if (kind === 'delete') confirm(t('deleteSelectedSessionsConfirm', { count: targets.length }))
  else if (kind === 'close') return requestSessionClose(targets, () => runBatch(request), () => confirm(t('closeSessionConfirmBody', { manager: t('manageSessions') })))
  else return runBatch(request)
}
async function runBatch(request = batchRequest.value) {
  if (busy.value) return
  if (!request?.targets.length) return
  const { kind, targets } = request
  const rows = managedSessions.value.slice()
  batchView.value = {
    rows, page: page.value, pageSize: pageSize.value, total: sessionPage.value?.total ?? 0,
    stats: liveStats.value,
    workers: new Map(rows.map(item => [item.id, serverStatus.value ? workerAvailable(item.id) : undefined])),
  }
  batchRequest.value = request
  batchCompleted.value = 0
  batchPhase.value = 'running'
  batchOpen.value = true
  busy.value = true
  batchFailures.value = []
  const succeeded = new Set<string>()
  try {
    for (const item of targets) {
      try {
        await client.request(`/sessions/${item.id}${kind === 'delete' ? '' : '/' + kind}`, { method: kind === 'delete' ? 'DELETE' : 'POST' })
        succeeded.add(item.id)
        if (kind === 'delete') try { localStorage.removeItem('vista-session-draft:' + item.id) } catch { /* Browser storage can be unavailable. */ }
      } catch (cause) {
        batchFailures.value.push({ id: item.id, title: displaySessionTitle(item), message: cause instanceof Error ? cause.message : String(cause) })
      } finally {
        batchCompleted.value++
      }
    }
    if (succeeded.size) {
      const activeID = selected.value?.id
      if (kind !== 'open' && activeID && succeeded.has(activeID)) conversation.clearSession()
      const routedID = typeof route.query.session === 'string' ? route.query.session : ''
      if (kind !== 'open' && succeeded.has(routedID)) {
        const query = { ...route.query }
        delete query.session
        await router.replace({ path: route.path, query })
      }
    }
  } finally {
    try {
      // Read after the last mutation, never reuse a read started before it.
      // The final SSE notification can arrive after the mutation response.
      cancelArchiveRead()
      await refreshArchive()
    } finally {
      selectedIDs.value = new Set([...selectedIDs.value].filter(id => !succeeded.has(id)))
      batchView.value = null
      busy.value = false
      if (batchFailures.value.length) batchPhase.value = 'complete'
      else batchOpen.value = false
    }
  }
}
function localTime(utc: string) {
  return formatDateTime(utc, locale.value)
}
</script>

<template>
<WorkspaceModal :open="true" width="min(960px, calc(100vw - 2rem))" :keyboard="!deleteTarget && !batchOpen && !closeConfirmation && !busy" @cancel="!busy && !batchOpen && !closeConfirmation && backToWorkspace()"><section class="workspace session-manager-page modal-page">
        <div class="conversation-header"><v-button variant="text" class="icon-button page-back" :aria-label="t('backToConversation')" :disabled="busy" @click="backToWorkspace"><ArrowLeftOutlined /></v-button><div class="page-heading"><small>{{ t('sessionManagerHeading') }}</small><h2>{{ t('manageSessions') }}</h2></div><v-button variant="text" class="icon-button modal-close icon-close" :aria-label="t('closeSessionManager')" :disabled="busy" @click="backToWorkspace">×</v-button></div>
        <div class="session-manager-content">
          <p>{{ t('sessionManagerHint') }}</p>
          <div class="session-manager-stats" :class="{ 'debug-mode': debugMode }"><div><strong>{{ managedStats.total }}</strong><small>{{ t('allSessions') }}</small></div><div><strong>{{ managedStats.open }}</strong><small>{{ t('openSessions') }}</small></div><div><strong>{{ managedStats.closed }}</strong><small>{{ t('closedSessions') }}</small></div><div v-if="debugMode"><strong>{{ managedStats.workers }}</strong><small>{{ t('workers') }}</small></div></div>
          <div class="session-manager-controls"><v-input v-model:value="sessionSearch" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')" :disabled="busy || !!renamingSessionID" allow-clear /><v-select v-model:value="sessionFilter" :options="[{ label: t('allSessions'), value: 'all' }, { label: t('openSessions'), value: 'open' }, { label: t('closedSessions'), value: 'closed' }]" :aria-label="t('sessionFilter')" :disabled="busy || !!renamingSessionID" /></div>
          <v-alert v-if="!batchView && archiveError" type="error" show-icon :message="archiveError" />
          <p v-else-if="!batchView && archiveLoading" role="status">{{ t('loadingSessions') }}</p>
          <div v-if="managedSessions.length" class="session-bulk-actions">
            <v-checkbox :checked="allVisibleSelected" :indeterminate="selectedSessions.length > 0 && !allVisibleSelected" :disabled="busy" @update:checked="selectAllVisible">{{ t('selectAllVisible') }}</v-checkbox>
            <span role="status">{{ t('selectedSessionCount', { count: selectedSessions.length }) }}</span>
            <v-button v-if="selectedSessions.length" variant="text" :disabled="busy" @click="selectAllVisible(false)">{{ t('clearSelection') }}</v-button>
            <v-button :disabled="busy || !selectedClosed.length" @click="beginBatch('open')">{{ t('openSelected') }} ({{ selectedClosed.length }})</v-button>
            <v-button :disabled="busy || !selectedOpen.length" @click="beginBatch('close')">{{ t('closeSelected') }} ({{ selectedOpen.length }})</v-button>
            <v-button danger :disabled="busy || !selectedSessions.length" @click="beginBatch('delete')">{{ t('deleteSelected') }}</v-button>
          </div>
          <v-empty v-if="!managedSessions.length && !archiveLoading && !archiveError" class="vista-empty-state session-manager-empty" :description="t(totalSessions ? 'noMatchingSessions' : 'noSessionsYet')" role="status" />
          <div v-for="item in managedSessions" :key="item.id" class="managed-session" :data-session-id="item.id" :class="{ 'managed-session-selected': selectedIDs.has(item.id) }"><div class="managed-session-main"><v-checkbox class="managed-session-selection" :checked="selectedIDs.has(item.id)" :aria-label="t('selectSession', { title: displaySessionTitle(item) })" :disabled="busy" @update:checked="selectSession(item.id, $event)" /><div class="managed-session-info"><form v-if="renamingSessionID === item.id" class="session-rename" @submit.prevent="saveRename(item)"><v-input v-model:value="sessionTitleDraft" :maxlength="200" :aria-label="t('sessionTitle')" /><v-button type="submit" variant="primary" :disabled="busy || !sessionTitleDraft.trim()">{{ t('saveChanges') }}</v-button><v-button @click="cancelRename">{{ t('cancel') }}</v-button></form><template v-else><v-button v-if="item.open" variant="text" class="session-name" :disabled="busy" @click="router.push(`/sessions/${item.id}`)">{{ displaySessionTitle(item) }}</v-button><strong v-else>{{ displaySessionTitle(item) }}</strong></template><small>{{ item.open ? t('open') : t('closed') }} · {{ localTime(item.createdAt) }}</small><SessionIndicators :session="item" :worker-available="managedWorkerAvailable(item.id)" /></div></div><div class="actions"><SessionPin :session="item" /><v-button :disabled="busy" @click="beginRename(item)">{{ t('editTitle') }}</v-button><v-button v-if="item.open" :disabled="busy" @click="action(item,'close')">{{ t('close') }}</v-button><v-button v-else variant="primary" :disabled="busy" @click="action(item,'open')">{{ t('open') }}</v-button><v-button v-if="item.open && managedWorkerAvailable(item.id) === false" :disabled="busy" @click="action(item,'retry')">{{ t('retryWorker') }}</v-button><confirm-popover :show="deleteTarget?.id === item.id" :message="t('deleteSessionConfirm', { title: displaySessionTitle(item) })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" popover-class="session-delete-popover" @update:show="updateSessionDelete($event, item)" @confirm="confirmDelete"><template #trigger><v-button danger :disabled="busy">{{ t('delete') }}</v-button></template></confirm-popover></div></div>
        </div>
        <ListPagination :page="managedPagination.page" :page-size="managedPagination.pageSize" :total="managedPagination.total" :disabled="busy || !!renamingSessionID" @change="changePage" />
      </section></WorkspaceModal>
<BatchOperationModal :open="batchOpen" :title="batchTitle"
  :phase="batchPhase" :total="batchRequest?.targets.length ?? 0" :completed="batchCompleted"
  :confirmation="batchRequest?.confirmation" :confirm-text="t(batchRequest?.kind === 'delete' ? 'delete' : 'closeAndStop')" danger
  :failures="batchFailures" :failure-summary="t('batchSessionFailures', { count: batchFailures.length })"
  @cancel="dismissBatch" @confirm="runBatch()" />
</template>

<style scoped>
.managed-session { flex-wrap: wrap; }
.managed-session-main { display: flex; align-items: center; gap: .75rem; flex: 1; min-width: min(100%, 18rem); }
.managed-session-info { flex: 1; }
.managed-session-selected { background: var(--selected); }
.session-manager-page>.list-pagination { padding: .75rem 28px; }
@media(max-width:830px){.session-manager-page>.list-pagination{padding:.6rem 1rem}}
</style>
