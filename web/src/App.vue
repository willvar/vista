<script setup lang="ts">
import { computed, nextTick, onErrorCaptured, ref, watch } from 'vue'
import { Empty as VEmpty, Button as VButton, Modal as VModal, Dropdown as VDropdown } from './ui'
import { IconLayoutGrid, IconSettings, IconX, IconPlus, IconDots, IconLoader2, IconClock } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import AssetLoadNotice from './AssetLoadNotice.vue'
import { captureAssetError } from './asset-recovery'
import WorkspaceSplit from './ui/WorkspaceSplit.vue'
import { provideWorkspace } from './composables/useWorkspace'
import { useSidebarWidth } from './composables/useSidebarWidth'
import { useDrawerSwipe } from './composables/useDrawerSwipe'
import VistaMark from './VistaMark.vue'
import ProfileMenu from './ProfileMenu.vue'
import SessionIndicators from './SessionIndicators.vue'
import InlineSessionTitle from './InlineSessionTitle.vue'
import SessionPin from './SessionPin.vue'
import SessionActionDialog from './SessionActionDialog.vue'
import { sessionEntryId, sessionListActivity } from '@vista/client'
import type { Session } from '@vista/client'
const props = defineProps<{ profileId: string }>()
const { t } = useI18n({ useScope: 'global' })
onErrorCaptured(error => { if (captureAssetError(error)) return false })
const { desktop, width: sidebarWidth, minimum, maximum, resize, finishResize, reset } = useSidebarWidth()
const { route, conversation, error, busy, totalSessions, openSessions, selected, serverStatus, mobileSessionsOpen, drawerEntryOpen, settingsOpen,
  sessionsReady, modelsReady, stateConnection, stateSync, modalQuery, openSettings, closeSettings, openSessionManager, create, action, workerAvailable, displaySessionTitle,
  closeConfirmation, cancelSessionClose, confirmSessionClose, pathInProfile, workspacePath, closeMobileSessions, navigateFromSidebar } = provideWorkspace(props.profileId, desktop)
const initialLoading = computed(() => !sessionsReady.value || !modelsReady.value || !serverStatus.value)
const initialLoadDelayed = ref(false)
watch(initialLoading, (loading, _, cleanup) => {
  if (!loading) return
  const timer = setTimeout(() => { initialLoadDelayed.value = true }, 5_000)
  cleanup(() => clearTimeout(timer))
}, { immediate: true })
const layout = ref<HTMLElement | null>(null)
const sessionsPanel = ref<HTMLElement | null>(null)
const sessionsBackdrop = ref<HTMLElement | null>(null)
const drawerSwipe = useDrawerSwipe(sessionsPanel, sessionsBackdrop, mobileSessionsOpen, desktop, closeMobileSessions)
const renamingSession = ref('')
const editingSessionOrder = ref<string[]>([])
// Update live row contents, but defer reordering until the title editor closes.
// Moving a focused input's DOM node would blur it and submit an unfinished title.
const sidebarSessions = computed(() => {
  if (!renamingSession.value) return openSessions.value
  const positions = new Map(editingSessionOrder.value.map((id, index) => [id, index]))
  return [...openSessions.value].sort((a, b) => (positions.get(a.id) ?? positions.size) - (positions.get(b.id) ?? positions.size))
})
watch(renamingSession, id => { editingSessionOrder.value = id ? sidebarSessions.value.map(item => item.id) : [] }, { flush: 'sync' })
watch(openSessions, items => {
  if (renamingSession.value && !items.some(item => item.id === renamingSession.value)) renamingSession.value = ''
}, { flush: 'sync' })
const deletingSession = ref<Session | null>(null)
const deleteConfirmationOpen = ref(false)
// Keep confirmation copy stable while the dialog's exit animation runs.
const closeDialogDetails = ref({ count: 0, stopsWork: false })
watch(closeConfirmation, request => {
  if (request) closeDialogDetails.value = { count: request.count, stopsWork: request.stopsWork }
}, { flush: 'sync' })
function updateRenaming(item: Session, editing: boolean) {
  if (editing) renamingSession.value = item.id
  else if (renamingSession.value === item.id) renamingSession.value = ''
}
function sessionMenu(item: Session) {
  return {
    items: [
      { key: 'rename', label: t('editTitle'), disabled: busy.value },
      ...(sessionEntryId(item) ? [{ key: 'close', label: t('close'), disabled: busy.value }] : []),
      { key: 'delete', label: t('delete'), disabled: busy.value, danger: true },
    ],
    onClick: ({ key }: { key: string }) => {
      if (key === 'rename') renamingSession.value = item.id
      else if (key === 'close') void action(item, 'close', true)
      else if (key === 'delete') { deletingSession.value = item; deleteConfirmationOpen.value = true }
    },
  }
}
async function deleteSession() {
  if (!deletingSession.value || busy.value) return
  await action(deletingSession.value, 'delete')
  deleteConfirmationOpen.value = false
}

