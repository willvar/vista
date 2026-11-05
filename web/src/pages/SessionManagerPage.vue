<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeftOutlined } from '@antdv-next/icons'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Checkbox as VCheckbox, Input as VInput, Modal as VModal, Select as VSelect } from '../ui'
import WorkspaceModal from '../WorkspaceModal.vue'
import ConfirmPopover from '../ConfirmPopover.vue'
import SessionIndicators from '../SessionIndicators.vue'
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
const { data: sessionPage, loading: archiveLoading, error: archiveError, ready } = usePagedList<SessionPage>({
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
const managedSessions = computed(() => editingRows.value ?? (ready.value ? sessionPage.value?.items ?? [] : []))
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
const batchDeleteOpen = ref(false)
const batchFailures = ref<Array<{ id: string; title: string; message: string }>>([])
watch(() => managedSessions.value.map(item => item.id), ids => {
  const visible = new Set(ids)
  selectedIDs.value = new Set([...selectedIDs.value].filter(id => visible.has(id)))
  if (!selectedIDs.value.size) batchDeleteOpen.value = false
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
function cancelBatchDelete() {
  if (!busy.value) batchDeleteOpen.value = false
}
function closeSelected() {
  const targets = selectedOpen.value.slice()
  return requestSessionClose(targets, () => batchAction('close', targets))
}
async function batchAction(kind: 'open' | 'close' | 'delete', targets = (kind === 'open' ? selectedClosed.value : kind === 'close' ? selectedOpen.value : selectedSessions.value).slice()) {
  if (busy.value) return
  if (!targets.length) return
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
      }
    }
    selectedIDs.value = new Set([...selectedIDs.value].filter(id => !succeeded.has(id)))
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
    batchDeleteOpen.value = false
    busy.value = false
  }
}
function localTime(utc: string) {
  return formatDateTime(utc, locale.value)
}
</script>

<template>
<WorkspaceModal :open="true" width="min(960px, calc(100vw - 2rem))" :keyboard="!deleteTarget && !batchDeleteOpen && !closeConfirmation && !busy" @cancel="!busy && !batchDeleteOpen && !closeConfirmation && backToWorkspace()"><section class="workspace session-manager-page modal-page">
        <div class="conversation-header"><v-button variant="text" class="page-back" :aria-label="t('backToConversation')" :disabled="busy" @click="backToWorkspace"><ArrowLeftOutlined /></v-button><div class="page-heading"><small>{{ t('sessionManagerHeading') }}</small><h2>{{ t('manageSessions') }}</h2></div><v-button variant="text" class="modal-close icon-close" :aria-label="t('closeSessionManager')" :disabled="busy" @click="backToWorkspace">×</v-button></div>
        <div class="session-manager-content">
          <p>{{ t('sessionManagerHint') }}</p>
          <div class="session-manager-stats" :class="{ 'debug-mode': debugMode }"><div><strong>{{ totalSessions }}</strong><small>{{ t('allSessions') }}</small></div><div><strong>{{ serverStatus?.sessions.open ?? openSessions.length }}</strong><small>{{ t('openSessions') }}</small></div><div><strong>{{ closedSessions }}</strong><small>{{ t('closedSessions') }}</small></div><div v-if="debugMode"><strong>{{ serverStatus?.sessions?.workers ?? 0 }}</strong><small>{{ t('workers') }}</small></div></div>
          <div class="session-manager-controls"><v-input v-model:value="sessionSearch" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')" :disabled="busy || !!renamingSessionID" allow-clear /><v-select v-model:value="sessionFilter" :options="[{ label: t('allSessions'), value: 'all' }, { label: t('openSessions'), value: 'open' }, { label: t('closedSessions'), value: 'closed' }]" :aria-label="t('sessionFilter')" :disabled="busy || !!renamingSessionID" /></div>
          <v-alert v-if="archiveError" type="error" show-icon :message="archiveError" />
          <p v-else-if="archiveLoading" role="status">{{ t('loadingSessions') }}</p>
          <div v-if="managedSessions.length" class="session-bulk-actions">
            <v-checkbox :checked="allVisibleSelected" :indeterminate="selectedSessions.length > 0 && !allVisibleSelected" :disabled="busy" @update:checked="selectAllVisible">{{ t('selectAllVisible') }}</v-checkbox>
            <span role="status">{{ t('selectedSessionCount', { count: selectedSessions.length }) }}</span>
            <v-button v-if="selectedSessions.length" variant="text" :disabled="busy" @click="selectAllVisible(false)">{{ t('clearSelection') }}</v-button>
            <v-button :disabled="busy || !selectedClosed.length" @click="batchAction('open')">{{ t('openSelected') }} ({{ selectedClosed.length }})</v-button>
            <v-button :disabled="busy || !selectedOpen.length" @click="closeSelected">{{ t('closeSelected') }} ({{ selectedOpen.length }})</v-button>
            <v-button danger :disabled="busy || !selectedSessions.length" @click="batchDeleteOpen = true">{{ t('deleteSelected') }}</v-button>
          </div>
          <v-alert v-if="batchFailures.length" type="error" show-icon :message="t('batchSessionFailures', { count: batchFailures.length })" class="session-batch-error">
            <template #description><ul><li v-for="failure in batchFailures" :key="failure.id">{{ failure.title }}: {{ failure.message }}</li></ul></template>
          </v-alert>
          <v-empty v-if="!managedSessions.length && !archiveLoading && !archiveError" class="vista-empty-state session-manager-empty" :description="t(totalSessions ? 'noMatchingSessions' : 'noSessionsYet')" role="status" />
          <div v-for="item in managedSessions" :key="item.id" class="managed-session" :class="{ 'managed-session-selected': selectedIDs.has(item.id) }"><div class="managed-session-main"><v-checkbox class="managed-session-selection" :checked="selectedIDs.has(item.id)" :aria-label="t('selectSession', { title: displaySessionTitle(item) })" :disabled="busy" @update:checked="selectSession(item.id, $event)" /><div class="managed-session-info"><form v-if="renamingSessionID === item.id" class="session-rename" @submit.prevent="saveRename(item)"><v-input v-model:value="sessionTitleDraft" :maxlength="200" :aria-label="t('sessionTitle')" /><v-button type="submit" variant="primary" :disabled="busy || !sessionTitleDraft.trim()">{{ t('saveChanges') }}</v-button><v-button @click="cancelRename">{{ t('cancel') }}</v-button></form><template v-else><v-button v-if="item.open" variant="text" class="session-name" :disabled="busy" @click="router.push(`/sessions/${item.id}`)">{{ displaySessionTitle(item) }}</v-button><strong v-else>{{ displaySessionTitle(item) }}</strong></template><small>{{ item.open ? t('open') : t('closed') }} · {{ localTime(item.createdAt) }}</small><SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" /></div></div><div class="actions"><v-button :disabled="busy" @click="beginRename(item)">{{ t('editTitle') }}</v-button><v-button v-if="item.open" :disabled="busy" @click="action(item,'close')">{{ t('close') }}</v-button><v-button v-else variant="primary" :disabled="busy" @click="action(item,'open')">{{ t('open') }}</v-button><v-button v-if="item.open && !workerAvailable(item.id)" :disabled="busy" @click="action(item,'retry')">{{ t('retryWorker') }}</v-button><confirm-popover :show="deleteTarget?.id === item.id" :message="t('deleteSessionConfirm', { title: displaySessionTitle(item) })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" popover-class="session-delete-popover" @update:show="updateSessionDelete($event, item)" @confirm="confirmDelete"><template #trigger><v-button danger :disabled="busy">{{ t('delete') }}</v-button></template></confirm-popover></div></div>
        </div>
        <ListPagination :page="page" :page-size="pageSize" :total="sessionPage?.total ?? 0" :disabled="busy || !!renamingSessionID" @change="changePage" />
      </section></WorkspaceModal>
<v-modal :open="batchDeleteOpen" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
  :title="t('deleteSelected')" :ok-text="t('delete')" :cancel-text="t('cancel')"
  :confirm-loading="busy" :ok-button-props="{ danger: true, disabled: busy || !selectedSessions.length }"
  :cancel-button-props="{ disabled: busy }" :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
  @cancel="cancelBatchDelete" @ok="batchAction('delete')">
  <p class="batch-delete-confirmation">{{ t('deleteSelectedSessionsConfirm', { count: selectedSessions.length }) }}</p>
</v-modal>
</template>

<style scoped>
.managed-session-main { display: flex; align-items: center; gap: .75rem; flex: 1; min-width: 0; }
.managed-session-info { flex: 1; }
.managed-session-selected { background: var(--selected); }
.session-batch-error { margin: .75rem 0; overflow-wrap: anywhere; }
.session-batch-error ul { margin: .35rem 0 0; padding-left: 1.25rem; }
.batch-delete-confirmation { margin: 0; overflow-wrap: anywhere; }
.session-manager-page>.list-pagination { padding: .75rem 28px; }
@media(max-width:830px){.session-manager-page>.list-pagination{padding:.6rem 1rem}}
</style>
