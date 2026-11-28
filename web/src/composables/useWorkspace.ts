import { computed, inject, nextTick, onMounted, onUnmounted, provide, ref, watch, type InjectionKey, type Ref } from 'vue'
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router'
import { layerTokens, returnToParent, useRouteLayer, withLayers } from './routeLayers'
import { useI18n } from 'vue-i18n'
import { VistaAPIError, createWorkspaceSync, mergeSessionSnapshot, sessionEntryId, sessionListActivity } from '@vista/client'
import type { StateTopic, StateConnection, WorkspaceSnapshots, ModelCatalog, ServerStatus, Session, SessionSort, SyncCheckpoint } from '@vista/client'
import { createProfileClient, profilePath, profileStorageKey, routeProfileId } from '../client/profileScope'
import { draftStoragePrefix, useConversation } from './useConversation'
import { usePresentation } from './usePresentation'
import { useSidebarSessions } from './useSidebarSessions'
import { useInteractionPreferences } from './useInteractionPreferences'
import { isPlainEscape } from './useEscape'
import { quoteStoragePrefix } from '../state/quotedReply'
import { trackActivity } from '../client/activity'

function createWorkspace(profileId: string, desktop: Readonly<Ref<boolean>>) {
  const route = useRoute()
  const router = useRouter()
  const { t } = useI18n({ useScope: 'global' })
  const client = createProfileClient(() => document.documentElement.lang, profileId)
  const pathInProfile = (path: string) => profilePath(profileId, path)
  const storageKey = (key: string) => profileStorageKey(key, profileId)
  const forgetLocalDraft = (id: string) => {
    try {
      localStorage.removeItem(storageKey(draftStoragePrefix) + id)
      localStorage.removeItem(storageKey(quoteStoragePrefix) + id)
      sessionStorage.removeItem(storageKey('vista-pending-draft:') + id)
    } catch { /* Storage can be unavailable. */ }
  }
  const error = ref('')
  const busy = ref(false)
  const debugMode = ref(false)
  try {
    const saved = document.cookie.split('; ').find(item => item.startsWith('vista-debug-mode='))?.slice('vista-debug-mode='.length)
    debugMode.value = (saved ?? localStorage.getItem('vista-debug-mode')) === 'true'
  } catch { /* Diagnostics stay hidden when storage is unavailable. */ }
  watch(debugMode, (value, previous) => {
    if (previous !== undefined) {
      try { localStorage.setItem('vista-debug-mode', String(value)) } catch { /* Keep the choice for this page. */ }
    }
    // Like language, retain this browser preference when a restart changes the server port.
    document.cookie = `vista-debug-mode=${value}; Path=/; Max-Age=31536000; SameSite=Strict`
  }, { flush: 'sync', immediate: true })
  // Local conversation events can update row content, not membership or the
  // ordering of the server's contiguous page window.
  const sessions = ref<Session[]>([])
  const sessionsReady = ref(false)
  const sidebarSortKey = storageKey('vista-sidebar-sort')
  const sidebarSort = ref<SessionSort>('created-desc')
  try {
    const saved = localStorage.getItem(sidebarSortKey)
    if (saved === 'created-asc' || saved === 'updated-desc' || saved === 'updated-asc' || saved === 'title-asc') sidebarSort.value = saved
  } catch { /* Use the default if browser storage is unavailable. */ }
  watch(sidebarSort, value => {
    try { localStorage.setItem(sidebarSortKey, value) } catch { /* Keep the choice for this page. */ }
  }, { flush: 'sync' })
  const sidebar = useSidebarSessions(client, computed(() => ({
    open: true, sort: sidebarSort.value, untitledLabel: sidebarSort.value === 'title-asc' ? t('untitledSession') : undefined,
  })))
  const attentionCount = ref(0)
  watch(sidebar.snapshot, (current, previous) => {
    const local = new Map(sessions.value.map(item => [item.id, item]))
    sessions.value = current?.items.map((item, index) => item === previous?.items[index] ? local.get(item.id) ?? item : item) ?? []
    if (current) {
      sessionsReady.value = true
      attentionCount.value = current.attention ?? 0
    }
  }, { flush: 'sync' })
  watch(sidebar.failed, failed => { if (failed) sessionsReady.value = true })
  const modelCatalog = ref<ModelCatalog>({ default: '', models: [] })
  const modelsReady = ref(false)
  const serverStatus = ref<ServerStatus | null>(null)
  const drawer = useRouteLayer('sessions-drawer', to => routeProfileId(to.params) === profileId && (to.name === 'home' || to.name === 'session'))
  const drawerEntryOpen = computed(() => drawer.current.value !== undefined)
  const mobileSessionsOpen = computed(() => !desktop.value && drawerEntryOpen.value)
  const workspacePath = computed(() => router.resolve(withLayers(route, [])).fullPath)
  function openMobileSessions() {
    if (!layerTokens(route).length && window.matchMedia('(max-width: 830px)').matches) void drawer.open()
  }
  function closeMobileSessions() { drawer.close() }
  function navigateFromSidebar(destination: RouteLocationRaw) {
    return drawerEntryOpen.value ? drawer.complete(destination) : router.push(destination)
  }
  const openSessions = computed(() => {
    const byId = new Map(sessions.value.map(item => [item.id, item]))
    return sidebar.snapshot.value?.items.map(item => byId.get(item.id) ?? item) ?? []
  })
  const openTotal = computed(() => sidebar.snapshot.value?.total ?? serverStatus.value?.sessions.open ?? 0)
  const closedSessions = computed(() => serverStatus.value?.sessions.closed ?? 0)
  const totalSessions = computed(() => serverStatus.value?.sessions.total ?? openTotal.value)
  const settingsOpen = computed(() => route.name === 'settings' || String(route.name).startsWith('settings-'))
  const sessionManagerOpen = computed(() => route.name === 'session-manager')
  const needsProvider = computed(() => modelsReady.value && !modelCatalog.value.models.some(model => model.provider))
  const interactionPreferences = useInteractionPreferences(client, cause => { error.value = String(cause) }, profileId)
  const revisions = { sessions: 0, models: 0, status: 0 }
  const stateConnection = ref<StateConnection>({ phase: 'connecting' })
  const stateSync = createWorkspaceSync({
    client, pagedSessions: true,
    status: value => { stateConnection.value = value },
    metrics: value => {
      if (!serverStatus.value || Date.parse(value.sampledAt) >= Date.parse(serverStatus.value.sampledAt)) {
        revisions.status++
        serverStatus.value = value
      }
    },
  })
  watch(sidebar.interest, stateSync.setSessionWindow, { immediate: true, flush: 'sync' })
  const wantsMetrics = computed(() => debugMode.value || route.name === 'settings-about')
  watch(wantsMetrics, value => stateSync.setMetrics(value && !document.hidden))
  let disposed = false
  const scopeActive = () => !disposed && routeProfileId(route.params) === profileId
  let stopActivity: (() => void) | undefined
  const conversation = useConversation({ client, sessions, modelCatalog, error, busy, refresh: refreshSessions, translate: t, profileId, interactionPreferences, serverDrafts: true,
    onSessionStarted: async session => { if (scopeActive()) await router.push(pathInProfile(`/sessions/${session.id}`)) } })
  const stopProfileGuard = router.beforeEach(async to => {
    if (!scopeActive() || routeProfileId(to.params) === profileId) return
    const changingConversation = () => busy.value || conversation.editingMessage.value
    if (changingConversation()) { error.value = t('profileSwitchBusy'); return false }
    try { await conversation.flushDrafts() }
    catch { error.value = t('draftUnsaved'); return false }
    if (changingConversation()) { error.value = t('profileSwitchBusy'); return false }
  })
  const { selected, manualCompacting, compactionRequestPending, agentWorking, selectedModel } = conversation
  const senderDisabled = computed(() => manualCompacting.value || compactionRequestPending.value || !interactionPreferences.ready.value || !modelsReady.value
    || (!agentWorking.value && !selectedModel.value)
    || !!selected.value && (!selected.value.open || !workerAvailable(selected.value.id)))
  const presentation = usePresentation(modelCatalog)

  async function refreshTopics(topics: StateTopic[], signal?: AbortSignal, pushed: WorkspaceSnapshots = {}, version?: string) {
    if (!scopeActive()) return
    await Promise.all(topics.map(async topic => {
      if (topic !== 'sessions' && topic !== 'status' && topic !== 'models') return
      const revision = ++revisions[topic]
      const started = selected.value ? { sessionId: selected.value.id, eventSequence: conversation.lastEvent.value } : null
      if (topic === 'sessions') {
        await sidebar.synchronize(version, signal, pushed.sessionWindow)
        if (!scopeActive() || signal?.aborted || revision !== revisions.sessions) return
        const activeID = selected.value?.id
        const routedID = route.name === 'session' ? String(route.params.id) : ''
        const target = activeID || routedID
        // Not having a row is normal in a paged list. Only the single-session
        // checkpoint can distinguish an off-page conversation from a deletion.
        const window = sidebar.snapshot.value
        let item = window?.items.find(row => row.id === target)
        let eventSequence = window?.throughSequences?.[target] ?? (started?.sessionId === target ? started.eventSequence : 0)
        if (target && !item) {
          try {
            const point = await client.request<SyncCheckpoint>(`/sessions/${target}/checkpoint`, { signal })
            item = point.session
            eventSequence = point.throughSequence
          }
          catch (cause) { if (!(cause instanceof VistaAPIError && cause.status === 404)) throw cause }
          if (!scopeActive() || signal?.aborted || revision !== revisions.sessions || selected.value?.id !== activeID
            || (route.name === 'session' ? String(route.params.id) : '') !== routedID) return
        }
        if (activeID) {
          if (item) {
            const boundary = { sessionId: activeID, eventSequence }
            const [current] = mergeSessionSnapshot([item], selected.value, boundary, Math.max(conversation.lastEvent.value, conversation.activeRunSequence.value))
            selected.value = current!
            if (sidebar.snapshot.value === window) sessions.value = sessions.value.map(row => row.id === current!.id ? current! : row)
          } else conversation.clearSession()
        }
        if (target && routedID === target) {
          if (!item) { await router.replace(pathInProfile('/')); return }
          if (!item.open && selected.value?.id !== target) { await router.replace(pathInProfile('/sessions')); return }
        }
        // A new session can receive an update before its first send leaves
        // home. Only actual route navigation may clear that in-flight scope.
        if (route.name !== 'home') void syncRoute(item)
      } else if (topic === 'status') {
        const status = pushed.status ?? await client.request<ServerStatus>('/status', { signal })
        if (scopeActive() && !signal?.aborted && revision === revisions.status
          && (!serverStatus.value || Date.parse(status.sampledAt) >= Date.parse(serverStatus.value.sampledAt))) serverStatus.value = status
      } else {
        const catalog = pushed.models ?? await client.request<ModelCatalog>('/models', { signal })
        if (!scopeActive() || signal?.aborted || revision !== revisions.models) return
        modelCatalog.value = catalog
        modelsReady.value = true
      }
    }))
  }
  stateSync.subscribe(['sessions', 'status', 'models'], (topics, signal, point) =>
    refreshTopics(topics, signal, point, `${point.epoch}:${point.revisions.sessions}`))
  // Settings and the session manager can cover the composer. Keep its local
  // unsaved edits, but reconcile remote revisions only when it is in use.
  watch(() => route.name === 'home' || route.name === 'session', (visible, _, cleanup) => {
    if (!visible || !scopeActive()) return
    cleanup(stateSync.subscribe(['drafts'], async (_, signal) => {
      if (scopeActive() && (route.name === 'home' || route.name === 'session' && selected.value?.id === route.params.id)) await conversation.refreshDraft(signal)
    }))
  }, { immediate: true })
  async function refreshSessions() {
    try { await refreshTopics(['sessions', 'status']) }
    catch (cause) { if (!disposed) error.value = String(cause) }
  }
  function resumeState() {
    if (!document.hidden) {
      if (!stateSync.setMetrics(wantsMetrics.value)) stateSync.reconnect()
    } else stateSync.setMetrics(false)
  }

  function restorePage(event: PageTransitionEvent) { if (event.persisted) resumeState() }

  function modalQuery() { return selected.value?.open ? { session: selected.value.id } : {} }
  function backToWorkspace() { void router.push(pathInProfile(selected.value?.open ? `/sessions/${selected.value.id}` : '/')) }
  function closeSettings() {
    if (!settingsOpen.value || layerTokens(route).length) return
    returnToParent(router, pathInProfile(selected.value?.open ? `/sessions/${selected.value.id}` : '/'))
  }
  function openSessionManager() { void navigateFromSidebar({ path: pathInProfile('/sessions'), query: modalQuery() }) }
  function openSettings() { void navigateFromSidebar({ path: pathInProfile(window.matchMedia('(max-width: 830px)').matches ? '/settings' : '/settings/general'), query: modalQuery() }) }

  async function create() {
    if (busy.value) return
    await navigateFromSidebar(pathInProfile('/'))
    if (route.name !== 'home') return
    if (selected.value) conversation.clearSession()
    await nextTick()
    document.querySelector<HTMLTextAreaElement>('.sender-card textarea')?.focus()
  }

  const closeConfirmation = ref<{ count: number; stopsWork: boolean; close: () => Promise<void> } | null>(null)
  async function requestSessionClose(items: Session[], close: () => Promise<void>, confirm?: () => void, alwaysConfirm = false) {
    if (busy.value || closeConfirmation.value || !items.length) return
    const interruptsWork = items.some(item => {
      const current = sessions.value.find(session => session.id === item.id) ?? item
      const active = selected.value
      const group = sessionListActivity(current)
      return !!(group.runningId || group.executingId || active && (active.branchRootId || active.id) === item.id
        && (active.running || active.executing))
    })
    if (interruptsWork || alwaysConfirm) {
      if (confirm) confirm()
      else closeConfirmation.value = { count: items.length, stopsWork: interruptsWork, close }
    }
    else await close()
  }
  function cancelSessionClose() { if (!busy.value) closeConfirmation.value = null }
  async function confirmSessionClose() {
    const request = closeConfirmation.value
    if (!request || busy.value) return
    try { await request.close() }
    finally { if (closeConfirmation.value === request) closeConfirmation.value = null }
  }
  async function renameTitle(item: Session, title: string) {
    error.value = ''
    try {
      await client.request(`/sessions/${item.id}`, { method: 'PATCH', body: JSON.stringify({ title }) })
      return true
    } catch (cause) { error.value = String(cause); return false }
  }
  async function action(item: Session, kind: 'open' | 'close' | 'delete' | 'retry', confirmClose = false) {
    if (busy.value) return
    if (kind === 'close') return requestSessionClose([item], () => performAction(item, kind), undefined, confirmClose)
    await performAction(item, kind)
  }
  // User actions operate on a conversation, not on whichever version is open.
  async function changeSessionGroup(item: Session, kind: 'close' | 'delete') {
    const { sessionIds } = await client.request<{ sessionIds: string[] }>(`/sessions/${item.id}/group${kind === 'close' ? '/close' : ''}`, {
      method: kind === 'delete' ? 'DELETE' : 'POST',
    })
    if (kind === 'delete') for (const id of sessionIds) forgetLocalDraft(id)
    return sessionIds
  }
  function sessionGroupError(cause: unknown) {
    return cause instanceof VistaAPIError && cause.code === 'conversation_changed' ? t('sessionGroupChanged')
      : cause instanceof Error ? cause.message : String(cause)
  }
  async function leaveChangedSessions(affected: ReadonlySet<string>) {
    if (!scopeActive()) return
    if (selected.value && affected.has(selected.value.id)) conversation.clearSession()
    if (route.name === 'session' && affected.has(String(route.params.id))) await router.replace(pathInProfile('/'))
    else if (typeof route.query.session === 'string' && affected.has(route.query.session)) {
      const query = { ...route.query }
      delete query.session
      await router.replace({ path: route.path, query })
    }
  }
  async function performAction(item: Session, kind: 'open' | 'close' | 'delete' | 'retry') {
    busy.value = true
    try {
      if (kind === 'close' || kind === 'delete') {
        await leaveChangedSessions(new Set(await changeSessionGroup(item, kind)))
      } else {
        await client.request(`/sessions/${item.id}/${kind}`, { method: 'POST' })
        if (scopeActive() && kind === 'open' && sessionManagerOpen.value) await router.push(pathInProfile(`/sessions/${item.id}`))
      }
    } catch (cause) { error.value = sessionGroupError(cause) }
    finally { busy.value = false }
  }

  const routeRequests = new AbortController()
  async function syncRoute(resolved?: Session) {
    if (!scopeActive()) return
    const id = route.name === 'session' ? String(route.params.id || '')
      : typeof route.query.session === 'string' ? route.query.session : ''
    let item = resolved?.id === id ? resolved : sessions.value.find(session => session.id === id && session.open)
    if (id && !item && selected.value?.id !== id) {
      const target = workspacePath.value
      try {
        const point = await client.request<SyncCheckpoint>(`/sessions/${id}/checkpoint`, { signal: routeRequests.signal })
        if (!scopeActive() || workspacePath.value !== target) return
        item = point.session
      } catch (cause) { if (scopeActive() && workspacePath.value === target && !(cause instanceof VistaAPIError && cause.status === 404)) error.value = String(cause) }
    }
    if (item && selected.value?.id !== item.id) await conversation.selectSession(item)
    else if (route.name === 'home') conversation.clearSession()
  }
  let composing = false
  let compositionEndedAt = -Infinity
  function compositionStart() { composing = true }
  function compositionEnd() { composing = false; compositionEndedAt = performance.now() }
  function editingWithIME(event: KeyboardEvent) {
    return !isPlainEscape(event) || composing || performance.now() - compositionEndedAt < 200
  }
  function onEscape(event: KeyboardEvent) {
    if (editingWithIME(event)) return
    // Components own Escape while a layer is open, including its closing animation.
    // Modal handlers also run on window, so stopPropagation alone is insufficient.
    if (settingsOpen.value || sessionManagerOpen.value || [...document.querySelectorAll<HTMLElement>(
      '[role="dialog"], [role="alertdialog"], .ui-select-dropdown, .ui-popover, .ui-menu',
    )].some(element => element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden')) return
    if (drawerEntryOpen.value && layerTokens(route).at(-1) === 'sessions-drawer:') { event.preventDefault(); closeMobileSessions(); return }
    if (layerTokens(route).length) return
    if (!selected.value?.open || !conversation.agentWorking.value || busy.value) return
    event.preventDefault()
    void conversation.cancelExecution()
  }
  watch(workspacePath, () => {
    closeConfirmation.value = null
    void syncRoute()
  })
  onMounted(() => {
    stopActivity = trackActivity(minutes => client.request('/activity', { method: 'POST', body: JSON.stringify({ minutes }), keepalive: true }))
    window.addEventListener('compositionstart', compositionStart, true)
    window.addEventListener('compositionend', compositionEnd, true)
    window.addEventListener('keydown', onEscape)
    window.addEventListener('online', resumeState)
    window.addEventListener('pageshow', restorePage)
    document.addEventListener('visibilitychange', resumeState)
    void interactionPreferences.load(routeRequests.signal).then(() => {
      if (scopeActive()) stateSync.start(debugMode.value && !document.hidden)
    })
  })
  onUnmounted(() => {
    stopActivity?.()
    window.removeEventListener('compositionstart', compositionStart, true)
    window.removeEventListener('compositionend', compositionEnd, true)
    window.removeEventListener('keydown', onEscape)
    disposed = true
    stopProfileGuard()
    routeRequests.abort()
    stateSync.stop()
    window.removeEventListener('online', resumeState)
    window.removeEventListener('pageshow', restorePage)
    document.removeEventListener('visibilitychange', resumeState)
  })
  function workerMemory(id: string) { return serverStatus.value?.workerMetrics?.find(item => item.sessionId === id)?.memory }
  function workerAvailable(id: string) { return !!serverStatus.value?.workerMetrics?.some(item => item.sessionId === id) }
  function mib(bytes: number) { return (bytes / 1048576).toFixed(1) }
  return {
    client, stateSync, stateConnection, router, route, error, busy, debugMode, sidebar, sidebarSort, openSessions, openTotal, closedSessions, totalSessions, attentionCount, modelCatalog, serverStatus, mobileSessionsOpen, drawerEntryOpen,
    sessionsReady, modelsReady, profileId, pathInProfile, scopeActive, workspacePath, openMobileSessions, closeMobileSessions, navigateFromSidebar,
    changeSessionGroup, sessionGroupError, leaveChangedSessions,
    settingsOpen, sessionManagerOpen, needsProvider, senderDisabled, conversation, selected, modalQuery, backToWorkspace, openSessionManager,
    openSettings, closeSettings, create, action, renameTitle,
    closeConfirmation, requestSessionClose, cancelSessionClose, confirmSessionClose,
    workerMemory, workerAvailable, mib, interactionPreferences, ...presentation,
  }
}

export type Workspace = ReturnType<typeof createWorkspace>
const workspaceKey: InjectionKey<Workspace> = Symbol('vista-workspace')
export function provideWorkspace(profileId: string, desktop: Readonly<Ref<boolean>>) {
  const workspace = createWorkspace(profileId, desktop)
  provide(workspaceKey, workspace)
  return workspace
}
export function useWorkspace() {
  const workspace = inject(workspaceKey)
  if (!workspace) throw new Error('Vista workspace was not provided')
  return workspace
}
