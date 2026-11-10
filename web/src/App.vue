<script setup lang="ts">
import { computed, nextTick, onErrorCaptured, ref, watch } from 'vue'
import { Empty as VEmpty, Button as VButton, Modal as VModal, Dropdown as VDropdown } from './ui'
import { IconLayoutGrid, IconSettings, IconX, IconPlus, IconDots, IconLoader2 } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import AppProvider from './AppProvider.vue'
import AssetLoadNotice from './AssetLoadNotice.vue'
import { captureAssetError } from './asset-recovery'
import WorkspaceSplit from './ui/WorkspaceSplit.vue'
import { provideWorkspace } from './composables/useWorkspace'
import { useSidebarWidth } from './composables/useSidebarWidth'
import { useDrawerSwipe } from './composables/useDrawerSwipe'
import VistaMark from './VistaMark.vue'
import SessionIndicators from './SessionIndicators.vue'
import InlineSessionTitle from './InlineSessionTitle.vue'
import SessionPin from './SessionPin.vue'
import type { Session } from './client/types'
import { sessionHasUnread } from './state/workspace'
const { t } = useI18n({ useScope: 'global' })
onErrorCaptured(error => { if (captureAssetError(error)) return false })
const { router, error, busy, totalSessions, openSessions, selected, serverStatus, mobileSessionsOpen, settingsOpen,
  stateConnection, stateSync, backToWorkspace, openSettings, openSessionManager, create, action, workerAvailable, displaySessionTitle,
  closeConfirmation, cancelSessionClose, confirmSessionClose } = provideWorkspace()
const layout = ref<HTMLElement | null>(null)
const { desktop, width: sidebarWidth, minimum, maximum, resize, finishResize, reset } = useSidebarWidth()
const sessionsPanel = ref<HTMLElement | null>(null)
const sessionsBackdrop = ref<HTMLElement | null>(null)
const drawerSwipe = useDrawerSwipe(sessionsPanel, sessionsBackdrop, mobileSessionsOpen, desktop)
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
      { key: 'close', label: t('close'), disabled: busy.value },
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
function activateSession(id: string) {
  if (selected.value?.id === id) void create()
  else void router.push(`/sessions/${id}`)
}
</script>

<template>
  <AppProvider>
  <main class="app-shell">
    <AssetLoadNotice />
    <Transition name="mobile-sessions-mask"><div v-if="mobileSessionsOpen" ref="sessionsBackdrop" class="mobile-sessions-backdrop" @click="mobileSessionsOpen = false"></div></Transition>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="stateConnection.phase === 'error'" class="error state-sync-error" role="alert" :title="stateConnection.error"><span>{{ t('stateSyncFailed') }}</span><v-button @click="stateSync.reconnect()">{{ t('retry') }}</v-button></p>
    <div ref="layout" class="layout" :class="{ 'has-session': !!selected }">
      <router-view name="overlay" />
      <WorkspaceSplit :sidebar-width="sidebarWidth" :minimum="minimum" :maximum="maximum" :desktop="desktop" @resize="resize" @resize-end="finishResize" @reset="reset">
      <template #sidebar>
      <aside id="sessions-panel" ref="sessionsPanel" class="sessions-panel" :class="{ 'mobile-open': mobileSessionsOpen }"
        @pointerdown.capture="drawerSwipe.start" @pointermove="drawerSwipe.move" @pointerup="drawerSwipe.end"
        @pointercancel="drawerSwipe.cancel" @lostpointercapture="drawerSwipe.lostCapture" @click.capture="drawerSwipe.click">
        <div class="sidebar-brand"><button class="brand-home" aria-label="Vista" :disabled="busy" @click="create"><VistaMark /><span>Vista</span></button><v-button variant="text" class="icon-button mobile-sessions-close icon-close" :aria-label="t('closeSessionDrawer')" @click="mobileSessionsOpen = false"><IconX /></v-button></div>
        <v-button variant="text" class="sidebar-new-conversation" :disabled="busy" @click="create"><IconPlus aria-hidden="true" /><span>{{ t('newConversation') }}</span></v-button>
        <div class="sidebar-section-label"><span>{{ t('recentConversations') }}</span><small>{{ openSessions.length || '' }}</small></div>
        <v-empty v-if="!openSessions.length" class="vista-empty-state vista-empty-state-compact sidebar-empty" :description="t(totalSessions ? 'noOpenSessions' : 'createSessionHint')" />
        <ul><li v-for="item in sidebarSessions" :key="item.id" :data-session-id="item.id" class="session-card" :class="{ selected: selected?.id === item.id }" role="button" tabindex="0" :aria-label="t('activateSession', { title: displaySessionTitle(item) })" :aria-current="selected?.id === item.id ? 'page' : undefined" @click="activateSession(item.id)" @keydown.enter.prevent="activateSession(item.id)" @keydown.space.prevent="activateSession(item.id)">
          <InlineSessionTitle :session="item" :editing="renamingSession === item.id" @update:editing="updateRenaming(item, $event)">
            <strong class="session-name" :class="{ unread: sessionHasUnread(item) }">{{ displaySessionTitle(item) }}</strong>
          </InlineSessionTitle>
          <div class="session-card-actions" @click.stop @keydown.enter.stop @keydown.space.stop>
            <span v-if="item.running && item.executing && (!serverStatus || workerAvailable(item.id))" class="session-execution-loading" aria-hidden="true"><IconLoader2 class="ui-icon-spin" /></span>
            <v-dropdown :menu="sessionMenu(item)"><v-button variant="text" class="icon-button session-menu" :aria-label="t('sessionMenu')" :disabled="busy"><IconDots /></v-button></v-dropdown>
            <SessionPin :session="item" />
          </div>
          <SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" />
          <v-button v-if="serverStatus && !workerAvailable(item.id)" variant="text" class="session-retry" :disabled="busy" @click.stop="action(item,'retry')" @keydown.enter.stop @keydown.space.stop>{{ t('retryWorker') }}</v-button>
        </li></ul>
        <footer class="sessions-panel-footer">
          <nav class="sidebar-navigation" :aria-label="t('workspaceHeading')">
            <v-button variant="text" class="manage-sessions-link" @click="openSessionManager"><IconLayoutGrid aria-hidden="true" />{{ t('manageSessions') }}</v-button>
            <v-button variant="text" class="settings-trigger" :aria-label="t('settings')" @click="settingsOpen ? backToWorkspace() : openSettings()"><IconSettings aria-hidden="true" />{{ t('settings') }}</v-button>
          </nav>
        </footer>
      </aside>
      </template>
      <router-view />
      </WorkspaceSplit>
    </div>
  </main>
  <v-modal :open="!!closeConfirmation" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
    :title="t(closeDialogDetails.count > 1 ? 'closeSelectedSessionsConfirmTitle' : 'closeSessionConfirmTitle', { count: closeDialogDetails.count })"
    :ok-text="t(closeDialogDetails.stopsWork ? 'closeAndStop' : 'close')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="cancelSessionClose" @ok="confirmSessionClose">
    <p>{{ t('closeSessionConfirmBody', { manager: t('manageSessions') }) }}</p>
  </v-modal>
  <v-modal :open="deleteConfirmationOpen" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
    :title="t('deleteSessionConfirmTitle')" :ok-text="t('delete')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="!busy && (deleteConfirmationOpen = false)" @ok="deleteSession">
    <p style="overflow-wrap: anywhere">{{ t('deleteSessionConfirm', { title: deletingSession ? displaySessionTitle(deletingSession) : '' }) }}</p>
  </v-modal>
  </AppProvider>
</template>