watch(() => selected.value?.id, async () => {
  await nextTick()
  if (window.innerWidth > 830 || mobileSessionsOpen.value) document.querySelector('.sessions-panel .session-card.selected')?.scrollIntoView({ block: 'nearest' })
})
let drawerParent = ''
watch(mobileSessionsOpen, async open => {
  if (open) { drawerParent = workspacePath.value; return }
  if (drawerEntryOpen.value) return // The desktop sidebar paints the same history entry without a drawer.
  if (!busy.value) { cancelSessionClose(); deleteConfirmationOpen.value = false }
  if (desktop.value || drawerParent !== workspacePath.value) return
  await nextTick()
  if (!mobileSessionsOpen.value && drawerParent === workspacePath.value) {
    const trigger = [...document.querySelectorAll<HTMLElement>('.mobile-sessions-trigger')].find(element => element.getClientRects().length)
    trigger?.focus({ preventScroll: true })
  }
}, { immediate: true })
const selectedRoot = computed(() => selected.value?.branchRootId || selected.value?.id)
function openBranch(id: string) { void navigateFromSidebar(pathInProfile(`/sessions/${id}`)) }
function activateSession(item: Session) {
  const id = sessionEntryId(item)
  if (!id) return
  if (selected.value?.id === id) void create()
  else openBranch(id)
}
function isExecuting(item: Session) {
  const id = sessionListActivity(item).executingId
  return id && (!serverStatus.value || workerAvailable(id))
}
</script>

