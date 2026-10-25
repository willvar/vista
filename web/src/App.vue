<script setup lang="ts">
import { computed, nextTick, ref, watch, watchEffect } from 'vue'
import { Button as AButton, Input as AInput, Splitter as ASplitter, SplitterPanel as ASplitterPanel } from 'antdv-next'
import { AppstoreOutlined, SettingOutlined, SearchOutlined, CloseOutlined, PlusOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import AppProvider from './AppProvider.vue'
import { provideWorkspace } from './composables/useWorkspace'
import { useSidebarWidth } from './composables/useSidebarWidth'
import { useDrawerSwipe } from './composables/useDrawerSwipe'
import VistaMark from './VistaMark.vue'
import SessionIndicators from './SessionIndicators.vue'
import { sessionHasUnread } from './state/workspace'
const { t } = useI18n({ useScope: 'global' })
const { router, error, busy, totalSessions, openSessions, selected, serverStatus, mobileSessionsOpen, settingsOpen,
  stateConnection, stateSync, backToWorkspace, openSettings, openSessionManager, create, action, workerAvailable } = provideWorkspace()
const layout = ref<HTMLElement | null>(null)
const { desktop, width: sidebarWidth, minimum, maximum, resize, finishResize, reset } = useSidebarWidth()
const sessionsPanel = ref<HTMLElement | null>(null)
const sessionsBackdrop = ref<HTMLElement | null>(null)
const drawerSwipe = useDrawerSwipe(sessionsPanel, sessionsBackdrop, mobileSessionsOpen, desktop)
const sessionSearch = ref('')
const filteredSessions = computed(() => openSessions.value.filter(item => item.title.toLocaleLowerCase().includes(sessionSearch.value.trim().toLocaleLowerCase())))
watchEffect(() => {
  const separator = layout.value?.querySelector<HTMLElement>('.ant-splitter-bar')
  if (!separator) return
  separator.setAttribute('aria-label', t('resizeSessionList'))
  separator.setAttribute('aria-orientation', 'vertical')
  separator.setAttribute('aria-controls', 'sessions-panel')
  separator.title = t('resizeSessionListHint')
}, { flush: 'post' })
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
    <p v-if="stateConnection.phase === 'error'" class="error state-sync-error" role="alert" :title="stateConnection.error"><span>{{ t('stateSyncFailed') }}</span><a-button size="small" @click="stateSync.reconnect()">{{ t('retry') }}</a-button></p>
    <div ref="layout" class="layout" :class="{ 'has-session': !!selected }">
      <router-view name="overlay" />
      <a-splitter class="workspace-splitter" :on-resize="resize" :on-resize-end="finishResize" :on-dragger-double-click="reset">
      <a-splitter-panel class="sidebar-split-panel" :size="sidebarWidth" :min="desktop ? minimum : 0" :max="desktop ? maximum : 0" :resizable="desktop">
      <aside id="sessions-panel" ref="sessionsPanel" class="sessions-panel" :class="{ 'mobile-open': mobileSessionsOpen }"
        @pointerdown.capture="drawerSwipe.start" @pointermove="drawerSwipe.move" @pointerup="drawerSwipe.end"
        @pointercancel="drawerSwipe.cancel" @lostpointercapture="drawerSwipe.lostCapture" @click.capture="drawerSwipe.click">
        <div class="sidebar-brand"><button class="brand-home" aria-label="Vista" :disabled="busy" @click="create"><VistaMark /><span>Vista</span></button><a-button type="text" class="mobile-sessions-close icon-close" :aria-label="t('closeSessionDrawer')" @click="mobileSessionsOpen = false"><CloseOutlined /></a-button></div>
        <a-button type="text" class="sidebar-new-conversation" :disabled="busy" @click="create"><PlusOutlined aria-hidden="true" /><span>{{ t('newConversation') }}</span></a-button>
        <a-input v-if="openSessions.length" v-model:value="sessionSearch" class="sidebar-search" :placeholder="t('searchSessions')" :aria-label="t('searchSessions')" allow-clear><template #prefix><SearchOutlined aria-hidden="true" /></template></a-input>
        <div class="sidebar-section-label"><span>{{ t('recentConversations') }}</span><small>{{ openSessions.length || '' }}</small></div>
        <p v-if="!openSessions.length" class="sidebar-empty">{{ t(totalSessions ? 'noOpenSessions' : 'createSessionHint') }}</p>
        <p v-else-if="!filteredSessions.length" class="sidebar-empty">{{ t('noMatchingSessions') }}</p>
        <ul><li v-for="item in filteredSessions" :key="item.id" :data-session-id="item.id" class="session-card" :class="{ selected: selected?.id === item.id }" role="button" tabindex="0" :aria-label="t('activateSession', { title: item.title })" :aria-current="selected?.id === item.id ? 'page' : undefined" @click="activateSession(item.id)" @keydown.enter.prevent="activateSession(item.id)" @keydown.space.prevent="activateSession(item.id)">
          <strong class="session-name" :class="{ unread: sessionHasUnread(item) }">{{ item.title }}</strong><a-button type="text" class="session-close icon-close" :aria-label="t('closeSession', { title: item.title })" :disabled="busy" @click.stop="action(item,'close')" @keydown.enter.stop @keydown.space.stop><CloseOutlined /></a-button>
          <SessionIndicators :session="item" :worker-available="serverStatus ? workerAvailable(item.id) : undefined" />
          <a-button v-if="serverStatus && !workerAvailable(item.id)" type="text" size="small" class="session-retry" :disabled="busy" @click.stop="action(item,'retry')" @keydown.enter.stop @keydown.space.stop>{{ t('retryWorker') }}</a-button>
        </li></ul>
        <footer class="sessions-panel-footer">
          <nav class="sidebar-navigation" :aria-label="t('workspaceHeading')">
            <a-button type="text" class="manage-sessions-link" @click="openSessionManager"><AppstoreOutlined aria-hidden="true" />{{ t('manageSessions') }}</a-button>
            <a-button type="text" class="settings-trigger" :aria-label="t('settings')" @click="settingsOpen ? backToWorkspace() : openSettings()"><SettingOutlined aria-hidden="true" />{{ t('settings') }}</a-button>
          </nav>
        </footer>
      </aside>
      </a-splitter-panel>
      <a-splitter-panel class="conversation-split-panel" :min="desktop ? 480 : 0" :resizable="desktop">
      <router-view />
      </a-splitter-panel>
      </a-splitter>
    </div>
  </main>
  </AppProvider>
</template>
