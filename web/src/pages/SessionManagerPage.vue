<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { RadioGroup } from '@ark-ui/vue/radio-group'
import { Menu } from '@ark-ui/vue/menu'
import { IconCheck, IconCheckbox, IconDots, IconPinFilled, IconSearch, IconSortAscending, IconSortDescending } from '@tabler/icons-vue'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Checkbox as VCheckbox, Dropdown as VDropdown, Input as VInput, Modal as VModal, Tooltip as VTooltip } from '../ui'
import WorkspaceModal from '../WorkspaceModal.vue'
import BatchOperationModal from '../BatchOperationModal.vue'
import InlineSessionTitle from '../InlineSessionTitle.vue'
import SessionIndicators from '../SessionIndicators.vue'
import SessionPin from '../SessionPin.vue'
import ListPagination from '../ListPagination.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { usePagedList } from '../composables/usePagedList'
import { formatDateTime } from '../ui/datetime.ts'
import type { Session, SessionPage } from '../client/types'

const router = useRouter()
const { t, n, locale } = useI18n({ useScope: 'global' })
const {
  openSessions, closedSessions, totalSessions, serverStatus, busy, debugMode, stateSync,
  backToWorkspace, action, closeConfirmation, requestSessionClose,
  workerAvailable, displaySessionTitle,
  client, route, selected, conversation,
} = useWorkspace()
const sessionSearch = ref('')
const searchQuery = ref('')
const sessionFilter = ref<'all' | 'open' | 'closed'>('all')
const sessionSort = ref('created-desc')
const sortOptions = computed(() => [
  { value: 'created-desc', label: t('sessionSortCreatedNewest') },
  { value: 'created-asc', label: t('sessionSortCreatedOldest') },
  { value: 'updated-desc', label: t('sessionSortUpdatedNewest') },
  { value: 'updated-asc', label: t('sessionSortUpdatedOldest') },
])
const sortTitle = computed(() => `${t('sortSessions')}: ${sortOptions.value.find(option => option.value === sessionSort.value)!.label}`)
const sortingByUpdate = computed(() => sessionSort.value.startsWith('updated-'))
const page = ref(1)
const pageSize = ref(10)
const selecting = ref(false)
const renamingSessionID = ref('')
const enteringSessionID = ref('')
const entryError = ref('')
const deleteTarget = shallowRef<Session | null>(null)
const deleteOpen = ref(false)
const { data: sessionPage, loading: archiveLoading, error: archiveError, ready, cancel: cancelArchiveRead, refresh: refreshArchive } = usePagedList<SessionPage>({
  client, sync: stateSync, topic: 'sessions', page,
  path: current => {
    const query = new URLSearchParams({ page: String(current), pageSize: String(pageSize.value) })
    if (sessionSort.value !== 'created-desc') query.set('sort', sessionSort.value)
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
watch([searchQuery, sessionFilter, sessionSort, pageSize], () => {
  selectedIDs.value = new Set()
  page.value = 1
}, { flush: 'sync' })
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
const filters = computed(() => [
  { value: 'all', label: t('sessionFilterAll'), accessibleLabel: t('allSessions'), count: managedStats.value.total },
  { value: 'open', label: t('sessionFilterOpen'), accessibleLabel: t('openSessions'), count: managedStats.value.open },
  { value: 'closed', label: t('sessionFilterClosed'), accessibleLabel: t('closedSessions'), count: managedStats.value.closed },
])
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
function toggleSelection() {
  if (busy.value || renamingSessionID.value) return
  selecting.value = !selecting.value
  selectedIDs.value = new Set()
}
function changeFilter(value: string | null) {
  if (value === 'all' || value === 'open' || value === 'closed') sessionFilter.value = value
}
function updateRenaming(id: string, editing: boolean) {
  if (editing) renamingSessionID.value = id
  else if (renamingSessionID.value === id) renamingSessionID.value = ''
}
async function enterSession(item: Session) {
  if (busy.value || selecting.value || renamingSessionID.value) return
  entryError.value = ''
  if (item.open) return router.push(`/sessions/${item.id}`)
  busy.value = true
  enteringSessionID.value = item.id
  try {
    await client.request(`/sessions/${item.id}/open`, { method: 'POST' })
    await router.push(`/sessions/${item.id}`)
  } catch (cause) {
    entryError.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    enteringSessionID.value = ''
    busy.value = false
  }
}
function sessionMenu(item: Session) {
  return {
    items: [
      { key: 'rename', label: t('editTitle') },
      ...(item.open ? [{ key: 'close', label: t('close') }] : []),
      ...(item.open && managedWorkerAvailable(item.id) === false ? [{ key: 'retry', label: t('retryWorker') }] : []),
      { key: 'delete', label: t('delete'), danger: true },
    ],
    onClick: ({ key }: { key: string }) => {
      if (busy.value || selecting.value || renamingSessionID.value) return
      if (key === 'rename') renamingSessionID.value = item.id
      else if (key === 'close' || key === 'retry') void action(item, key)
      else if (key === 'delete') { deleteTarget.value = item; deleteOpen.value = true }
    },
  }
}
async function deleteSession() {
  if (!deleteTarget.value || busy.value) return
  await action(deleteTarget.value, 'delete')
  deleteOpen.value = false
}
function closeManager() {
  if (!busy.value && !batchOpen.value && !closeConfirmation.value && !deleteOpen.value) backToWorkspace()
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
<WorkspaceModal :open="true" width="min(960px, calc(100vw - 2rem))" :title="t('manageSessions')" page-class="session-manager-page"
  :close-label="t('closeSessionManager')" :back-label="t('backToConversation')" :close-disabled="busy"
  :keyboard="!deleteOpen && !batchOpen && !closeConfirmation && !busy && !renamingSessionID" @cancel="closeManager" @back="closeManager">
    <div class="session-manager-toolbar">
      <div class="session-manager-search">
        <v-input v-model:value="sessionSearch" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')"
          :disabled="busy || !!renamingSessionID" allow-clear><template #prefix><IconSearch aria-hidden="true" /></template></v-input>
      </div>
      <div class="session-manager-controls">
        <RadioGroup.Root class="session-manager-filters" :model-value="sessionFilter" :aria-label="t('sessionFilter')"
          :disabled="busy || !!renamingSessionID" orientation="horizontal" @update:model-value="changeFilter">
          <RadioGroup.Item v-for="filter in filters" :key="filter.value" :value="filter.value" class="session-filter">
            <RadioGroup.ItemHiddenInput :aria-label="filter.accessibleLabel" :aria-labelledby="undefined" :aria-describedby="`session-filter-count-${filter.value}`" />
            <RadioGroup.ItemText class="session-filter-label">{{ filter.label }}<span :id="`session-filter-count-${filter.value}`" class="session-filter-count">{{ n(filter.count) }}</span></RadioGroup.ItemText>
          </RadioGroup.Item>
        </RadioGroup.Root>
        <small v-if="debugMode" class="session-manager-workers">{{ t('workers') }} · {{ n(managedStats.workers) }}</small>
      </div>
      <div class="session-manager-actions">
        <Menu.Root :positioning="{ placement: 'bottom-end', gutter: 6 }" lazy-mount unmount-on-exit>
          <Menu.Trigger as-child>
            <v-button variant="text" class="icon-button session-sort-trigger" :aria-label="t('sortSessions')" :title="sortTitle"
              :disabled="busy || !!renamingSessionID || !totalSessions">
              <IconSortAscending v-if="sessionSort.endsWith('-asc')" aria-hidden="true" /><IconSortDescending v-else aria-hidden="true" />
            </v-button>
          </Menu.Trigger>
          <Teleport to="body"><Menu.Positioner style="z-index: 1500">
            <Menu.Content class="ui-menu session-sort-menu" :aria-label="t('sortSessions')">
              <Menu.RadioItemGroup v-model="sessionSort">
                <Menu.RadioItem v-for="option in sortOptions" :key="option.value" :value="option.value" class="ui-menu-item">
                  <Menu.ItemText>{{ option.label }}</Menu.ItemText>
                  <Menu.ItemIndicator><IconCheck aria-hidden="true" /></Menu.ItemIndicator>
                </Menu.RadioItem>
              </Menu.RadioItemGroup>
            </Menu.Content>
          </Menu.Positioner></Teleport>
        </Menu.Root>
        <v-tooltip :title="t(selecting ? 'finishSessionSelection' : 'selectSessions')">
          <v-button variant="text" class="icon-button session-selection-toggle" :aria-label="t(selecting ? 'finishSessionSelection' : 'selectSessions')" :aria-pressed="selecting"
            :disabled="busy || !!renamingSessionID || !totalSessions" @click="toggleSelection">
            <IconCheck v-if="selecting" aria-hidden="true" /><IconCheckbox v-else aria-hidden="true" />
          </v-button>
        </v-tooltip>
      </div>
      <div v-if="selecting" class="session-selection-toolbar">
        <v-checkbox :checked="allVisibleSelected" :indeterminate="selectedSessions.length > 0 && !allVisibleSelected"
          :disabled="busy || !managedSessions.length" @update:checked="selectAllVisible">{{ t('selectAllVisible') }}</v-checkbox>
        <span role="status">{{ t('selectedSessionCount', { count: selectedSessions.length }) }}</span>
        <v-tooltip v-if="selectedSessions.length" :title="t('clearSelection')">
          <v-button variant="text" class="icon-button" :aria-label="t('clearSelection')" :disabled="busy" @click="selectAllVisible(false)">×</v-button>
        </v-tooltip>
      </div>
    </div>
    <div class="session-manager-content" :aria-busy="!batchView && archiveLoading">
      <v-alert v-if="entryError" type="error" show-icon :message="entryError" />
      <v-alert v-if="!batchView && archiveError" type="error" show-icon :message="archiveError" />
      <p v-else-if="!batchView && archiveLoading && !ready" class="session-manager-loading" role="status">{{ t('loadingSessions') }}</p>
      <v-empty v-if="!managedSessions.length && !archiveLoading && !archiveError" class="vista-empty-state session-manager-empty"
        :description="t(totalSessions ? 'noMatchingSessions' : 'noSessionsYet')" role="status" />
      <div v-for="item in managedSessions" :key="item.id" class="managed-session" :data-session-id="item.id"
        :class="{ 'managed-session-selected': selectedIDs.has(item.id), 'managed-session-selecting': selecting }">
        <v-checkbox v-if="selecting" class="managed-session-selection" :checked="selectedIDs.has(item.id)"
          :aria-label="t('selectSession', { title: displaySessionTitle(item) })" :disabled="busy" @update:checked="selectSession(item.id, $event)" />
        <div class="managed-session-info">
          <InlineSessionTitle :session="item" :editing="renamingSessionID === item.id" @update:editing="updateRenaming(item.id, $event)">
            <v-button v-if="!selecting" variant="text" class="managed-session-entry" :title="displaySessionTitle(item)" :aria-label="displaySessionTitle(item)"
              :disabled="busy || !!renamingSessionID" :loading="enteringSessionID === item.id" @click="enterSession(item)"><span class="session-name">{{ displaySessionTitle(item) }}</span></v-button>
            <strong v-else class="session-name" :title="displaySessionTitle(item)">{{ displaySessionTitle(item) }}</strong>
          </InlineSessionTitle>
          <div class="managed-session-meta"><time :datetime="sortingByUpdate ? item.updatedAt ?? item.createdAt : item.createdAt">{{ localTime(sortingByUpdate ? item.updatedAt ?? item.createdAt : item.createdAt) }}</time><template v-if="!item.open"><span aria-hidden="true">·</span><span>{{ t('closed') }}</span></template></div>
          <SessionIndicators :session="item" :worker-available="managedWorkerAvailable(item.id)" />
        </div>
        <div v-if="!selecting" class="managed-session-actions">
          <SessionPin :session="item" />
          <v-dropdown :menu="sessionMenu(item)">
            <v-button variant="text" class="icon-button session-menu" :aria-label="t('sessionMenu')" :title="t('sessionMenu')" :disabled="busy || !!renamingSessionID"><IconDots /></v-button>
          </v-dropdown>
        </div>
        <IconPinFilled v-else-if="item.pinned" class="managed-session-pin" aria-hidden="true" />
      </div>
    </div>
    <Transition name="session-batch-toolbar">
      <div v-if="selecting && selectedSessions.length" class="session-bulk-actions">
        <v-button v-if="selectedClosed.length" :disabled="busy" :aria-label="`${t('openSelected')} (${selectedClosed.length})`" @click="beginBatch('open')">{{ t('open') }} ({{ n(selectedClosed.length) }})</v-button>
        <v-button v-if="selectedOpen.length" :disabled="busy" :aria-label="`${t('closeSelected')} (${selectedOpen.length})`" @click="beginBatch('close')">{{ t('close') }} ({{ n(selectedOpen.length) }})</v-button>
        <v-button variant="text" danger :disabled="busy" :aria-label="t('deleteSelected')" @click="beginBatch('delete')">{{ t('delete') }}</v-button>
      </div>
    </Transition>
    <ListPagination :page="managedPagination.page" :page-size="managedPagination.pageSize" :total="managedPagination.total"
      :disabled="busy || !!renamingSessionID" @change="changePage" />
</WorkspaceModal>
<v-modal :open="deleteOpen" class="session-delete-modal" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
  :title="t('deleteSessionConfirmTitle')" :ok-text="t('delete')" :cancel-text="t('cancel')" :confirm-loading="busy"
  :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
  :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="!busy && (deleteOpen = false)" @ok="deleteSession">
  <p style="overflow-wrap: anywhere">{{ t('deleteSessionConfirm', { title: deleteTarget ? displaySessionTitle(deleteTarget) : '' }) }}</p>
</v-modal>
<BatchOperationModal :open="batchOpen" :title="batchTitle"
  :phase="batchPhase" :total="batchRequest?.targets.length ?? 0" :completed="batchCompleted"
  :confirmation="batchRequest?.confirmation" :confirm-text="t(batchRequest?.kind === 'delete' ? 'delete' : 'closeAndStop')" danger
  :failures="batchFailures" :failure-summary="t('batchSessionFailures', { count: batchFailures.length })"
  @cancel="dismissBatch" @confirm="runBatch()" />
</template>

<style scoped>
.session-manager-toolbar { display: flex; flex: none; flex-wrap: wrap; align-items: center; gap: 12px; padding: 16px 28px 12px; }
.session-manager-search { display: flex; flex: 1 1 200px; min-width: 0; }
.session-manager-search > .ui-input-affix-wrapper { flex: 1; min-width: 0; }
.session-manager-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; min-width: 0; }
.session-manager-actions { display: flex; flex: none; align-items: center; gap: 4px; }
.session-selection-toggle[aria-pressed=true] { color: var(--accent); background: var(--selected); }
.session-manager-filters { display: flex; gap: 4px; padding: 4px; border-radius: 10px; background: var(--surface-subtle); }
.session-filter { position: relative; display: flex; align-items: center; justify-content: center; min-height: var(--control-height); padding: 0 14px; border-radius: 7px; color: var(--muted); cursor: pointer; transition: background-color .16s, color .16s, box-shadow .16s; }
.session-filter :deep(input) { position: absolute; inset: 0; width: 100% !important; height: 100% !important; margin: 0 !important; clip: auto !important; clip-path: none !important; opacity: 0; z-index: 1; cursor: inherit; }
.session-filter[data-state=checked] { background: var(--surface); color: var(--text); box-shadow: 0 1px 3px var(--shadow-color); }
.session-filter:has(input:focus-visible) { outline: var(--focus-outline, 2px solid var(--accent)); outline-offset: 2px; }
.session-filter[data-disabled] { opacity: .5; cursor: not-allowed; }
.session-filter-label { display: flex; align-items: center; justify-content: center; gap: 10px; font-size: var(--font-size-body); }
.session-filter-count { font-size: var(--font-size-small); font-variant-numeric: tabular-nums; color: var(--muted); }
.session-manager-workers { margin-inline-start: auto; font-size: var(--font-size-small); white-space: nowrap; }
.session-selection-toolbar { display: flex; flex-basis: 100%; align-items: center; gap: 8px 12px; min-height: var(--button-icon-size); }
.session-selection-toolbar > .ui-checkbox-wrapper { flex: 1; min-width: 0; }
.session-selection-toolbar > [role=status] { margin-inline-start: auto; color: var(--muted); font-size: var(--font-size-small); }
.session-manager-content { flex: 1; min-width: 0; min-height: 0; overflow: auto; padding: 4px 16px 16px; scrollbar-width: thin; }
.session-manager-loading { padding: 8px 12px; color: var(--muted); }
.managed-session { position: relative; display: flex; align-items: center; gap: 12px; padding: 12px; border-radius: 10px; transition: background-color .16s; }
.managed-session + .managed-session { margin-top: 2px; }
.managed-session:hover, .managed-session:has(.session-menu[aria-expanded=true]) { background: var(--surface-subtle); }
.managed-session.managed-session-selected { background: var(--selected); }
.managed-session-info { flex: 1; min-width: 0; }
.managed-session-info :deep(.inline-session-title) { min-height: var(--control-height); }
.managed-session-info :deep(.session-rename .ui-input) { width: 100%; }
.managed-session-entry.ui-btn { width: 100%; min-width: 0; justify-content: flex-start; height: var(--control-height); margin: 0; padding: 0; border: 0; background: transparent; }
.managed-session-entry::after { content: ''; position: absolute; inset: 0; cursor: pointer; }
.managed-session-entry:disabled::after { cursor: default; }
.managed-session-entry.ui-btn-text:not(:disabled):hover { background: transparent; }
.managed-session-entry:focus-visible { outline: none; }
.managed-session:has(.managed-session-entry:focus-visible) { outline: var(--focus-outline, 2px solid var(--accent)); outline-offset: -2px; }
.managed-session .session-name { display: block; width: 100%; max-width: 100%; min-width: 0; margin: 0; padding: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; text-align: start; font-size: var(--font-size-body); font-weight: 500; line-height: var(--control-height); }
.managed-session-meta { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 8px; color: var(--muted); font-size: var(--font-size-small); line-height: 1.6; }
.managed-session-meta time { overflow-wrap: anywhere; }
.managed-session-info :deep(.session-state) { display: flex; flex-wrap: wrap; margin-top: 4px; }
.managed-session-actions { position: relative; z-index: 1; display: flex; flex: none; align-items: center; gap: 4px; }
.managed-session-selection { position: static; flex: none; }
.managed-session-selection :deep(input) { position: absolute; inset: 0; }
.managed-session-selecting { cursor: pointer; }
.managed-session-pin { flex: none; color: var(--accent); }
.session-bulk-actions { display: flex; justify-content: flex-end; align-items: center; flex-wrap: wrap; flex: none; gap: 8px; margin: 0 28px; padding: 12px 0; border-top: 1px solid var(--border-subtle); }
.session-manager-page > .list-pagination { padding: 12px 28px; }
.session-batch-toolbar-enter-active, .session-batch-toolbar-leave-active { transition: opacity .16s, transform .16s; }
.session-batch-toolbar-enter-from, .session-batch-toolbar-leave-to { opacity: 0; transform: translateY(4px); }
@media (max-width: 830px) {
  .session-manager-toolbar { padding: 12px 18px; }
  .session-manager-search { flex-basis: 100%; }
  .session-manager-controls { flex: 1; }
  .session-manager-filters { flex: 1; }
  .session-filter { flex: 1 1 auto; min-width: 0; min-height: 40px; padding-inline: 8px; }
  .session-filter-label { flex-wrap: wrap; gap: 2px 8px; }
  .session-manager-content { padding: 0 6px 12px; }
  .managed-session { display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: 'title actions' 'meta meta' 'state state'; gap: 0 10px; }
  .managed-session-info { display: contents; }
  .managed-session-info :deep(.inline-session-title) { grid-area: title; }
  .managed-session-meta { grid-area: meta; }
  .managed-session-info :deep(.session-state) { grid-area: state; }
  .managed-session-actions { grid-area: actions; gap: 0; }
  .managed-session-selecting { grid-template-columns: 16px minmax(0, 1fr) auto; grid-template-areas: 'selection title pin' 'selection meta pin' 'selection state pin'; }
  .managed-session-selection { grid-area: selection; }
  .managed-session-pin { grid-area: pin; }
  .session-bulk-actions { justify-content: stretch; margin-inline: 18px; }
  .session-bulk-actions > .ui-btn { flex: 1; }
  .session-manager-page > .list-pagination { padding: 8px 18px; }
}
@media (max-height: 520px) {
  .session-manager-toolbar { gap: 6px 12px; padding-block: 8px; }
  .session-manager-search { flex-basis: 140px; }
  .session-manager-controls { flex: 0 0 auto; }
  .session-filter { padding-inline: 6px; }
  .session-filter-label { flex-wrap: nowrap; gap: 6px; white-space: nowrap; }
  .session-bulk-actions { padding-block: 6px; }
  .session-manager-page > .list-pagination { padding-block: 4px; }
}
@media (prefers-reduced-motion: reduce) {
  .session-filter, .managed-session, .session-batch-toolbar-enter-active, .session-batch-toolbar-leave-active { transition: none; }
}
</style>