<template>
  <main class="app-shell">
    <AssetLoadNotice />
    <Transition name="mobile-sessions-mask"><div v-if="mobileSessionsOpen" ref="sessionsBackdrop" class="mobile-sessions-backdrop" @click="closeMobileSessions"></div></Transition>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="stateConnection.phase === 'error'" class="error state-sync-error" role="alert" :title="stateConnection.error"><span>{{ t('stateSyncFailed') }}</span><v-button @click="stateSync.reconnect()">{{ t('retry') }}</v-button></p>
    <p v-else-if="initialLoading" class="state-sync-loading" role="status">
      <IconLoader2 class="ui-icon-spin" aria-hidden="true" /><span>{{ t(initialLoadDelayed ? 'stateSyncSlow' : 'uiLoading') }}</span>
    </p>
    <div ref="layout" class="layout" :class="{ 'has-session': !!selected }">
      <router-view name="overlay" />
      <WorkspaceSplit :sidebar-width="sidebarWidth" :minimum="minimum" :maximum="maximum" :desktop="desktop" @resize="resize" @resize-end="finishResize" @reset="reset">
      <template #sidebar>
      <aside id="sessions-panel" ref="sessionsPanel" class="sessions-panel" :class="{ 'mobile-open': mobileSessionsOpen }"
        @pointerdown.capture="drawerSwipe.start" @pointermove="drawerSwipe.move" @pointerup="drawerSwipe.end"
        @pointercancel="drawerSwipe.cancel" @lostpointercapture="drawerSwipe.lostCapture" @click.capture="drawerSwipe.click">
        <div class="sidebar-brand"><button class="brand-home" aria-label="Vista" :disabled="busy" @click="create"><VistaMark /><span>Vista</span></button><v-button variant="text" class="icon-button mobile-sessions-close icon-close" :aria-label="t('closeSessionDrawer')" @click="closeMobileSessions"><IconX /></v-button></div>
        <v-button variant="text" class="sidebar-new-conversation" :disabled="busy" @click="create"><IconPlus aria-hidden="true" /><span>{{ t('newConversation') }}</span></v-button>
        <div class="sidebar-section-label"><span>{{ t('recentConversations') }}</span><small>{{ openSessions.length || '' }}</small></div>
        <v-empty v-if="sessionsReady && serverStatus && !openSessions.length" class="vista-empty-state vista-empty-state-compact sidebar-empty" :description="t(totalSessions ? 'noOpenSessions' : 'createSessionHint')" />
        <ul><li v-for="item in sidebarSessions" :key="item.id" :data-session-id="item.id" class="session-card" :class="{ selected: selectedRoot === item.id }" role="button" tabindex="0" :aria-label="t('activateSession', { title: displaySessionTitle(item) })" :aria-current="selectedRoot === item.id ? 'page' : undefined" @click="activateSession(item)" @keydown.enter.prevent="activateSession(item)" @keydown.space.prevent="activateSession(item)">
          <InlineSessionTitle :session="item" :editing="renamingSession === item.id" @update:editing="updateRenaming(item, $event)">
            <strong class="session-name" :class="{ unread: !!sessionListActivity(item).unreadId }">{{ displaySessionTitle(item) }}</strong>
          </InlineSessionTitle>
          <div class="session-card-actions" @click.stop @keydown.enter.stop @keydown.space.stop>
            <span v-if="isExecuting(item)" class="session-execution-loading" aria-hidden="true"><IconLoader2 class="ui-icon-spin" /></span>
            <v-dropdown :menu="sessionMenu(item)"><v-button variant="text" class="icon-button session-menu" :aria-label="t('sessionMenu')" :disabled="busy"><IconDots /></v-button></v-dropdown>
            <SessionPin :session="item" />
          </div>
          <SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" @open="openBranch" />
          <v-button v-if="item.open && serverStatus && !workerAvailable(item.id)" variant="text" class="session-retry" :disabled="busy" @click.stop="action(item,'retry')" @keydown.enter.stop @keydown.space.stop>{{ t('retryWorker') }}</v-button>
        </li></ul>
        <footer class="sessions-panel-footer">
          <nav class="sidebar-navigation" :aria-label="t('workspaceHeading')">
            <v-button variant="text" class="manage-sessions-link" @click="openSessionManager"><IconLayoutGrid aria-hidden="true" />{{ t('manageSessions') }}</v-button>
            <v-button variant="text" class="schedule-link" :aria-label="t('scheduledTasks')" :title="t('scheduledTasks')"
              @click="navigateFromSidebar({ path: pathInProfile('/schedules'), query: modalQuery() })">
              <IconClock aria-hidden="true" /><span>{{ t('scheduledTasks') }}</span>
            </v-button>
            <div class="sidebar-profile-row">
              <ProfileMenu />
              <v-button variant="text" class="settings-trigger" :aria-label="t('settings')" :title="t('settings')" @click="settingsOpen ? closeSettings() : openSettings()"><IconSettings aria-hidden="true" /></v-button>
            </div>
          </nav>
        </footer>
      </aside>
      </template>
      <router-view />
      </WorkspaceSplit>
    </div>
  </main>
  <v-modal :open="!!closeConfirmation" class="session-confirmation" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
    :title="t(closeDialogDetails.count > 1 ? 'closeSelectedSessionsConfirmTitle' : 'closeSessionConfirmTitle', { count: closeDialogDetails.count })"
    :ok-text="t(closeDialogDetails.stopsWork ? 'closeAndStop' : 'close')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="cancelSessionClose" @ok="confirmSessionClose">
    <p>{{ t('closeSessionConfirmBody', { manager: t('manageSessions') }) }}</p>
  </v-modal>
  <v-modal :open="deleteConfirmationOpen" class="session-confirmation" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
    :title="t('deleteSessionConfirmTitle')" :ok-text="t('delete')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="!busy && (deleteConfirmationOpen = false)" @ok="deleteSession">
    <p style="overflow-wrap: anywhere">{{ t('deleteSessionConfirm', { title: deletingSession ? displaySessionTitle(deletingSession) : '' }) }}</p>
  </v-modal>
  <SessionActionDialog v-if="route.name === 'session' && selected?.open" :action="conversation.sessionAction.value" :busy="busy"
    @decision="conversation.decideSessionAction" />
</template>

<style scoped>
:global(.session-confirmation .ui-modal-container) {
  display: flex;
  flex-direction: column;
  max-height: calc(100dvh - 32px);
}
:global(.session-confirmation .ui-modal-body) {
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
}
:global(.session-confirmation .ui-modal-footer) {
  flex: none;
}

@media (max-width: 830px) and (max-height: 520px) {
  .sidebar-navigation { grid-template-columns: minmax(0, 1fr) auto; }
  .sidebar-profile-row { grid-column: 1 / -1; }
  .sidebar-navigation .schedule-link { width: 44px; min-height: 44px; padding: 0; justify-content: center; }
  .schedule-link span { display: none; }
}
</style>
