<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeftOutlined } from '@antdv-next/icons'
import { Alert as AAlert, Button as AButton, Checkbox as ACheckbox, Input as AInput, Modal as AModal, Select as ASelect } from 'antdv-next'
import WorkspaceModal from '../WorkspaceModal.vue'
import ConfirmPopover from '../ConfirmPopover.vue'
import SessionIndicators from '../SessionIndicators.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { formatDateTime } from '../ui/datetime.ts'

const router = useRouter()
const { t, locale } = useI18n({ useScope: 'global' })
const {
  sessions, openSessions, closedSessions, serverStatus, busy, debugMode,
  backToWorkspace, action, beginRename, cancelRename, saveRename,
  renamingSessionID, sessionTitleDraft, deleteTarget, updateSessionDelete, confirmDelete,
  workerAvailable,
  client, refresh, route, selected, conversation,
} = useWorkspace()
const sessionSearch = ref('')
const sessionFilter = ref<'all' | 'open' | 'closed'>('all')
const managedSessions = computed(() => sessions.value.filter(session =>
  (sessionFilter.value === 'all' || session.open === (sessionFilter.value === 'open'))
  && session.title.toLocaleLowerCase().includes(sessionSearch.value.trim().toLocaleLowerCase())))
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
async function batchAction(kind: 'open' | 'close' | 'delete') {
  if (busy.value) return
  const targets = (kind === 'open' ? selectedClosed.value : kind === 'close' ? selectedOpen.value : selectedSessions.value).slice()
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
        batchFailures.value.push({ id: item.id, title: item.title, message: cause instanceof Error ? cause.message : String(cause) })
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
      await refresh()
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
<WorkspaceModal :open="true" :width="{ xs: 'calc(100vw - 2rem)', sm: 760, xl: 960 }" :keyboard="!deleteTarget && !batchDeleteOpen && !busy" @cancel="!busy && !batchDeleteOpen && backToWorkspace()"><section class="workspace session-manager-page modal-page">
        <div class="conversation-header"><a-button type="text" class="page-back" :aria-label="t('backToConversation')" :disabled="busy" @click="backToWorkspace"><ArrowLeftOutlined /></a-button><div class="page-heading"><small>{{ t('sessionManagerHeading') }}</small><h2>{{ t('manageSessions') }}</h2></div><a-button type="text" class="modal-close icon-close" :aria-label="t('closeSessionManager')" :disabled="busy" @click="backToWorkspace">×</a-button></div>
        <div class="session-manager-content">
          <p>{{ t('sessionManagerHint') }}</p>
          <div class="session-manager-stats" :class="{ 'debug-mode': debugMode }"><div><strong>{{ sessions.length }}</strong><small>{{ t('allSessions') }}</small></div><div><strong>{{ openSessions.length }}</strong><small>{{ t('openSessions') }}</small></div><div><strong>{{ closedSessions }}</strong><small>{{ t('closedSessions') }}</small></div><div v-if="debugMode"><strong>{{ serverStatus?.sessions?.workers ?? 0 }}</strong><small>{{ t('workers') }}</small></div></div>
          <div class="session-manager-controls"><a-input v-model:value="sessionSearch" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')" :disabled="busy" allow-clear /><a-select v-model:value="sessionFilter" :options="[{ label: t('allSessions'), value: 'all' }, { label: t('openSessions'), value: 'open' }, { label: t('closedSessions'), value: 'closed' }]" :aria-label="t('sessionFilter')" :disabled="busy" /></div>
          <p class="session-manager-count">{{ t('sessionManagerCount', { shown: managedSessions.length, total: sessions.length }) }}</p>
          <div v-if="managedSessions.length" class="session-bulk-actions">
            <a-checkbox :checked="allVisibleSelected" :indeterminate="selectedSessions.length > 0 && !allVisibleSelected" :disabled="busy" @update:checked="selectAllVisible">{{ t('selectAllVisible') }}</a-checkbox>
            <span role="status">{{ t('selectedSessionCount', { count: selectedSessions.length }) }}</span>
            <a-button v-if="selectedSessions.length" type="text" size="small" :disabled="busy" @click="selectAllVisible(false)">{{ t('clearSelection') }}</a-button>
            <a-button size="small" :disabled="busy || !selectedClosed.length" @click="batchAction('open')">{{ t('openSelected') }} ({{ selectedClosed.length }})</a-button>
            <a-button size="small" :disabled="busy || !selectedOpen.length" @click="batchAction('close')">{{ t('closeSelected') }} ({{ selectedOpen.length }})</a-button>
            <a-button size="small" danger :disabled="busy || !selectedSessions.length" @click="batchDeleteOpen = true">{{ t('deleteSelected') }}</a-button>
          </div>
          <a-alert v-if="batchFailures.length" type="error" show-icon :message="t('batchSessionFailures', { count: batchFailures.length })" class="session-batch-error">
            <template #description><ul><li v-for="failure in batchFailures" :key="failure.id">{{ failure.title }}: {{ failure.message }}</li></ul></template>
          </a-alert>
          <p v-if="!managedSessions.length" class="session-manager-empty">{{ t(sessions.length ? 'noMatchingSessions' : 'noSessionsYet') }}</p>
          <div v-for="item in managedSessions" :key="item.id" class="managed-session" :class="{ 'managed-session-selected': selectedIDs.has(item.id) }"><div class="managed-session-main"><a-checkbox class="managed-session-selection" :checked="selectedIDs.has(item.id)" :aria-label="t('selectSession', { title: item.title })" :disabled="busy" @update:checked="selectSession(item.id, $event)" /><div class="managed-session-info"><form v-if="renamingSessionID === item.id" class="session-rename" @submit.prevent="saveRename(item)"><a-input v-model:value="sessionTitleDraft" :maxlength="200" :aria-label="t('sessionTitle')" /><a-button html-type="submit" size="small" type="primary" :disabled="busy || !sessionTitleDraft.trim()">{{ t('saveChanges') }}</a-button><a-button size="small" @click="cancelRename">{{ t('cancel') }}</a-button></form><template v-else><a-button v-if="item.open" type="text" class="session-name" :disabled="busy" @click="router.push(`/sessions/${item.id}`)">{{ item.title }}</a-button><strong v-else>{{ item.title }}</strong></template><small>{{ item.open ? t('open') : t('closed') }} · {{ localTime(item.createdAt) }}</small><SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" /></div></div><div class="actions"><a-button size="small" :disabled="busy" @click="beginRename(item)">{{ t('editTitle') }}</a-button><a-button v-if="item.open" size="small" :disabled="busy" @click="action(item,'close')">{{ t('close') }}</a-button><a-button v-else size="small" type="primary" :disabled="busy" @click="action(item,'open')">{{ t('open') }}</a-button><a-button v-if="item.open && !workerAvailable(item.id)" size="small" :disabled="busy" @click="action(item,'retry')">{{ t('retryWorker') }}</a-button><confirm-popover :show="deleteTarget?.id === item.id" :message="t('deleteSessionConfirm', { title: item.title })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" popover-class="session-delete-popover" @update:show="updateSessionDelete($event, item)" @confirm="confirmDelete"><template #trigger><a-button size="small" danger :disabled="busy">{{ t('delete') }}</a-button></template></confirm-popover></div></div>
        </div>
      </section></WorkspaceModal>
<a-modal :open="batchDeleteOpen" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
  :title="t('deleteSelected')" :ok-text="t('delete')" :cancel-text="t('cancel')"
  :confirm-loading="busy" :ok-button-props="{ danger: true, disabled: busy || !selectedSessions.length }"
  :cancel-button-props="{ disabled: busy }" :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
  @cancel="cancelBatchDelete" @ok="batchAction('delete')">
  <p class="batch-delete-confirmation">{{ t('deleteSelectedSessionsConfirm', { count: selectedSessions.length }) }}</p>
</a-modal>
</template>

<style scoped>
.managed-session-main { display: flex; align-items: center; gap: .75rem; flex: 1; min-width: 0; }
.managed-session-info { flex: 1; }
.managed-session-selected { background: var(--selected); }
.session-batch-error { margin: .75rem 0; overflow-wrap: anywhere; }
.session-batch-error ul { margin: .35rem 0 0; padding-left: 1.25rem; }
.batch-delete-confirmation { margin: 0; overflow-wrap: anywhere; }
</style>
