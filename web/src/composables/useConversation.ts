import { profileStorageKey } from '../client/profileScope.ts'
import { serviceDrafts, type DraftInput } from './serviceDrafts.ts'
import { computed, onScopeDispose, reactive, ref, toRefs, watch, type Ref } from 'vue'
import { VistaAPIError, restoreConversationSnapshot, createConversationSync, appendMessage, createConversationState, messageResults, runResults, pruneConversationWindow, reduceConversationEvent, replyTraceSegments, trailingTraceSegment, conversationItems, displayBodyVersion, displayOutputKey, hydrateMainOutput, mainOutputKey, mainOutputText, mergeMainOutputs, confirmInputEvent, projectInputQueue, jobActive } from '@vista/client'
import type { VistaClient, ConversationSyncStatus, ConversationSyncDiagnostics, Artifact, ConversationSnapshot, CompactionStatus, ContextUsage, Job, MainOutput, Message, ModelCatalog, OutputView, QuestionAnswer, QueuedInput, SendMessageResult, Session, SessionActionPreview, StreamEvent, ReplyTraceSegment, ConversationItem, MainOutputRecord } from '@vista/client'
import { toolOperationLabelKey, compactTraceText, toolSummaryText } from '../state/runTrace.ts'
import { useInteractionPreferences, type InteractionPreferences } from './useInteractionPreferences.ts'
import { formatQuotedReply, quoteStoragePrefix, readQuotedReply, type QuotedReply, type QuotedText } from '../state/quotedReply.ts'
import { reasoningChoiceLabel } from '../ui/reasoning.ts'
import { configuredConversationModel, visibleModels } from '../state/modelVisibility.ts'
import { failureAnalysisPrompt, type FailureContext } from '../state/failureAnalysis.ts'

export type ConversationOptions = {
  client: VistaClient
  sessions: Ref<Session[]>
  modelCatalog: Ref<ModelCatalog>
  error: Ref<string>
  busy: Ref<boolean>
  refresh: () => Promise<void>
  translate: (key: string, params?: Record<string, unknown>) => string
  interactionPreferences?: InteractionPreferences
  serverDrafts?: boolean
  onSessionStarted?: (session: Session) => Promise<void>
}
type Scope = { id: string; generation: number; controller: AbortController; initialSelectionVersion?: number; sync?: ReturnType<typeof createConversationSync> }
export const draftStoragePrefix = profileStorageKey('vista-session-draft:')
export const welcomeDraftStorageKey = profileStorageKey('vista-home-draft')

function readDraft(id: string) {
  try { return localStorage.getItem(draftStoragePrefix + id) || '' } catch { return '' }
}

function mergeOutputSnapshot<T extends { id: string }>(snapshot: T[], current: T[], before: Map<string, T>): T[] {
  const newer = current.filter(item => before.get(item.id) !== item)
  const newerIDs = new Set(newer.map(item => item.id))
  return [...newer, ...snapshot.filter(item => !newerIDs.has(item.id))]
}

