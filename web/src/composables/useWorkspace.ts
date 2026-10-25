import { computed, inject, nextTick, onMounted, onUnmounted, provide, ref, watch, type InjectionKey } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { createVistaClient, VistaAPIError } from '../client/vista'
import { createWorkspaceSync, type StateTopic, type StateConnection, type WorkspaceSnapshots } from '../client/workspaceSync'
import type { ModelCatalog, ServerStatus, Session, SyncCheckpoint } from '../client/types'
import { useConversation } from './useConversation'
import { usePresentation } from './usePresentation'
import { useInteractionPreferences } from './useInteractionPreferences'
import { isPlainEscape } from './useEscape'
import { mergeSessionSnapshot, sessionNeedsAttention } from '../state/workspace'
import { quoteStoragePrefix } from '../state/quotedReply'

function createWorkspace() {
  const route = useRoute()
  const router = useRouter()
  const { t } = useI18n({ useScope: 'global' })
  const client = createVistaClient()
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
  const sessions = ref<Session[]>([])
  const modelCatalog = ref<ModelCatalog>({ default: '', models: [] })
  const serverStatus = ref<ServerStatus | null>(null)
  const mobileSessionsOpen = ref(false)
  const openSessions = computed(() => sessions.value.filter(session => session.open))
  const closedSessions = computed(() => serverStatus.value?.sessions.closed ?? 0)
  const totalSessions = computed(() => serverStatus.value?.sessions.total ?? sessions.value.length)
  const attentionCount = computed(() => openSessions.value.filter(session => sessionNeedsAttention(session,
    serverStatus.value ? workerAvailable(session.id) : undefined)).length)
  const settingsOpen = computed(() => route.name === 'settings' || String(route.name).startsWith('settings-'))
  const sessionManagerOpen = computed(() => route.name === 'session-manager')
  const needsProvider = computed(() => !modelCatalog.value.models.some(model => model.provider))
  const presentation = usePresentation(modelCatalog)
  const interactionPreferences = useInteractionPreferences(client, cause => { error.value = String(cause) })
  const revisions = { sessions: 0, models: 0, status: 0 }
  const stateConnection = ref<StateConnection>({ phase: 'connecting' })
  const stateSync = createWorkspaceSync({
    open: (metrics, signal) => fetch(`/api/events${metrics ? '?metrics=1' : ''}`, { signal }),
    status: value => { stateConnection.value = value },
    metrics: value => {
      if (!serverStatus.value || Date.parse(value.sampledAt) >= Date.parse(serverStatus.value.sampledAt)) {
        revisions.status++
        serverStatus.value = value
      }
    },
  })
  watch(debugMode, value => stateSync.setMetrics(value && !document.hidden))
  let disposed = false
  const conversation = useConversation({ client, sessions, modelCatalog, error, busy, refresh: refreshSessions, translate: t, interactionPreferences,
    onSessionStarted: async session => { await router.push(`/sessions/${session.id}`) } })
  const { selected, selectedModel } = conversation

  async function refreshTopics(topics: StateTopic[], signal?: AbortSignal, pushed: WorkspaceSnapshots = {}) {
    await Promise.all(topics.map(async topic => {
      if (topic !== 'sessions' && topic !== 'status' && topic !== 'models') return
      const revision = ++revisions[topic]
      const started = selected.value ? { sessionId: selected.value.id, eventSequence: conversation.lastEvent.value } : null
      if (topic === 'sessions') {
        let snapshot = pushed.sessions?.items ?? await client.request<Session[]>('/sessions?open=true', { signal })
        if (disposed || signal?.aborted || revision !== revisions.sessions) return
        const activeID = selected.value?.id
        const routedID = route.name === 'session' ? String(route.params.id) : ''
        const lookupID = activeID && !snapshot.some(item => item.id === activeID) ? activeID
          : routedID && !snapshot.some(item => item.id === routedID) ? routedID : ''
        let point: SyncCheckpoint | undefined
        if (lookupID) {
          // Omission from the open list can mean closed, deleted, or created
          // after this snapshot. Verify just that conversation, never archives.
          try { point = await client.request<SyncCheckpoint>(`/sessions/${lookupID}/checkpoint`, { signal }) }
          catch (cause) { if (!(cause instanceof VistaAPIError && cause.status === 404)) throw cause }
          if (disposed || signal?.aborted || revision !== revisions.sessions) return
          if (selected.value?.id !== activeID) return refreshTopics(['sessions'], signal, {
            ...pushed, sessions: { items: snapshot, throughSequences: pushed.sessions?.throughSequences ?? {} },
          })
          if (point?.session.open) snapshot = [...snapshot, point.session]
        }
        const boundary = selected.value ? {
          sessionId: selected.value.id,
          eventSequence: point?.session.id === selected.value.id ? point.throughSequence
            : pushed.sessions?.throughSequences[selected.value.id]
              ?? (started?.sessionId === selected.value.id ? started.eventSequence : 0),
        } : null
        const list = mergeSessionSnapshot(snapshot, selected.value, boundary, Math.max(conversation.lastEvent.value, conversation.activeRunSequence.value))
        sessions.value = list.filter(item => item.open)
        if (selected.value) {
          const current = list.find(item => item.id === selected.value?.id)
            ?? (point?.session.id === selected.value.id ? mergeSessionSnapshot([point.session], selected.value, boundary,
              Math.max(conversation.lastEvent.value, conversation.activeRunSequence.value))[0] : undefined)
          if (current) selected.value = current
          else conversation.clearSession()
        }
        if (route.name === 'session') {
          const routed = list.find(item => item.id === route.params.id) ?? (point?.session.id === route.params.id ? point.session : undefined)
          if (!routed) await router.replace('/')
          else if (!routed.open && selected.value?.id !== routed.id) await router.replace('/sessions')
        }
        // A new session can receive a list push before its first send leaves
        // home. Only actual route navigation may clear that in-flight scope.
        if (route.name !== 'home') void syncRoute()
      } else if (topic === 'status') {
        const status = pushed.status ?? await client.request<ServerStatus>('/status', { signal })
        if (!disposed && !signal?.aborted && revision === revisions.status
          && (!serverStatus.value || Date.parse(status.sampledAt) >= Date.parse(serverStatus.value.sampledAt))) serverStatus.value = status
      } else {
        const catalog = pushed.models ?? await client.request<ModelCatalog>('/models', { signal })
        if (disposed || signal?.aborted || revision !== revisions.models) return
        modelCatalog.value = catalog
        if (!catalog.models.some(model => model.key === selectedModel.value)) {
          selectedModel.value = catalog.models.some(model => model.key === selected.value?.modelKey) ? selected.value!.modelKey : catalog.default
        }
      }
    }))
  }
  stateSync.subscribe(['sessions', 'status', 'models'], refreshTopics)
  async function refreshSessions() {
    try { await refreshTopics(['sessions', 'status']) }
    catch (cause) { if (!disposed) error.value = String(cause) }
  }
  function resumeState() {
    if (!document.hidden) {
      if (!stateSync.setMetrics(debugMode.value)) stateSync.reconnect()
    } else stateSync.setMetrics(false)
  }

  function restorePage(event: PageTransitionEvent) { if (event.persisted) resumeState() }

  function modalQuery() { return selected.value?.open ? { session: selected.value.id } : {} }
  function backToWorkspace() { void router.push(selected.value?.open ? `/sessions/${selected.value.id}` : '/') }
  function openSessionManager() { void router.push({ path: '/sessions', query: modalQuery() }) }
  function openSettings() { void router.push({ path: window.matchMedia('(max-width: 830px)').matches ? '/settings' : '/settings/general', query: modalQuery() }) }

  async function create() {
    if (busy.value) return
    mobileSessionsOpen.value = false
    await router.push('/')
    if (selected.value) conversation.clearSession()
    await nextTick()
    document.querySelector<HTMLTextAreaElement>('.sender-card textarea')?.focus()
  }

  const renamingSessionID = ref('')
  const sessionTitleDraft = ref('')
  const deleteTarget = ref<Session | null>(null)
  function beginRename(item: Session) { renamingSessionID.value = item.id; sessionTitleDraft.value = item.title }
  function cancelRename() { renamingSessionID.value = ''; sessionTitleDraft.value = '' }
  async function rename(item: Session, title: string, done: () => void) {
    if (!title || title.length > 200) return
    busy.value = true
    try {
      await client.request(`/sessions/${item.id}`, { method: 'PATCH', body: JSON.stringify({ title }) })
      done()
    } catch (cause) { error.value = String(cause) }
    finally { busy.value = false }
  }
  function saveRename(item: Session) { return rename(item, sessionTitleDraft.value.trim(), cancelRename) }
  async function action(item: Session, kind: 'open' | 'close' | 'delete' | 'retry') {
    busy.value = true
    try {
      await client.request(`/sessions/${item.id}${kind === 'delete' ? '' : '/' + kind}`, { method: kind === 'delete' ? 'DELETE' : 'POST' })
      if (kind === 'delete') try {
        localStorage.removeItem('vista-session-draft:' + item.id)
        localStorage.removeItem(quoteStoragePrefix + item.id)
      } catch { /* Storage can be unavailable. */ }
      if ((kind === 'close' || kind === 'delete') && selected.value?.id === item.id) {
        conversation.clearSession()
        if (route.name === 'session') await router.replace('/')
        else if (route.query.session === item.id) await router.replace({ path: route.path })
      }
      if (kind === 'open' && sessionManagerOpen.value) await router.push(`/sessions/${item.id}`)
    } catch (cause) { error.value = String(cause) }
    finally { busy.value = false }
  }
  function updateSessionDelete(show: boolean, item: Session) {
    if (show) deleteTarget.value = item
    else if (deleteTarget.value?.id === item.id) deleteTarget.value = null
  }
  async function confirmDelete() {
    const item = deleteTarget.value
    if (!item) return
    deleteTarget.value = null
    await action(item, 'delete')
  }

  async function syncRoute() {
    const id = route.name === 'session' ? String(route.params.id || '')
      : typeof route.query.session === 'string' ? route.query.session : ''
    const item = sessions.value.find(session => session.id === id && session.open)
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
  function dismissRename(event: KeyboardEvent) {
    if (editingWithIME(event) || !renamingSessionID.value) return
    event.preventDefault()
    event.stopPropagation()
    cancelRename()
  }
  function onEscape(event: KeyboardEvent) {
    if (editingWithIME(event)) return
    // Components own Escape while a layer is open, including its closing animation.
    // Modal handlers also run on window, so stopPropagation alone is insufficient.
    if (settingsOpen.value || sessionManagerOpen.value || [...document.querySelectorAll<HTMLElement>(
      '[role="dialog"], [role="alertdialog"], .ant-select-dropdown, .ant-popover, .ant-dropdown',
    )].some(element => element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden')) return
    if (renamingSessionID.value) { dismissRename(event); return }
    if (mobileSessionsOpen.value) { event.preventDefault(); mobileSessionsOpen.value = false; return }
    if (!selected.value?.open || !conversation.agentWorking.value || busy.value) return
    event.preventDefault()
    void conversation.cancelExecution()
  }
  watch(() => route.fullPath, () => {
    mobileSessionsOpen.value = false
    deleteTarget.value = null
    void syncRoute()
  })
  onMounted(() => {
    window.addEventListener('compositionstart', compositionStart, true)
    window.addEventListener('compositionend', compositionEnd, true)
    window.addEventListener('keydown', onEscape)
    window.addEventListener('online', resumeState)
    window.addEventListener('pageshow', restorePage)
    document.addEventListener('visibilitychange', resumeState)
    void interactionPreferences.load().then(() => {
      if (!disposed) stateSync.start(debugMode.value && !document.hidden)
    })
  })
  onUnmounted(() => {
    window.removeEventListener('compositionstart', compositionStart, true)
    window.removeEventListener('compositionend', compositionEnd, true)
    window.removeEventListener('keydown', onEscape)
    disposed = true
    stateSync.stop()
    window.removeEventListener('online', resumeState)
    window.removeEventListener('pageshow', restorePage)
    document.removeEventListener('visibilitychange', resumeState)
  })
  function workerMemory(id: string) { return serverStatus.value?.workerMetrics?.find(item => item.sessionId === id)?.memory }
  function workerAvailable(id: string) { return !!serverStatus.value?.workerMetrics?.some(item => item.sessionId === id) }
  function mib(bytes: number) { return (bytes / 1048576).toFixed(1) }
  return {
    client, stateSync, stateConnection, router, route, error, busy, debugMode, sessions, openSessions, closedSessions, totalSessions, attentionCount, modelCatalog, serverStatus, mobileSessionsOpen,
    settingsOpen, sessionManagerOpen, needsProvider, conversation, selected, modalQuery, backToWorkspace, openSessionManager,
    openSettings, create, action, renamingSessionID, sessionTitleDraft, beginRename, cancelRename, saveRename,
    deleteTarget, updateSessionDelete, confirmDelete,
    workerMemory, workerAvailable, mib, interactionPreferences, ...presentation,
  }
}

export type Workspace = ReturnType<typeof createWorkspace>
const workspaceKey: InjectionKey<Workspace> = Symbol('vista-workspace')
export function provideWorkspace() {
  const workspace = createWorkspace()
  provide(workspaceKey, workspace)
  return workspace
}
export function useWorkspace() {
  const workspace = inject(workspaceKey)
  if (!workspace) throw new Error('Vista workspace was not provided')
  return workspace
}
