<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Empty as VEmpty, Button as VButton, Modal as VModal, Input as VInput } from './ui'
import { AppstoreOutlined, SettingOutlined, CloseOutlined, PlusOutlined, SearchOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import AppProvider from './AppProvider.vue'
import WorkspaceSplit from './ui/WorkspaceSplit.vue'
import { provideWorkspace } from './composables/useWorkspace'
import { useSidebarWidth } from './composables/useSidebarWidth'
import { useDrawerSwipe } from './composables/useDrawerSwipe'
import VistaMark from './VistaMark.vue'
import SessionIndicators from './SessionIndicators.vue'
import { sessionHasUnread } from './state/workspace'
const { t } = useI18n({ useScope: 'global' })
const { router, error, busy, totalSessions, openSessions, selected, serverStatus, mobileSessionsOpen, settingsOpen,
  stateConnection, stateSync, backToWorkspace, openSettings, openSessionManager, create, action, workerAvailable, displaySessionTitle,
  closeConfirmation, cancelSessionClose, confirmSessionClose } = provideWorkspace()
const layout = ref<HTMLElement | null>(null)
const { desktop, width: sidebarWidth, minimum, maximum, resize, finishResize, reset } = useSidebarWidth()
const sessionsPanel = ref<HTMLElement | null>(null)
const sessionsBackdrop = ref<HTMLElement | null>(null)
const drawerSwipe = useDrawerSwipe(sessionsPanel, sessionsBackdrop, mobileSessionsOpen, desktop)
const sessionSearch = ref('')
const filteredSessions = computed(() => openSessions.value.filter(item => displaySessionTitle(item).toLocaleLowerCase().includes(sessionSearch.value.trim().toLocaleLowerCase())))
watch(() => selected.value?.id, async () => {
  sessionSearch.value = ''
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
        <div class="sidebar-brand"><button class="brand-home" aria-label="Vista" :disabled="busy" @click="create"><VistaMark /><span>Vista</span></button><v-button variant="text" class="icon-button mobile-sessions-close icon-close" :aria-label="t('closeSessionDrawer')" @click="mobileSessionsOpen = false"><CloseOutlined /></v-button></div>
        <v-button variant="text" class="sidebar-new-conversation" :disabled="busy" @click="create"><PlusOutlined aria-hidden="true" /><span>{{ t('newConversation') }}</span></v-button>
        <v-input v-if="openSessions.length" v-model:value="sessionSearch" class="sidebar-search" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')" allow-clear><template #prefix><SearchOutlined aria-hidden="true" /></template></v-input>
        <div class="sidebar-section-label"><span>{{ t('recentConversations') }}</span><small>{{ openSessions.length || '' }}</small></div>
        <v-empty v-if="!openSessions.length" class="vista-empty-state vista-empty-state-compact sidebar-empty" :description="t(totalSessions ? 'noOpenSessions' : 'createSessionHint')" />
        <v-empty v-else-if="!filteredSessions.length" class="vista-empty-state vista-empty-state-compact sidebar-empty" :description="t('noMatchingSessions')" />
        <ul><li v-for="item in filteredSessions" :key="item.id" :data-session-id="item.id" class="session-card" :class="{ selected: selected?.id === item.id }" role="button" tabindex="0" :aria-label="t('activateSession', { title: displaySessionTitle(item) })" :aria-current="selected?.id === item.id ? 'page' : undefined" @click="activateSession(item.id)" @keydown.enter.prevent="activateSession(item.id)" @keydown.space.prevent="activateSession(item.id)">
          <strong class="session-name" :class="{ unread: sessionHasUnread(item) }">{{ displaySessionTitle(item) }}</strong><v-button variant="text" class="icon-button session-close icon-close" :aria-label="t('closeSession', { title: displaySessionTitle(item) })" :disabled="busy" @click.stop="action(item,'close')" @keydown.enter.stop @keydown.space.stop><CloseOutlined /></v-button>
          <SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" />
          <v-button v-if="serverStatus && !workerAvailable(item.id)" variant="text" class="session-retry" :disabled="busy" @click.stop="action(item,'retry')" @keydown.enter.stop @keydown.space.stop>{{ t('retryWorker') }}</v-button>
        </li></ul>
        <footer class="sessions-panel-footer">
          <nav class="sidebar-navigation" :aria-label="t('workspaceHeading')">
            <v-button variant="text" class="manage-sessions-link" @click="openSessionManager"><AppstoreOutlined aria-hidden="true" />{{ t('manageSessions') }}</v-button>
            <v-button variant="text" class="settings-trigger" :aria-label="t('settings')" @click="settingsOpen ? backToWorkspace() : openSettings()"><SettingOutlined aria-hidden="true" />{{ t('settings') }}</v-button>
          </nav>
        </footer>
      </aside>
      </template>
      <router-view />
      </WorkspaceSplit>
    </div>
  </main>
  <v-modal :open="!!closeConfirmation" centered :z-index="1200" width="min(420px, calc(100vw - 2rem))"
    :title="t((closeConfirmation?.count ?? 0) > 1 ? 'closeSelectedSessionsConfirmTitle' : 'closeSessionConfirmTitle', { count: closeConfirmation?.count ?? 0 })"
    :ok-text="t('closeAndStop')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: true, disabled: busy }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @cancel="cancelSessionClose" @ok="confirmSessionClose">
    <p>{{ t('closeSessionConfirmBody', { manager: t('manageSessions') }) }}</p>
  </v-modal>
  </AppProvider>
</template>