export function useConversation({ client, sessions, modelCatalog, error, busy, refresh, translate: t, interactionPreferences = useInteractionPreferences(), serverDrafts = false, onSessionStarted }: ConversationOptions) {
  const { rememberedModel, reasoningByModelId: reasoningByModel } = interactionPreferences
  const state = reactive(createConversationState())
  const synchronization = ref<ConversationSyncStatus>({ phase: 'ready' })
  const syncDiagnostics = ref<ConversationSyncDiagnostics | null>(null)
  const snapshotReady = ref(false)
  const viewRevision = ref(0)
  let acknowledgedReplySequence = 0
  let auxiliaryRevision = 0
  const refs = toRefs(state)
  const { selected, messages, processEvents, modelDrafts, modelResults, activeRunID, runProgress, jobs, artifacts } = refs
  const olderActivity = ref<StreamEvent[]>([])
  const hasOlderActivity = ref(false)
  const activityPaging = ref(false)
  const paging = ref(false)
  const selectedModel = ref('')
  const selectedModelInfo = computed(() => visibleModels(modelCatalog.value).find(model => model.key === selectedModel.value))
  const pendingBusy = ref<Record<string, boolean>>({})
  const compaction = ref<CompactionStatus | null>(null)
  const contextUsage = ref<ContextUsage | null>(null)
  let contextUsageRequest = 0
  const compactionRequestPending = ref(false)
  const manualCompacting = computed(() => !!selected.value?.executing && !!compaction.value?.manual
    && (compaction.value.phase === 'compacting' || maintenanceRunID === compaction.value.runId))
  let maintenanceRunID = ''
  const continueAfterCompaction = computed(() => !!selected.value?.open && !selected.value.executing && !busy.value
    && !draft.value && !quotedTexts.value.length && !attachedImages.value.length && compaction.value?.manual && compaction.value.phase === 'compacted'
    && (compaction.value.sequence || 0) > latestUserSequence.value)
  const latestUserSequence = ref(0)
  const canCompact = computed(() => !!selected.value?.open && !selected.value.executing && !busy.value
    && !compactionRequestPending.value && messages.value.length > 1 && !!selectedModel.value)
  const modelOutputLoading = ref<Record<string, boolean>>({})
  const modelOutputFailure = ref<Record<string, string>>({})
  const modelOutputRequests = new Map<string, Promise<OutputView>>()
  const mainOutputRequests = new Map<string, Promise<MainOutputRecord>>()
  const indexedRuns = new Map<string, { request: Promise<boolean> }>()
  const displayMessages = computed(() => conversationItems(messages.value, state.mainOutputs, state.hasOlder, state.hasNewer))
  const inputQueue = computed(() => projectInputQueue(state))
  const pendingInputs = computed(() => inputQueue.value.items)
  const inputQueuePaused = computed(() => inputQueue.value.paused)
  const selectedReasoningKey = computed(() => selectedModelInfo.value?.id || '')
  // A session snapshot restores the selected effort, but a delayed list
  // projection must not undo a newer local keyboard or pointer change.
  const localReasoning = ref<{ modelKey: string; effort: string }>()
  const selectedReasoning = computed({
    get: () => {
      const choices = selectedModelInfo.value?.reasoningOptions || []
      const remembered = (localReasoning.value?.modelKey === selectedModel.value ? localReasoning.value.effort : undefined) || reasoningByModel.value[selectedReasoningKey.value]
        || (rememberedModel.value?.id === selectedReasoningKey.value ? rememberedModel.value.reasoningEffort : '')
      return choices.includes(remembered) ? remembered : choices.find(value => value !== 'off') || ''
    },
    set: (effort: string) => {
      if (!selectedReasoningKey.value || !selectedModelInfo.value?.reasoningOptions?.includes(effort)) return
      localReasoning.value = { modelKey: selectedModel.value, effort }
      reasoningByModel.value = { ...reasoningByModel.value, [selectedReasoningKey.value]: effort }
      void saveSelection({ manualReasoningEffort: effort })
      rememberSelectedModel()
    },
  })
  const reasoningChoices = computed(() => (selectedModelInfo.value?.reasoningOptions || []).map(value => ({ label: reasoningChoiceLabel(value, t), value })))
  const canAttachImage = computed(() => !!selectedModelInfo.value?.inputModalities?.includes('image'))
  const attachedImages = ref<File[]>([])
  const attachedPreviewURLs = ref<string[]>([])
  const draft = ref(readWelcomeDraft())
  const quotedReply = ref<QuotedReply | null>(null)
  const quotedTexts = computed(() => quotedReply.value?.items || [])
  const editingMessage = ref<Message | null>(null)
  const rollbackFailure = ref('')
  const analyzingFailure = ref('')
  const canAnalyzeFailure = computed(() => interactionPreferences.ready.value && !!selectedModelInfo.value?.provider)
  let editBackup: { draft: string; quote: QuotedReply | null; images: File[] } | null = null
  type ImageRef = { hash: string; name: string; mediaType: string; bytes: number }
  type MessageDraft = { message: Message; images: ImageRef[]; session?: Session }
  let draftRevision = 0
  let recallingScope: Scope | null = null
  let welcomeImages: File[] = []
  const analysisImageDrafts = new Map<string, File[]>()
  let restoringDraft = false
  let firstSendController: AbortController | null = null
  const draftKey = () => selected.value?.id || 'home'
  const inputs = (): DraftInput => ({ text: draft.value, quote: quotedReply.value, files: [...attachedImages.value] })
  const persisted = serverDrafts ? serviceDrafts(client, (key, value) => {
    if (draftKey() !== key || editingMessage.value) return
    restoringDraft = true
    draft.value = value.text; quotedReply.value = value.quote; attachedImages.value = value.files
    restoringDraft = false
  }) : undefined
  let loadedDraftKey: string | undefined
  function selectDraft() {
    if (!persisted) return
    const key = loadedDraftKey = draftKey()
    const textKey = key === 'home' ? welcomeDraftStorageKey : draftStoragePrefix + key
    const oldText = draft.value, oldQuote = quotedReply.value
    persisted.select(key, inputs(), () => {
      try {
        if (localStorage.getItem(textKey) === oldText) localStorage.removeItem(textKey)
        if (oldQuote && JSON.stringify(readQuotedReply(key)) === JSON.stringify(oldQuote)) localStorage.removeItem(quoteStoragePrefix + key)
      } catch { /* Imported and still intact on the server. */ }
    },currentScope?.controller.signal)
  }
  async function refreshDraft(signal?: AbortSignal) {
    if (editingMessage.value || busy.value) return
    if (loadedDraftKey !== draftKey()) { selectDraft(); return }
    await persisted?.refresh(draftKey(),signal)
  }
  function retryDraft() {
    if (persisted?.phase.value === 'loadFailed') void refreshDraft(currentScope?.controller.signal)
    else void persisted?.save(draftKey(),inputs()).catch(() => {})
  }
  function stageDraft() { if (!restoringDraft && !editingMessage.value) persisted?.change(draftKey(),inputs()) }

  function readWelcomeDraft() {
    try { return localStorage.getItem(welcomeDraftStorageKey) || '' } catch { return '' }
  }
  function initializeWelcomeSelection() {
    if (selected.value) return
    selectedModel.value = configuredConversationModel(modelCatalog.value)
  }

  function rememberSelectedModel() {
    const model = selectedModelInfo.value
    if (model) rememberedModel.value = { key: model.key, id: model.id, reasoningEffort: selectedReasoning.value }
  }
  let selectionVersion = 0
  let selectionSave = Promise.resolve()
  async function saveSelection(patch: Partial<Session>) {
    const scope = currentScope
    if (!scope || !current(scope)) return
    const version = ++selectionVersion
    if (selected.value) selected.value = { ...selected.value, ...patch }
    try {
      const update = selectionSave.then(() => request<Session>(scope, `/sessions/${scope.id}/selection`, { method: 'PATCH', body: JSON.stringify(patch) }))
      selectionSave = update.then(() => {}, () => {})
      const updated = await update
      if (current(scope) && version === selectionVersion && updated?.id) {
        const choice = { manualModelKey: updated.manualModelKey, manualReasoningEffort: updated.manualReasoningEffort, processingTier: updated.processingTier }
        selected.value = { ...selected.value!, ...choice }
        sessions.value = sessions.value.map(item => item.id === updated.id ? { ...item, ...choice } : item)
        if (!selected.value.executing) void refreshContextUsage()
      }
    } catch (failure) { report(scope, failure) }
  }
  const selectedProcessingTier = ref('')
  const discoveredTiers = ref<string[] | null>(null)
  const processingTiers = computed(() => discoveredTiers.value ?? selectedModelInfo.value?.processingTiers ?? [])
  watch([() => selectedModelInfo.value?.key, () => modelCatalog.value.settingsRevision], (_, __, onCleanup) => {
    const model = selectedModelInfo.value
    discoveredTiers.value = null
    if (!model || !['openai-codex', 'openrouter', 'cerebras', 'groq', 'mistral'].includes(model.providerId || '')) return
    const key = model.key
    const controller = new AbortController()
    onCleanup(() => controller.abort())
    void client.request<{ processingTiers?: string[] }>(`/model-processing-tiers?key=${encodeURIComponent(key)}`, { signal: controller.signal }).then(value => {
      if (!controller.signal.aborted && selectedModel.value === key) discoveredTiers.value = value?.processingTiers || []
    }).catch(() => { /* No advertised tier is safer than a guess. */ })
  }, { immediate: true })
  function chooseProcessingTier(value: string) {
    if (!selectedModelInfo.value || (value && !processingTiers.value.includes(value))) return
    selectedProcessingTier.value = value
    void saveSelection({ manualModelKey: selectedModel.value, processingTier: value })
  }
  const agentWorking = computed(() => !manualCompacting.value && !!selected.value?.executing && !(runProgress.value?.runId === activeRunID.value && runProgress.value.phase === 'finished'))
  const compacting = computed(() => manualCompacting.value || compactionRequestPending.value
    || agentWorking.value && compaction.value?.runId === activeRunID.value && compaction.value.phase === 'compacting')
  const compactionResult = computed(() => !compacting.value && !agentWorking.value
    && ['failed', 'interrupted'].includes(compaction.value?.phase || '')
    && (compaction.value?.sequence || 0) > latestUserSequence.value ? compaction.value : null)
  const agentStatus = computed(() => {
    if (synchronization.value.phase === 'error') return t('conversationSyncFailed')
    if (['connecting', 'recovering'].includes(synchronization.value.phase)) return t('conversationSynchronizing')
    if (compacting.value) return t('contextCompacting')
    const progress = runProgress.value?.runId === activeRunID.value ? runProgress.value : null
    if (progress?.phase === 'retry' && progress.retry) return t('modelRetryWaiting', progress.retry)
    if (progress?.phase === 'tool') return [t(toolOperationLabelKey(progress.tool)), compactTraceText(toolSummaryText(progress.tool, progress.summary), 90)].filter(Boolean).join(' · ')
    const key = { model: 'agentWaitingModel', reasoning: 'agentReasoning', response: 'agentResponding', processing: 'agentProcessing' }[progress?.phase as 'model' | 'reasoning' | 'response' | 'processing']
    return t(key || 'agentStarting')
  })
  const hasTaskDetails = computed(() => !!(olderActivity.value.some(event => !hasVisibleRun(event.data?.runId)) || hasOlderActivity.value))
  const sessionAction = ref<SessionActionPreview | null>(null)
  let currentScope: Scope | null = null
  let generation = 0
  let busyOwner: symbol | null = null
  let recentOldestEvent = 0
  let recentHasOlderActivity = false
  let olderActivityCursor = 0
  let readingSequence = 0

  function current(scope: Scope): boolean {
    return currentScope === scope && generation === scope.generation && selected.value?.id === scope.id && !scope.controller.signal.aborted
  }
  async function request<T>(scope: Scope, path: string, options: RequestInit = {}) {
    return client.request<T>(path, { ...options, signal: scope.controller.signal })
  }
  function report(scope: Scope, failure: unknown) { if (current(scope)) error.value = String(failure) }
  function startBusy() { const owner = Symbol(); busyOwner = owner; busy.value = true; return owner }
  function endBusy(scope: Scope, owner: symbol) { if (current(scope) && busyOwner === owner) { busyOwner = null; busy.value = false } }
  function addMessage(message: Message | null | undefined) { Object.assign(state, appendMessage(state, message)) }
  async function markRead(sequence: number) {
    const scope = currentScope
    if (!scope || !current(scope) || sequence <= Math.max(selected.value?.readSequence || 0, readingSequence)) return
    readingSequence = sequence
    try {
      const result = await request<{ readSequence: number }>(scope, `/sessions/${scope.id}/read`, {
        method: 'POST', body: JSON.stringify({ sequence }),
      })
      if (!current(scope)) return
      const readSequence = Math.max(selected.value?.readSequence || 0, result.readSequence)
      const updated = { ...selected.value!, readSequence }
      selected.value = updated
      sessions.value = sessions.value.map(item => item.id === scope.id ? { ...item, ...updated } : item)
    } catch { /* Retry on the next visible snapshot; a read receipt must not interrupt work. */ }
    finally { if (current(scope) && readingSequence === sequence) readingSequence = 0 }
  }
  function applyContextUsage(usage: ContextUsage | null | undefined, sequence: number) {
    if (!usage || !Number.isFinite(usage.tokens) || usage.tokens < 0 || sequence < (contextUsage.value?.sequence || 0)) return
    contextUsage.value = { ...usage, sequence }
  }
  async function refreshContextUsage() {
    const scope = currentScope
    if (!scope || !current(scope)) return
    const requestID = ++contextUsageRequest
    try {
      const usage = await request<ContextUsage | null>(scope, `/sessions/${scope.id}/context-usage`)
      if (current(scope) && requestID === contextUsageRequest) applyContextUsage(usage, usage?.sequence || 0)
    } catch { /* A display estimate must not interrupt the conversation. */ }
  }
  function applyCompaction(status: CompactionStatus | null | undefined, sequence: number, terminal = true) {
    if (!status?.runId || !['compacting', 'compacted', 'failed', 'interrupted'].includes(status.phase) || sequence < (compaction.value?.sequence || 0)) return
    const incomingPhase = status.phase
    // Preserve a committed manual checkpoint, not every automatic recovery in
    // the same run. The incoming lifecycle still owns the maintenance slot.
    if (status.manual && status.runId === compaction.value?.runId && compaction.value.phase === 'compacted' && status.phase !== 'compacted')
      status = { ...compaction.value }
    compaction.value = { ...status, sequence }
    if (!status.manual || sequence < state.activeRunSequence || !selected.value) return
    if (incomingPhase === 'compacting') {
      maintenanceRunID = status.runId
      selected.value = { ...selected.value, executing: true }
    } else if (terminal) {
      maintenanceRunID = ''
      selected.value = { ...selected.value, executing: false }
    }
  }
  async function compactSession() {
    const scope = currentScope
    if (!scope || !current(scope) || !canCompact.value) return
    compactionRequestPending.value = true
    try {
      const result = await request<{ runId: string; event: StreamEvent }>(scope, `/sessions/${scope.id}/compact`, {
        method: 'POST', body: JSON.stringify({ modelKey: selectedModel.value, reasoningEffort: selectedReasoning.value }),
      })
      if (!current(scope)) return
      // Do not advance the SSE cursor: intervening events still need replay.
      applyCompaction(result.event.data, result.event.sequence)
      error.value = ''
    } catch (failure) { report(scope, failure) }
    finally { if (current(scope)) compactionRequestPending.value = false }
  }
  function fillContinueDraft() {
    if (!continueAfterCompaction.value) return false
    draft.value = t('continueConversation')
    return true
  }
  function handleEvent(event: StreamEvent, live = true) {
    if (event.sequence <= state.lastEvent) return
    const scope = currentScope
    if (!Number.isSafeInteger(event.sequence) || typeof event.kind !== 'string' || !event.data || typeof event.data !== 'object') throw new Error('Invalid conversation event')
    const next = reduceConversationEvent(state, event)
    Object.assign(state, next)
    const reply = event.kind === 'run_finished' ? event.data.message : event.kind === 'message' ? event.data : null
    if (reply?.role === 'assistant') acknowledgedReplySequence = Math.max(acknowledgedReplySequence, reply.sequence)
    try {
      if (event.kind === 'message' && event.data?.role === 'user') {
        latestUserSequence.value = Math.max(latestUserSequence.value, event.sequence)
        maintenanceRunID = ''
      }
      if (event.kind === 'context_status') applyCompaction(event.data, event.sequence)
      if (event.kind === 'context_usage' && !event.data?.depth) applyContextUsage(event.data, event.sequence)
      if (event.kind === 'context_notice' && !event.data?.depth) applyContextUsage(event.data?.contextUsage, event.sequence)
      if (event.kind === 'context_notice' && !event.data?.depth) applyCompaction(event.data?.context, event.sequence, false)
      if (event.kind === 'run_finished' && event.data?.runId === compaction.value?.runId && compaction.value?.phase === 'compacting') {
        const interrupted = ['interrupted', 'canceled', 'cancelled'].includes(event.data?.status)
        applyCompaction({ ...compaction.value, phase: interrupted ? 'interrupted' : 'failed',
          error: event.data?.message?.content || event.data?.reason || t(interrupted ? 'contextInterrupted' : 'contextUnavailable') }, event.sequence)
      }
      if (['session_title', 'session_renamed', 'message', 'run_finished'].includes(event.kind) || event.kind.startsWith('input_')) {
        const liveSession = selected.value
        if (liveSession) sessions.value = sessions.value.map(item => item.id === liveSession.id
          ? { ...item, title: liveSession.title, executing: liveSession.executing, pendingReply: liveSession.pendingReply,
            latestReplySequence: liveSession.latestReplySequence, latestRunStatus: liveSession.latestRunStatus,
            pendingInputCount: liveSession.pendingInputCount, inputQueuePaused: liveSession.inputQueuePaused } : item)
      }
      if (live && (event.kind === 'run_finished' || event.kind === 'context_status' && event.data?.manual && event.data?.phase !== 'compacting')) void refreshContextUsage()
    } catch (failure) { scope?.sync?.diagnostic('event side effect failed', failure) }
  }
  function applySnapshot(scope: Scope, snapshot: ConversationSnapshot) {
    if (!current(scope) || snapshot.throughSequence < state.lastEvent) return
    // Routing can start from a workspace list captured before the first send.
    // Restore the saved choice once, unless the user has since edited it.
    // Locally created first-send scopes keep their not-yet-persisted choice.
    const restore = scope.initialSelectionVersion === selectionVersion
    delete scope.initialSelectionVersion
    if (restore) restoreSelection(snapshot.session)
    const choice = restore ? snapshot.session : selected.value
    const old = state
    const updated = { ...snapshot.session, readSequence: Math.max(snapshot.session.readSequence || 0, selected.value?.readSequence || 0),
      manualModelKey: choice?.manualModelKey, manualReasoningEffort: choice?.manualReasoningEffort, processingTier: choice?.processingTier }
    const rollback = (snapshot.revertedSequence || 0) > old.lastEvent
    const next = restoreConversationSnapshot(old, snapshot, updated)
    // In-flight indexes/bodies belong to the view they started from. Reload
    // indexes after replacement, including runs whose IDs have not changed.
    indexedRuns.clear(); modelOutputRequests.clear(); mainOutputRequests.clear()
    modelOutputLoading.value = {}; modelOutputFailure.value = {}
    if (rollback) {
      olderActivity.value = []; hasOlderActivity.value = false
      compaction.value = null; contextUsage.value = null; maintenanceRunID = ''
      contextUsageRequest++
    }
    viewRevision.value++
    Object.assign(state, next)
    snapshotReady.value = true
    acknowledgedReplySequence = snapshot.session.latestReplySequence || 0
    latestUserSequence.value = next.activeRunSequence
    recentOldestEvent = snapshot.eventsBefore || snapshot.events[0]?.sequence || 0
    recentHasOlderActivity = snapshot.hasOlderEvents
    if (!olderActivity.value.length) { olderActivityCursor = recentOldestEvent; hasOlderActivity.value = recentHasOlderActivity }
    for (const event of snapshot.events) {
      if (event.kind === 'context_status') applyCompaction(event.data, event.sequence)
      if (event.kind === 'context_usage' && !event.data.depth) applyContextUsage(event.data, event.sequence)
    }
    void loadAuxiliary(scope)
    void refreshContextUsage()
  }
  function connectStream(scope: Scope) {
    if (scope.sync) return scope.sync.start()
    const sync = scope.sync = createConversationSync({
      client, sessionId: scope.id, scope: scope.generation, signal: scope.controller.signal,
      cursor: () => state.lastEvent, replySequence: () => acknowledgedReplySequence,
      inputQueue: () => ({ count: state.queuedInputs.length, paused: state.inputQueuePaused }),
      applyEvent: (event, live) => { if (current(scope)) handleEvent(event, live) },
      applySnapshot: snapshot => applySnapshot(scope, snapshot),
      applySessionAction: action => { if (current(scope)) sessionAction.value = action },
      applyCheckpoint: point => {
        if (!current(scope) || !selected.value) return
        const previous = selected.value
        const verified = { ...point.session, readSequence: Math.max(point.session.readSequence || 0, previous.readSequence || 0),
              manualModelKey: previous.manualModelKey, manualReasoningEffort: previous.manualReasoningEffort, processingTier: previous.processingTier }
        if (state.activeRunSequence > point.throughSequence) {
          verified.executing = previous.executing
          verified.pendingReply = previous.pendingReply
          verified.latestRunStatus = previous.latestRunStatus
        }
        const queue = inputQueue.value
        verified.inputQueuePaused = queue.paused; verified.pendingInputCount = queue.items.length
        selected.value = verified
        sessions.value = sessions.value.map(item => item.id === scope.id ? verified : item)
      },
      status: status => { if (current(scope)) synchronization.value = status },
      diagnostics: value => { if (current(scope)) syncDiagnostics.value = value },
    })
    const { signal } = scope.controller
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') sync.reconnect('foreground')
    }, { signal })
    if (typeof window !== 'undefined') window.addEventListener('online', () => sync.reconnect('online'), { signal })
    return sync.start()
  }
  function resynchronize() { currentScope?.sync?.reconnect('manual recovery', true) }
  function renderFailure(failure: unknown) { currentScope?.sync?.diagnostic('render failed', failure) }

  watch(draft, value => {
    draftRevision++
    if (restoringDraft) return
    if (persisted) { stageDraft(); return }
    const key = selected.value ? draftStoragePrefix + selected.value.id : welcomeDraftStorageKey
    try {
      if (value) localStorage.setItem(key, value)
      else localStorage.removeItem(key)
    } catch { /* Keep the draft when storage is unavailable. */ }
  }, { flush: 'sync' })
  watch(quotedReply, quote => {
    draftRevision++
    if (restoringDraft || !selected.value) return
    if (persisted) { stageDraft(); return }
    try {
      const key = quoteStoragePrefix + selected.value.id
      if (quote) localStorage.setItem(key, JSON.stringify(quote))
      else localStorage.removeItem(key)
    } catch { /* Keep the quote when storage is unavailable. */ }
  }, { flush: 'sync' })
  watch(attachedImages, files => {
    for (const url of attachedPreviewURLs.value) URL.revokeObjectURL(url)
    attachedPreviewURLs.value = files.map(file => URL.createObjectURL(file))
    stageDraft()
  }, { flush: 'sync' })
  watch(sessions, list => {
    if (!selected.value) return
    const updated = list.find(item => item.id === selected.value?.id)
    // The workspace list contains open summaries only. Its owner verifies
    // omission (closing versus deletion) before clearing the selected view.
    if (updated) selected.value = updated
  }, { flush: 'sync' })
  watch(() => selected.value?.open, (open, previous) => {
    if (open && previous === false && synchronization.value.phase === 'stopped') currentScope?.sync?.reconnect('conversation reopened', true)
  }, { flush: 'sync' })
  watch(modelCatalog, catalog => {
    const primary = configuredConversationModel(catalog)
    if (selectedModel.value !== primary) {
      selectedModel.value = primary
      selectedProcessingTier.value = ''
    }
  }, { flush: 'sync', immediate: true })

  // Index loading is independent of the trace and of virtual row mounting.
  watch(() => [...new Set([...messages.value.filter(message => !message.contextSummary).map(message => message.runId), ...(state.hasNewer ? [] : [activeRunID.value])])].filter(Boolean), runs => {
    const scope = currentScope
    if (!scope || !current(scope)) return
    const retained = new Set(runs)
    for (const run of indexedRuns.keys()) if (!retained.has(run)) indexedRuns.delete(run)
    Object.assign(state, pruneConversationWindow(state))
    const keys = new Set([...messages.value.map(message => message.id), ...state.mainOutputs.map(mainOutputKey)])
    for (const key of modelOutputRequests.keys()) if (!keys.has(key)) modelOutputRequests.delete(key)
    for (const key of mainOutputRequests.keys()) if (!keys.has(key)) mainOutputRequests.delete(key)
    modelOutputLoading.value = Object.fromEntries(Object.entries(modelOutputLoading.value).filter(([key]) => keys.has(key)))
    modelOutputFailure.value = Object.fromEntries(Object.entries(modelOutputFailure.value).filter(([key]) => keys.has(key)))
  })
  function loadMainOutputIndex(scope: Scope, runId: string): Promise<boolean> {
    if (!hasVisibleRun(runId)) return Promise.resolve(false)
    const pending = indexedRuns.get(runId)
    if (pending) return pending.request
    const token = { request: Promise.resolve(false) }
    indexedRuns.set(runId, token)
    const retained = () => current(scope) && indexedRuns.get(runId) === token && hasVisibleRun(runId)
    token.request = Promise.resolve().then(async () => {
      let before = 0
      const outputs: MainOutput[] = []
      try {
        do {
          const page = await request<MainOutput[]>(scope, `/sessions/${encodeURIComponent(scope.id)}/runs/${encodeURIComponent(runId)}/main-outputs${before ? `?before=${before}` : ''}`)
          if (!retained()) return false
          outputs.push(...page)
          if (page.length < 100) break
          const next = page[0]?.sequence
          if (!next || before && next >= before) throw new Error('Main output index did not advance')
          before = next
        } while (retained())
        if (!retained()) return false
        state.mainOutputs = pruneConversationWindow({ ...state, mainOutputs: mergeMainOutputs(state.mainOutputs, outputs) }).mainOutputs
        return true
      } catch {
        // These indexes are optional for the visible snapshot. An explicit
        // navigation handles failure without making background retries noisy.
        if (retained()) indexedRuns.delete(runId)
        return false
      }
    })
    return token.request
  }
  async function readOutputIndexes(runs: string[]) {
    const scope = currentScope
    if (!scope || !current(scope)) return false
    const reads = await Promise.all([...new Set(runs)].filter(Boolean).map(runId => loadMainOutputIndex(scope, runId)))
    return current(scope) && reads.every(Boolean)
  }

  function clearSession(loadDraft = true) {
    if (persisted) restoringDraft = true
    cancelEditMessage()
    rollbackFailure.value = ''
    if (!selected.value) welcomeImages = [...attachedImages.value]
    firstSendController?.abort()
    firstSendController = null
    currentScope?.controller.abort()
    currentScope = null
    sessionAction.value = null
    synchronization.value = { phase: 'ready' }
    syncDiagnostics.value = null
    snapshotReady.value = false
    acknowledgedReplySequence = 0
    auxiliaryRevision++
    readingSequence = 0
    generation++
    if (busyOwner) { busyOwner = null; busy.value = false }
    attachedImages.value = [...welcomeImages]
    restoringDraft = true
    Object.assign(state, createConversationState())
    quotedReply.value = null
    modelOutputLoading.value = {}
    modelOutputFailure.value = {}
    modelOutputRequests.clear()
    mainOutputRequests.clear()
    indexedRuns.clear()
    draft.value = readWelcomeDraft()
    restoringDraft = false
    initializeWelcomeSelection()
    selectedProcessingTier.value = ''
    pendingBusy.value = {}
    compaction.value = null
    contextUsage.value = null
    contextUsageRequest++
    compactionRequestPending.value = false
    maintenanceRunID = ''
    latestUserSequence.value = 0
    olderActivity.value = []
    hasOlderActivity.value = false
    activityPaging.value = false
    paging.value = false
    recentOldestEvent = olderActivityCursor = 0
    recentHasOlderActivity = false
    if (loadDraft) selectDraft()
  }
  onScopeDispose(() => { persisted?.dispose(); clearSession(false); analysisImageDrafts.clear() })

  function restoreSelection(session: Session) {
    const saved = session.manualModelKey || session.modelKey
    selectedModel.value = configuredConversationModel(modelCatalog.value)
    localReasoning.value = saved && session.manualReasoningEffort ? { modelKey: saved, effort: session.manualReasoningEffort } : undefined
    selectedProcessingTier.value = selectedModel.value === saved ? session.processingTier || '' : ''
  }
  async function selectSession(session: Session) {
    clearSession(false)
    if (persisted) restoringDraft = true
    const scope: Scope = { id: session.id, generation, controller: new AbortController(), initialSelectionVersion: selectionVersion }
    currentScope = scope
    selected.value = session
    attachedImages.value = analysisImageDrafts.get(session.id) || []
    analysisImageDrafts.delete(session.id)
    draft.value = readDraft(session.id)
    quotedReply.value = readQuotedReply(session.id)
    restoringDraft = false
    selectDraft()
    restoreSelection(session)
    await connectStream(scope)
  }
  async function loadAuxiliary(scope: Scope) {
    const revision = ++auxiliaryRevision
    const load = async <T>(path: string, apply: (value: T) => void) => {
      try { const value = await request<T>(scope, `/sessions/${scope.id}/${path}`); if (current(scope) && revision === auxiliaryRevision) apply(value) }
      catch (failure) { scope.sync?.diagnostic(`auxiliary ${path} failed`, failure) }
    }
    await Promise.all([
      load<CompactionStatus | null>('context', value => { if (value) applyCompaction(value, value.sequence || 0) }),
      refreshConversation(),
    ])
  }

  async function refreshConversation() {
    const scope = currentScope
    if (!scope || !current(scope)) return
    const jobsBefore = new Map(state.jobs.map(job => [job.id, job]))
    const artifactsBefore = new Map(state.artifacts.map(artifact => [artifact.id, artifact]))
    const view = viewRevision.value
    try {
      const [sessionJobs, sessionArtifacts] = await Promise.all([
        request<Job[]>(scope, `/sessions/${scope.id}/jobs`),
        request<Artifact[]>(scope, `/sessions/${scope.id}/artifacts`),
      ])
      if (!current(scope) || view !== viewRevision.value) return
      // Preserve entries changed by SSE while still loading unrelated history.
      state.jobs = mergeOutputSnapshot(sessionJobs, state.jobs, jobsBefore)
        .sort((a, b) => Number(jobActive(b)) - Number(jobActive(a))).slice(0, 200)
      state.artifacts = mergeOutputSnapshot(sessionArtifacts, state.artifacts, artifactsBefore).slice(0, 200)
    } catch (failure) { report(scope, failure) }
  }
  async function loadOlderActivity() {
    const scope = currentScope
    if (!scope || !current(scope) || !hasOlderActivity.value || activityPaging.value || olderActivityCursor <= 0) return
    activityPaging.value = true
    try {
      const page = await request<StreamEvent[]>(scope, `/sessions/${scope.id}/event-page?before=${olderActivityCursor}`)
      if (!current(scope)) return
      olderActivityCursor = page[0]?.sequence || olderActivityCursor
      olderActivity.value = page.filter(event => ['tool', 'run_finished'].includes(event.kind))
      hasOlderActivity.value = page.length === 100
    } catch (failure) { report(scope, failure) }
    finally { if (current(scope)) activityPaging.value = false }
  }
  function showRecentActivity() {
    if (activityPaging.value) return
    olderActivity.value = []
    olderActivityCursor = recentOldestEvent
    hasOlderActivity.value = recentHasOlderActivity
  }
  async function loadOlder() {
    const scope = currentScope
    const first = state.messages[0]
    if (!scope || !current(scope) || !first || paging.value) return false
    const view = viewRevision.value
    paging.value = true
    try {
      const page = await request<Message[]>(scope, `/sessions/${scope.id}/messages?before=${first.sequence}`)
      if (!current(scope) || view !== viewRevision.value) return false
      const next = [...page, ...state.messages]
      state.messages = [...new Map(next.map(message => [message.id, message])).values()].sort((a, b) => a.sequence - b.sequence)
      state.hasOlder = page.length === 100
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { if (current(scope)) paging.value = false }
  }
  async function loadNewer() {
    const scope = currentScope
    const last = state.messages.at(-1)
    if (!scope || !current(scope) || !last || paging.value) return
    paging.value = true
    try {
      const page = await request<Message[]>(scope, `/sessions/${scope.id}/messages?after=${last.sequence}`)
      if (!current(scope)) return
      const next = [...state.messages, ...page]
      state.messages = [...new Map(next.map(message => [message.id, message])).values()].sort((a, b) => a.sequence - b.sequence)
      state.hasNewer = page.length === 100 || (state.messages.at(-1)?.sequence || 0) < state.latestMessageSequence
    } catch (failure) { report(scope, failure) }
    finally { if (current(scope)) paging.value = false }
  }
  async function loadLatest() {
    const scope = currentScope
    if (!scope || !current(scope) || paging.value) return false
    const view = viewRevision.value
    paging.value = true
    try {
      const page = await request<Message[]>(scope, `/sessions/${scope.id}/messages`)
      if (!current(scope) || view !== viewRevision.value) return false
      let window = page
      let last = window.at(-1)?.sequence || 0
      // Reading an older retained window stops appending new SSE messages.
      // Catch up to its observed watermark before declaring this the tail.
      while (last < state.latestMessageSequence) {
        const received = state.messages.filter(message => message.sequence > last)
        const newer = received.length && received.at(-1)!.sequence >= state.latestMessageSequence ? received
          : await request<Message[]>(scope, `/sessions/${scope.id}/messages?after=${last}`)
        if (!current(scope) || view !== viewRevision.value) return false
        const next = newer.at(-1)?.sequence || 0
        if (next <= last) throw new Error('Latest message page did not advance')
        window = [...window, ...newer]
        last = next
      }
      const retained = new Map(state.messages.map(message => [message.id, message]))
      for (const message of window) retained.set(message.id, message)
      state.messages = [...retained.values()].sort((a, b) => a.sequence - b.sequence)
      state.hasOlder &&= page.length === 100
      state.hasNewer = false
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { if (current(scope)) paging.value = false }
  }

  function hasVisibleRun(runId: string) { return !!runId && (messages.value.some(message => message.runId === runId) || !state.hasNewer && activeRunID.value === runId) }
  const traceSegments = computed(() => replyTraceSegments(messages.value, agentWorking.value ? activeRunID.value : '', state.hasNewer))
  const trailingTrace = computed(() => {
    const runId = activeRunID.value, active = agentWorking.value
    if (!runId || state.hasNewer) return
    // Keep the latest unanchored run visible if it ends without a reply.
    if (!active && (selected.value?.executing || !processEvents.value.some(event => event.data?.runId === runId
      && event.kind === 'run_finished' && !event.data.message && event.sequence > state.latestMessageSequence))) return
    return trailingTraceSegment(traceSegments.value, runId, processEvents.value, active,
      state.runClock?.id === runId ? state.runClock.startedAt : '')
  })
  function traceSegment(message: Message) { return traceSegments.value.get(message.id)! }
  function runActivityAnchor(message: Message) {
    return traceSegment(message)?.anchorId === message.id
  }
  function firstRunSequence(runId: string) { return messages.value.find(message => message.runId === runId)?.sequence || 0 }
  function resultsForMessage(message: Message) { return messageResults(message, modelResults.value) }
  function resultsForRun(runId: string) { return runResults(runId, messages.value, modelResults.value) }
  function runStartedAt(runId: string) { return messages.value.find(message => message.runId === runId && message.role === 'user')?.createdAt || '' }
  function runReplyTurns(runId: string) {
    return messages.value.filter(message => message.runId === runId && message.role === 'assistant' && message.outputTurn)
      .map(message => message.outputTurn!)
  }
  function resultsForTrace(segment: ReplyTraceSegment) {
    return resultsForRun(segment.runId).filter(result => result.sequence > segment.after && (!segment.through || result.sequence <= segment.through))
  }
  function liveDrafts(message: Message) {
    if (message.runId !== activeRunID.value || !agentWorking.value) return []
    return modelDrafts.value.filter(item => {
      if (item.runId !== message.runId || item.depth || item.finished) return false
      // A later user input must not move an earlier response underneath it.
      let anchor: Message | undefined
      for (let index = messages.value.length - 1; index >= 0; index--) {
        const candidate = messages.value[index]
        if (candidate.runId === item.runId && candidate.sequence < item.sequence) { anchor = candidate; break }
      }
      return anchor?.id === message.id
    })
  }
  async function fullMessageText(message: Message) {
    if (!message.outputTurn || !message.outputBytes) return message.content
    if (new TextEncoder().encode(message.content).length === message.outputBytes) return message.content
    const text = messages.value.find(item => item.id === message.id)?.content ?? message.content
    if (new TextEncoder().encode(text).length === message.outputBytes) return text
    const streamed = state.mainOutputs.find(output => output.runId === message.runId && output.turn === message.outputTurn)
    if (streamed?.loaded && streamed.bytes === message.outputBytes) return mainOutputText(streamed)
    const output = await loadFullModelOutput(message)
    releaseDisplayOutput({ ...message, displayKey: message.id, displaySequence: message.sequence })
    return output.content
  }

  async function draftFiles(scope: Scope, result: MessageDraft) {
    const files: File[] = []
    for (const [index, image] of result.images.entries()) {
      if (!current(scope)) break
      const blob = await client.image(`/sessions/${encodeURIComponent(scope.id)}/resources/${encodeURIComponent(image.hash)}`, scope.controller.signal)
      files.push(new File([blob], image.name || `image-${index + 1}`, { type: image.mediaType }))
    }
    return files
  }
  function cancelEditMessage() {
    if (!editingMessage.value) return
    if (editBackup) {
      draft.value = editBackup.draft
      quotedReply.value = editBackup.quote
      attachedImages.value = editBackup.images
    }
    editingMessage.value = null
    editBackup = null
  }
  async function beginEditMessage(message: Message) {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.open || message.role !== 'user' || busy.value) return false
    const owner = startBusy()
    try {
      const result = await request<MessageDraft>(scope, `/sessions/${scope.id}/messages/${message.id}/draft`)
      if (!current(scope)) return false
      const files = await draftFiles(scope, result)
      if (!current(scope)) return false
      editBackup ||= { draft: draft.value, quote: quotedReply.value, images: [...attachedImages.value] }
      editingMessage.value = result.message
      draft.value = result.message.content
      quotedReply.value = null
      attachedImages.value = files
      error.value = ''
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { endBusy(scope, owner) }
  }
  async function reloadAfterRollback(clearEdit = true) {
    const session = selected.value
    if (!session) return
    const saved = { draft: draft.value, quote: quotedReply.value, images: [...attachedImages.value], edit: editingMessage.value, backup: editBackup }
    const nextGeneration = generation + 1
    await selectSession(session)
    if (selected.value?.id !== session.id || generation !== nextGeneration) return
    draft.value = saved.draft
    quotedReply.value = saved.quote
    attachedImages.value = saved.images
    if (!clearEdit) { editingMessage.value = saved.edit; editBackup = saved.backup }
  }
  async function withdrawMessage(message: Message) {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.open || message.role !== 'user' || busy.value) return false
    const owner = startBusy()
    rollbackFailure.value = ''
    try {
      // Rollback releases ownership held only by discarded history. Recover
      // the originals first, while their download URLs remain authorized.
      let files: File[] = []
      if (message.imageCount) {
        const original = await request<MessageDraft>(scope, `/sessions/${scope.id}/messages/${message.id}/draft`)
        if (!current(scope)) return false
        files = await draftFiles(scope, original)
        if (!current(scope)) return false
      }
      const result = await request<MessageDraft>(scope, `/sessions/${scope.id}/messages/${message.id}/revert`, { method: 'POST' })
      if (!current(scope)) return false
      editingMessage.value = null
      editBackup = null
      if (result.session) {
        endBusy(scope, owner)
        await selectSession(result.session)
        if (selected.value?.id !== result.session.id) return false
        await onSessionStarted?.(result.session)
      }
      draft.value = result.message.content
      quotedReply.value = null
      attachedImages.value = files
      if (!result.session) await reloadAfterRollback()
      return true
    } catch (failure) {
      if (current(scope)) { rollbackFailure.value = String(failure); error.value = rollbackFailure.value; await refresh() }
      return false
    } finally { endBusy(scope, owner) }
  }

  async function recallLastUserMessage(): Promise<boolean> {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.open || draft.value || quotedTexts.value.length || attachedImages.value.length || busy.value || recallingScope === scope) return false
    recallingScope = scope
    const revision = draftRevision
    const canRestore = () => current(scope) && draftRevision === revision && !draft.value && !quotedTexts.value.length && !attachedImages.value.length && !busy.value
    try {
      // Reading older history must not recall an older question as the latest one.
      let page = state.hasNewer ? await request<Message[]>(scope, `/sessions/${scope.id}/messages`) : state.messages
      let before = Infinity
      while (canRestore()) {
        const message = page.filter(item => item.role === 'user').at(-1)
        if (message) {
          if (!message.content) return false
          draft.value = message.content
          return true
        }
        if (page.length < 100 || !page[0] || page[0].sequence >= before) return false
        before = page[0].sequence
        page = await request<Message[]>(scope, `/sessions/${scope.id}/messages?before=${before}`)
      }
    } catch (failure) { report(scope, failure) }
    finally { if (recallingScope === scope) recallingScope = null }
    return false
  }

  function chooseImages(event: Event) {
    const input = event.target as HTMLInputElement
    addImages(Array.from(input.files || []))
    input.value = ''
  }
  function addImages(files: File[]) {
    const next = [...attachedImages.value, ...files]
    if (next.some(file => file.size < 1 || file.size > 50 * 1048576 || !['image/png', 'image/jpeg', 'image/webp', 'image/gif'].includes(file.type))) { error.value = t('imageLimit'); return }
    attachedImages.value = next
    error.value = ''
  }
  function pasteImages(event: ClipboardEvent) {
    const files = Array.from(event.clipboardData?.items || []).filter(item => item.kind === 'file').map(item => item.getAsFile()).filter((file): file is File => !!file)
    if (!files.length) return
    event.preventDefault()
    if (canAttachImage.value) addImages(files)
    else error.value = t('imageUnsupported')
  }
  function dropImages(event: DragEvent) {
    const files = Array.from(event.dataTransfer?.files || [])
    if (!files.length) return
    if (canAttachImage.value) addImages(files)
    else error.value = t('imageUnsupported')
  }
  function removeImage(index: number) { attachedImages.value = attachedImages.value.filter((_, item) => item !== index) }
  function quoteSelection(quote: QuotedText) {
    if (!selected.value?.open || busy.value || !quote.text.trim()) return
    const id = quotedReply.value?.nextId || 1
    quotedReply.value = { items: [...quotedTexts.value, { ...quote, id }], nextId: id + 1 }
  }
  function removeQuote(id: number) {
    if (!busy.value && quotedReply.value) quotedReply.value = { ...quotedReply.value, items: quotedTexts.value.filter(quote => quote.id !== id) }
  }
  function applyInputConfirmation(event: StreamEvent) {
    state.inputConfirmations = confirmInputEvent(state.inputConfirmations, event, state.lastEvent)
  }
  async function applySentMessage(scope: Scope, result: SendMessageResult) {
    if (!current(scope)) return
    if (state.hasNewer) { await selectSession(selected.value!); return }
    addMessage(result.message)
    if (result.message?.role === 'user') latestUserSequence.value = Math.max(latestUserSequence.value, result.message.sequence)
    if (result.runId && !state.finishedRuns[result.runId] && (!state.activeRunID || state.activeRunID === result.runId || (result.message?.sequence || 0) >= state.activeRunSequence)) {
      state.activeRunID = result.runId
      state.activeRunSequence = result.message?.sequence || state.activeRunSequence
      if (state.runClock?.id !== result.runId) state.runClock = { id: result.runId, startedAt: result.message?.createdAt || new Date().toISOString() }
      if (state.runProgress?.runId !== result.runId) state.runProgress = { runId: result.runId, phase: 'starting' }
      if (selected.value) selected.value = { ...selected.value, executing: true, pendingReply: undefined }
    }
    if (result.input) applyInputConfirmation(result.event)
    error.value = ''
  }
  async function send() {
    if (busy.value || manualCompacting.value || compactionRequestPending.value || (!draft.value.trim() && !attachedImages.value.length)) return
    let scope = currentScope
    let session = selected.value
    if (session && (!scope || !current(scope) || !session.open)) return
    if (!session && (!interactionPreferences.ready.value || !modelCatalog.value.models.some(model => model.key === selectedModel.value && !!model.provider))) return
    const text = draft.value
    const quote = quotedReply.value
    const content = formatQuotedReply(text, quote, (item, number) => t('quoteNumber', { number }) + ' · ' + t(item.role === 'assistant' ? 'quotedReply' : 'quotedMessage'))
    const files = [...attachedImages.value]
    const edited = editingMessage.value
    const sentInput: DraftInput = {text,quote,files}
    const modelKey = selectedModel.value
    if (!selectedModelInfo.value) { error.value = t('chooseModelBeforeSending'); return }
    const reasoningEffort = selectedReasoning.value
    const processingTier = selectedProcessingTier.value
    // Submitting also commits the displayed choice while initial loading is pending.
    if (scope) delete scope.initialSelectionVersion
    rememberSelectedModel()
    const owner = startBusy()
    const startedGeneration = generation
    try {
      const saved = !edited && persisted ? await persisted.save(session?.id || 'home',sentInput) : undefined
      if (generation !== startedGeneration) return
      if (!session) {
        const controller = new AbortController()
        firstSendController = controller
        await interactionPreferences.flush()
        if (generation !== startedGeneration || selected.value || controller.signal.aborted) return
        const created = await client.request<Session>('/sessions', { method: 'POST', body: '{}', signal: controller.signal })
        if (generation !== startedGeneration || selected.value || controller.signal.aborted) return
        firstSendController = null
        session = { ...created, manualModelKey: modelKey, manualReasoningEffort: reasoningEffort, processingTier }
        sessions.value = [...sessions.value.filter(item => item.id !== session!.id), session]
        scope = { id: session.id, generation, controller: new AbortController() }
        currentScope = scope
        selected.value = session
        welcomeImages = []
        if (!persisted) try {
          localStorage.removeItem(welcomeDraftStorageKey)
          if (draft.value) localStorage.setItem(draftStoragePrefix + session.id, draft.value)
        } catch { /* The in-memory draft remains available. */ }
        await connectStream(scope)
        if (!persisted) await onSessionStarted?.(session)
      }
      if (!scope) return
      if (!current(scope)) return
      const images: ImageRef[] = saved?.images || []
      for (const file of saved ? [] : files) {
        images.push(await request<ImageRef>(scope, `/sessions/${scope.id}/resources?name=${encodeURIComponent(file.name)}`, {
          method: 'POST', headers: { 'Content-Type': file.type }, body: file,
        }))
        if (!current(scope)) return
      }
      const result = await request<SendMessageResult>(scope, `/sessions/${scope.id}/messages`, { method: 'POST', body: JSON.stringify({ content, images, modelKey, reasoningEffort, processingTier, ...(edited ? { replacesMessageId: edited.id } : {}), ...(saved ? { draft: {key:saved.key,revision:saved.revision} } : {}) }) })
      if (!current(scope)) return
      if (persisted) restoringDraft = true
      if (draft.value === text) draft.value = ''
      if (quotedReply.value === quote) quotedReply.value = null
      if (attachedImages.value.length === files.length && files.every((file, index) => attachedImages.value[index] === file)) attachedImages.value = []
      restoringDraft = false
      if (saved) {
        persisted?.sent(saved.key,saved.revision,sentInput)
        if (saved.key === 'home' && session) {
          welcomeImages = []
          selectDraft()
          await onSessionStarted?.(session)
        }
      }
      if (edited) {
        const backup = editBackup
        editingMessage.value = null
        editBackup = null
        if (result.session) {
          if (backup) {
            draft.value = backup.draft; quotedReply.value = backup.quote; attachedImages.value = backup.images
          }
          endBusy(scope,owner)
          await selectSession(result.session)
          await onSessionStarted?.(result.session)
        } else await reloadAfterRollback()
      } else await applySentMessage(scope, result)
    } catch (failure) {
      if (scope && edited && current(scope)) {
        const message = String(failure)
        let removed = false
        try { await request(scope, `/sessions/${scope.id}/messages/${edited.id}/draft`) }
        catch (check) { removed = check instanceof VistaAPIError && check.status === 404 }
        if (!current(scope)) return
        await reloadAfterRollback(removed)
        if (selected.value?.id === scope.id) error.value = message
      } else if (scope) report(scope, failure)
      else if (generation === startedGeneration) error.value = String(failure)
    }
    finally {
      if (scope) endBusy(scope, owner)
      else if (generation === startedGeneration && busyOwner === owner) { busyOwner = null; busy.value = false }
      if (generation === startedGeneration) firstSendController = null
    }
  }
  function submitMessage() { if (!busy.value && (!attachedImages.value.length || canAttachImage.value)) void send() }
  function canRetryFailure(failure: FailureContext) {
    return failure.kind === 'execution' && !!selected.value?.open && !selected.value.executing && !busy.value
      && !manualCompacting.value && !compactionRequestPending.value && canAnalyzeFailure.value
      && selected.value.latestRunStatus === 'failed' && failure.runId === activeRunID.value
  }
  async function retryFailure(failure: FailureContext) {
    const scope = currentScope
    if (!scope || !current(scope) || !canRetryFailure(failure)) return
    const owner = startBusy()
    try {
      // A deliberate continuation keeps durable tool history and the user's
      // unsent draft. Never withdraw/resend the original task or repeat tools.
      const result = await request<SendMessageResult>(scope, `/sessions/${scope.id}/messages`, {
        method: 'POST', body: JSON.stringify({ content: t('retryTaskPrompt'), images: [],
              modelKey: selectedModel.value, reasoningEffort: selectedReasoning.value, processingTier: selectedProcessingTier.value }),
      })
      await applySentMessage(scope, result)
    } catch (failure) { report(scope, failure) }
    finally { endBusy(scope, owner) }
  }
  async function analyzeFailure(failure: FailureContext) {
    const scope = currentScope
    const source = selected.value
    if (!scope || !source || !current(scope) || busy.value || analyzingFailure.value || !canAnalyzeFailure.value) return
    const content = failureAnalysisPrompt(t('failureAnalysisPrompt'), source, failure, messages.value, state.modelByRun[failure.runId])
    const choice = { modelKey: selectedModel.value, reasoningEffort: selectedReasoning.value, processingTier: '' }
    const sourceImages = [...attachedImages.value]
    const owner = startBusy()
    analyzingFailure.value = failure.runId
    error.value = ''
    try {
      await interactionPreferences.flush()
      if (!current(scope)) return
      const response = await request<Session>(scope, '/sessions', { method: 'POST', body: '{}' })
      if (!current(scope)) return
      const created: Session = { ...response, manualModelKey: choice.modelKey, manualReasoningEffort: choice.reasoningEffort, processingTier: choice.processingTier }
      sessions.value = [...sessions.value.filter(item => item.id !== created.id), created]
      try { localStorage.setItem(draftStoragePrefix + created.id, content) } catch { /* Keep the in-memory retry below. */ }
      let deliveryFailure: unknown
      try {
        // Send before navigating. The destination's normal snapshot/stream
        // recovers the reply even if it arrives before that view opens.
        await request<SendMessageResult>(scope, `/sessions/${created.id}/messages`, {
          method: 'POST', body: JSON.stringify({ content, images: [], ...choice }),
        })
        try { localStorage.removeItem(draftStoragePrefix + created.id) } catch { /* A sent message is still durable. */ }
      } catch (cause) { deliveryFailure = cause }
      if (!current(scope)) return
      endBusy(scope, owner)
      if (sourceImages.length) analysisImageDrafts.set(source.id, sourceImages)
      const destinationSelectionVersion = selectionVersion
      await onSessionStarted?.(created)
      // The workspace route normally selects it; standalone callers need the
      // same state transition without opening a second stream for that route.
      if (current(scope)) await selectSession(created)
      if (selected.value?.id === created.id && deliveryFailure) {
        // A rejected first message has not saved this local retry choice.
        if (selectionVersion === destinationSelectionVersion) {
          restoreSelection(created)
          selected.value = { ...selected.value, processingTier: created.processingTier,
            manualModelKey: created.manualModelKey, manualReasoningEffort: created.manualReasoningEffort }
        }
        draft.value = content
        error.value = String(deliveryFailure)
      }
    } catch (cause) { report(scope, cause) }
    finally { endBusy(scope, owner); analyzingFailure.value = '' }
  }
  async function submitQuestion(runId: string, answers: QuestionAnswer[], callId?: string): Promise<boolean> {
    const scope = currentScope
    const session = selected.value
    const pending = session?.pendingReply
    if (!scope || !current(scope) || !session?.open || session.executing || busy.value || pending?.kind !== 'question' || pending.runId !== runId || pending.callId !== callId) return false
    if (!pending.questions?.length || !answers.length) return false
    const identified = pending.questions.some(question => !!question.id)
    if (identified ? answers.some(answer => !answer.questionId || !pending.questions.some(question => question.id === answer.questionId))
      : answers.length !== pending.questions.length) return false
    delete scope.initialSelectionVersion
    const owner = startBusy()
    rememberSelectedModel()
    try {
      const result = await request<SendMessageResult>(scope, `/sessions/${scope.id}/messages`, {
        method: 'POST', body: JSON.stringify({
          questionReply: { runId, ...(callId ? { callId } : {}), answers },
          modelKey: selectedModel.value, reasoningEffort: selectedReasoning.value, processingTier: selectedProcessingTier.value,
        }),
      })
      if (!current(scope)) return false
      if (selected.value?.pendingReply?.runId === runId && selected.value.pendingReply.callId === callId) selected.value = { ...selected.value, pendingReply: undefined }
      await applySentMessage(scope, result)
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { endBusy(scope, owner) }
  }
  async function decideSessionAction(requestId: string, sessionIds: string[]) {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.open || busy.value || sessionAction.value?.requestId !== requestId) return
    const owner = startBusy()
    try {
      await request(scope, `/sessions/${scope.id}/session-action/${encodeURIComponent(requestId)}`, { method: 'POST', body: JSON.stringify({ sessionIds }) })
      if (current(scope) && sessionAction.value?.requestId === requestId) sessionAction.value = null
    } catch (failure) {
      if (current(scope) && failure instanceof VistaAPIError && failure.status === 409) {
        if (sessionAction.value?.requestId === requestId) sessionAction.value = null
      } else report(scope, failure)
    } finally { endBusy(scope, owner) }
  }
  async function cancelExecution() {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.executing || busy.value || manualCompacting.value || compactionRequestPending.value) return
    const owner = startBusy()
    try {
      await request(scope, `/sessions/${scope.id}/cancel`, { method: 'POST' })
    } catch (failure) { report(scope, failure) }
    finally { endBusy(scope, owner) }
  }
  async function mutateInput(input: QueuedInput, action: 'edit' | 'delete' | 'withdraw', patch?: { content?: string }) {
    const scope = currentScope
    if (!scope || !current(scope) || pendingBusy.value[input.id]) return false
    pendingBusy.value = { ...pendingBusy.value, [input.id]: true }
    try {
      const result = await request<{ input: QueuedInput; event: StreamEvent; images?: Array<{ mediaType: string; hash: string; name?: string }> }>(scope,
        `/sessions/${scope.id}/inputs/${input.id}${action === 'withdraw' ? '/withdraw' : ''}`,
        { method: action === 'edit' ? 'PATCH' : action === 'delete' ? 'DELETE' : 'POST', ...(patch ? { body: JSON.stringify(patch) } : {}) })
      if (!current(scope)) return false
      applyInputConfirmation(result.event)
      if (action === 'withdraw') {
        draft.value = [draft.value, result.input.content].filter(Boolean).join('\n\n')
        const files = await Promise.all((result.images || []).map(async (image, index) => {
          const blob = await client.image(`/sessions/${encodeURIComponent(scope.id)}/resources/${encodeURIComponent(image.hash)}`, scope.controller.signal)
          return new File([blob], image.name || `image-${index + 1}`, { type: image.mediaType })
        }))
        if (!current(scope)) return false
        attachedImages.value = [...attachedImages.value, ...files]
      }
      error.value = ''
      return true
    } catch (failure) { report(scope, failure); if (current(scope)) scope.sync?.reconnect('input conflict', true); return false }
    finally { if (current(scope)) pendingBusy.value = { ...pendingBusy.value, [input.id]: false } }
  }
  async function resumeInputs() {
    const scope = currentScope
    if (!scope || !current(scope)) return
    try {
      const result = await request<{ event: StreamEvent }>(scope, `/sessions/${scope.id}/inputs/resume`, { method: 'POST', body: '{}' })
      if (current(scope)) applyInputConfirmation(result.event)
    } catch (failure) { report(scope, failure) }
  }
  function loadFullModelOutput(message: Message): Promise<OutputView> {
    const scope = currentScope
    if (!scope || !current(scope) || !message.outputTurn) return Promise.reject(new DOMException('Session changed', 'AbortError'))
    const cached = refs.modelOutput.value[message.id]
    if (cached) return Promise.resolve(cached)
    const pending = modelOutputRequests.get(message.id)
    if (pending) return pending
    modelOutputLoading.value[message.id] = true
    modelOutputFailure.value[message.id] = ''
    const loading = request<OutputView>(scope, `/sessions/${encodeURIComponent(scope.id)}/runs/${encodeURIComponent(message.runId)}/model-output?turn=${message.outputTurn}&full=1`).then(output => {
      if (!current(scope)) throw new DOMException('Session changed', 'AbortError')
      if (output.offset !== 0 || output.nextOffset !== output.availableBytes) throw new Error('Incomplete output response')
      if (modelOutputRequests.get(message.id) === loading && messages.value.some(item => item.id === message.id)) refs.modelOutput.value = { ...refs.modelOutput.value, [message.id]: output }
      return output
    }).catch(failure => {
      if (current(scope) && modelOutputRequests.get(message.id) === loading) modelOutputFailure.value[message.id] = String(failure)
      throw failure
    }).finally(() => {
      if (current(scope) && modelOutputRequests.get(message.id) === loading) {
        modelOutputLoading.value[message.id] = false
        modelOutputRequests.delete(message.id)
      }
    })
    modelOutputRequests.set(message.id, loading)
    return loading
  }
  function readModelOutput(message: Message, retry = false) {
    if (!message.outputTurn) return Promise.resolve(true)
    if (modelOutputFailure.value[message.id] && !retry) return Promise.resolve(false)
    return loadFullModelOutput(message).then(() => true, () => false)
  }

  function loadMainOutput(record: MainOutputRecord): Promise<MainOutputRecord> {
    const scope = currentScope
    if (!scope || !current(scope)) return Promise.reject(new DOMException('Session changed', 'AbortError'))
    const key = mainOutputKey(record)
    const latest = state.mainOutputs.find(output => mainOutputKey(output) === key)
    if (!latest) return Promise.reject(new DOMException('Output changed', 'AbortError'))
    if (latest.loaded) return Promise.resolve(latest)
    const pending = mainOutputRequests.get(key)
    if (pending) return pending
    modelOutputLoading.value[key] = true
    modelOutputFailure.value[key] = ''
    const snapshot = request<OutputView>(scope, `/sessions/${encodeURIComponent(scope.id)}/runs/${encodeURIComponent(record.runId)}/model-output?turn=${record.turn}&full=1`)
    const loading = snapshot.then(output => {
      if (!current(scope)) throw new DOMException('Session changed', 'AbortError')
      if (output.offset !== 0 || output.nextOffset !== output.availableBytes || !output.lastSequence) throw new Error('Incomplete output response')
      const retained = state.mainOutputs.find(item => mainOutputKey(item) === key)
      if (!retained || mainOutputRequests.get(key) !== loading) throw new DOMException('Output changed', 'AbortError')
      const hydrated = hydrateMainOutput(retained, output)
      state.mainOutputs = pruneConversationWindow({ ...state, mainOutputs: state.mainOutputs.map(item => mainOutputKey(item) === key ? hydrated : item) }).mainOutputs
      if (!hydrated.loaded) throw new Error('Incomplete output response')
      return hydrated
    }).catch(failure => {
      if (current(scope) && mainOutputRequests.get(key) === loading) modelOutputFailure.value[key] = String(failure)
      throw failure
    }).finally(() => {
      if (current(scope) && mainOutputRequests.get(key) === loading) {
        modelOutputLoading.value[key] = false
        mainOutputRequests.delete(key)
      }
    })
    mainOutputRequests.set(key, loading)
    return loading
  }
  function readDisplayOutput(message: ConversationItem, retry = false) {
    if (modelOutputFailure.value[displayOutputKey(message)] && !retry) return Promise.resolve(undefined)
    // Each consumer owns the returned strings even when another consumer
    // releases the shared source cache before history prepares its projection.
    if (!message.mainOutput) return loadFullModelOutput(message)
      .then(output => new Map([[message.displayKey, { version: displayBodyVersion({ ...message, outputBytes: output.availableBytes }), text: output.content }]]), () => undefined)
    return loadMainOutput(message.mainOutput).then(output => {
      // A streaming fragment may have become the saved reply while this read
      // was pending. Project the returned text with the current reply metadata.
      const replies = messages.value.filter(row => row.runId === message.runId && row.role === 'assistant' && row.outputTurn === message.outputTurn)
      return new Map(conversationItems(replies, [output]).map(row => [row.displayKey, { version: displayBodyVersion(row), text: row.content }]))
    }, () => undefined)
  }
  function releaseDisplayOutput(message: ConversationItem) {
    if (refs.modelOutput.value[message.id]) {
      const retained = { ...refs.modelOutput.value }
      delete retained[message.id]
      refs.modelOutput.value = retained
    }
    const key = message.mainOutput ? mainOutputKey(message.mainOutput) : ''
    state.mainOutputs = state.mainOutputs.map(output =>
      (key ? mainOutputKey(output) === key : output.runId === message.runId && output.turn === message.outputTurn)
        && output.complete && (output.content || output.deltas.length)
        ? { ...output, content: '', contentSequence: 0, deltas: [], loaded: false } : output)
  }

  return {
    editingMessage, beginEditMessage, cancelEditMessage, withdrawMessage, rollbackFailure, sessionAction, decideSessionAction,
    draftPhase: persisted?.phase, refreshDraft, retryDraft,
    ...refs, displayMessages, readOutputIndexes, readDisplayOutput, releaseDisplayOutput, olderActivity, hasOlderActivity, activityPaging, paging, selectedModel, selectedModelInfo,
    selectedReasoning, reasoningChoices, canAttachImage, attachedImages, attachedPreviewURLs,
    selectedProcessingTier, processingTiers, chooseProcessingTier,
     pendingInputs, inputQueuePaused, pendingBusy, mutateInput, resumeInputs, compaction, compacting, compactionResult, contextUsage, canCompact, compactSession, manualCompacting, compactionRequestPending, continueAfterCompaction, fillContinueDraft,
    draft, quotedTexts, quoteSelection, removeQuote, agentWorking, agentStatus, hasTaskDetails, recallLastUserMessage,
    synchronization, syncDiagnostics, snapshotReady, viewRevision, resynchronize, renderFailure,
    selectSession, clearSession, refreshConversation, loadOlderActivity, showRecentActivity, loadOlder, loadNewer, loadLatest, markRead,
    send, submitMessage, analyzingFailure, canAnalyzeFailure, analyzeFailure, canRetryFailure, retryFailure, submitQuestion, cancelExecution, readModelOutput, modelOutputLoading, modelOutputFailure,
    fullMessageText, resultsForMessage, resultsForRun, resultsForTrace, traceSegment, trailingTrace, runStartedAt, runReplyTurns, runActivityAnchor, firstRunSequence, liveDrafts,
    hasVisibleRun, chooseImages, addImages, pasteImages, dropImages, removeImage,
  }
}
