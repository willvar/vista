import { computed, onScopeDispose, reactive, ref, toRefs, watch, type Ref } from 'vue'
import type { VistaClient } from '../client/vista'
import { VistaAPIError } from '../client/vista.ts'
import { createConversationSync, type ConversationSyncStatus, type ConversationSyncDiagnostics } from '../client/conversationSync.ts'
import type { Artifact, ConversationSnapshot, CompactionStatus, ContextUsage, Job, MainOutput, Message, ModelCatalog, OutputView, QuestionAnswer, QueuedInput, SendMessageResult, Session, StreamEvent } from '../client/types'
import { appendMessage, createConversationState, messageResults, runResults, messageWindowLimit, pruneConversationWindow, reduceConversationEvent, replyTraceSegments } from '../state/conversation.ts'
import { toolOperationLabelKey } from '../state/runTrace.ts'
import { conversationItems, hydrateMainOutput, mainOutputKey, mergeMainOutputs, type ConversationItem, type MainOutputRecord } from '../state/mainOutputs.ts'
import { useInteractionPreferences, type InteractionPreferences, type SelectionMode, type SelectionTier } from './useInteractionPreferences.ts'
import { formatQuotedReply, quoteStoragePrefix, readQuotedText, type QuotedText } from '../state/quotedReply.ts'
import { reasoningChoiceLabel } from '../ui/reasoning.ts'
import { jobActive } from '../state/jobs.ts'

export type ConversationOptions = {
  client: VistaClient
  sessions: Ref<Session[]>
  modelCatalog: Ref<ModelCatalog>
  error: Ref<string>
  busy: Ref<boolean>
  refresh: () => Promise<void>
  translate: (key: string, params?: Record<string, unknown>) => string
  interactionPreferences?: InteractionPreferences
  onSessionStarted?: (session: Session) => Promise<void>
}
type Scope = { id: string; generation: number; controller: AbortController; sync?: ReturnType<typeof createConversationSync> }
export const draftStoragePrefix = 'vista-session-draft:'
export const welcomeDraftStorageKey = 'vista-home-draft'

function readDraft(id: string) {
  try { return localStorage.getItem(draftStoragePrefix + id) || '' } catch { return '' }
}

function mergeOutputSnapshot<T extends { id: string }>(snapshot: T[], current: T[], before: Map<string, T>): T[] {
  const newer = current.filter(item => before.get(item.id) !== item)
  const newerIDs = new Set(newer.map(item => item.id))
  return [...newer, ...snapshot.filter(item => !newerIDs.has(item.id))]
}

export function useConversation({ client, sessions, modelCatalog, error, busy, refresh, translate: t, interactionPreferences = useInteractionPreferences(), onSessionStarted }: ConversationOptions) {
  const { rememberedModel, reasoningByModelId: reasoningByModel, lastMode, lastTier, feedbackInvitationSeen } = interactionPreferences
  const state = reactive(createConversationState())
  const synchronization = ref<ConversationSyncStatus>({ phase: 'ready' })
  const syncDiagnostics = ref<ConversationSyncDiagnostics | null>(null)
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
  const selectedModelInfo = computed(() => modelCatalog.value.models.find(model => model.key === selectedModel.value))
  const selectionMode = ref<SelectionMode>('manual')
  const selectionTier = ref<SelectionTier>('speed')
  const feedbackInvitationOpen = ref(false)
  const feedback = ref<Record<string, 'up' | 'down'>>({})
  const feedbackBusy = ref<Record<string, boolean>>({})
  const pendingBusy = ref<Record<string, boolean>>({})
  const compaction = ref<CompactionStatus | null>(null)
  const contextUsage = ref<ContextUsage | null>(null)
  let contextUsageRequest = 0
  const compactionRequestPending = ref(false)
  const manualCompacting = computed(() => !!selected.value?.executing && !!compaction.value?.manual
    && (compaction.value.phase === 'compacting' || maintenanceRunID === compaction.value.runId))
  let maintenanceRunID = ''
  const continueAfterCompaction = computed(() => !!selected.value?.open && !selected.value.executing && !busy.value
    && !draft.value && !quotedText.value && !attachedImages.value.length && compaction.value?.manual && compaction.value.phase === 'compacted'
    && (compaction.value.sequence || 0) > latestUserSequence.value)
  const latestUserSequence = ref(0)
  const canCompact = computed(() => !!selected.value?.open && !selected.value.executing && !busy.value
    && !compactionRequestPending.value && messages.value.length > 1 && !!selectedModel.value)
  const modelOutputLoading = ref<Record<string, boolean>>({})
  const modelOutputFailure = ref<Record<string, string>>({})
  const modelOutputRequests = new Map<string, Promise<OutputView>>()
  const mainOutputRequests = new Map<string, Promise<void>>()
  const indexedRuns = new Map<string, object>()
  const displayMessages = computed(() => conversationItems(messages.value, state.mainOutputs, state.hasOlder, state.hasNewer))
  const pendingInputs = computed(() => state.queuedInputs.filter(item => ['queued', 'paused', 'delivering'].includes(item.status)))
  const inputQueuePaused = computed(() => !!selected.value?.inputQueuePaused || pendingInputs.value.some(item => item.status === 'paused'))
  const selectedReasoningKey = computed(() => selectedModelInfo.value?.id || '')
  const selectedReasoning = computed({
    get: () => {
      const choices = selectedModelInfo.value?.reasoningOptions || []
      if (selectionMode.value === 'auto') return choices.at(-1) || ''
      const remembered = reasoningByModel.value[selectedReasoningKey.value]
        || (rememberedModel.value?.id === selectedReasoningKey.value ? rememberedModel.value.reasoningEffort : '')
      return choices.includes(remembered) ? remembered : choices.find(value => value !== 'off') || ''
    },
    set: (effort: string) => {
      if (selectionMode.value !== 'manual' || !selectedReasoningKey.value || !selectedModelInfo.value?.reasoningOptions?.includes(effort)) return
      reasoningByModel.value = { ...reasoningByModel.value, [selectedReasoningKey.value]: effort }
      rememberSelectedModel()
      void saveSelection({ manualReasoningEffort: effort })
    },
  })
  const reasoningChoices = computed(() => (selectedModelInfo.value?.reasoningOptions || []).map(value => ({ label: reasoningChoiceLabel(value, t), value })))
  const canAttachImage = computed(() => selectionMode.value === 'auto'
    ? modelCatalog.value.models.some(model => model.inputModalities?.includes('image'))
    : !!selectedModelInfo.value?.inputModalities?.includes('image'))
  const attachedImages = ref<File[]>([])
  const attachedPreviewURLs = ref<string[]>([])
  const draft = ref(readWelcomeDraft())
  const quotedText = ref<QuotedText | null>(null)
  const editingMessage = ref<Message | null>(null)
  const rollbackFailure = ref('')
  let editBackup: { draft: string; quote: QuotedText | null; images: File[] } | null = null
  type ImageRef = { hash: string; name: string; mediaType: string; bytes: number }
  type MessageDraft = { message: Message; images: ImageRef[] }
  let draftRevision = 0
  let recallingScope: Scope | null = null
  let welcomeImages: File[] = []
  let restoringDraft = false
  let firstSendController: AbortController | null = null

  function readWelcomeDraft() {
    try { return localStorage.getItem(welcomeDraftStorageKey) || '' } catch { return '' }
  }
  function initializeWelcomeSelection() {
    if (selected.value) return
    selectionMode.value = lastMode.value
    selectionTier.value = lastTier.value
    const candidates = [rememberedModel.value?.key, modelCatalog.value.default]
    selectedModel.value = candidates.find(key => modelCatalog.value.models.some(model => model.key === key)) || modelCatalog.value.default
  }

  function rememberSelectedModel() {
    const model = selectedModelInfo.value
    if (model && selectionMode.value === 'manual') rememberedModel.value = { key: model.key, id: model.id, reasoningEffort: selectedReasoning.value }
  }
  function chooseModel(key: string) {
    if (!modelCatalog.value.models.some(model => model.key === key)) return
    selectedModel.value = key
    rememberSelectedModel()
    void saveSelection({ manualModelKey: key, manualReasoningEffort: selectedReasoning.value })
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
        const choice = { selectionMode: updated.selectionMode, selectionTier: updated.selectionTier, manualModelKey: updated.manualModelKey, manualReasoningEffort: updated.manualReasoningEffort, selectionReasoningEffort: updated.selectionReasoningEffort }
        selected.value = { ...selected.value!, ...choice }
        sessions.value = sessions.value.map(item => item.id === updated.id ? { ...item, ...choice } : item)
        if (!selected.value.executing) void refreshContextUsage()
      }
    } catch (failure) { report(scope, failure) }
  }
  function chooseSelectionMode(mode: SelectionMode) {
    if (mode === selectionMode.value) return
    selectionMode.value = mode
    lastMode.value = mode
    if (mode === 'manual') {
      const candidates = [selected.value?.manualModelKey, rememberedModel.value?.key, modelCatalog.value.default]
      selectedModel.value = candidates.find(key => modelCatalog.value.models.some(model => model.key === key)) || modelCatalog.value.default
    } else inviteFeedback()
    void saveSelection({ selectionMode: mode, selectionTier: selectionTier.value,
      ...(mode === 'manual' ? { manualModelKey: selectedModel.value, manualReasoningEffort: selectedReasoning.value } : {}) })
  }
  function chooseSelectionTier(tier: SelectionTier) {
    if (tier === selectionTier.value) return
    selectionTier.value = tier
    lastTier.value = tier
    void saveSelection({ selectionTier: tier })
  }
  function inviteFeedback() {
    if (!feedbackInvitationSeen.value) {
      feedbackInvitationOpen.value = true
      feedbackInvitationSeen.value = true
    }
  }
  async function rateMessage(message: Message, value: 'up' | 'down') {
    const scope = currentScope
    if (!scope || !current(scope) || feedbackBusy.value[message.id]) return
    feedbackBusy.value = { ...feedbackBusy.value, [message.id]: true }
    try {
      const next = feedback.value[message.id] === value ? null : value
      await request(scope, `/sessions/${scope.id}/messages/${message.id}/feedback`, { method: 'PUT', body: JSON.stringify({ value: next }) })
      if (!current(scope)) return
      const updated = { ...feedback.value }
      if (next) updated[message.id] = next; else delete updated[message.id]
      feedback.value = updated
    } catch (failure) { report(scope, failure) }
    finally { if (current(scope)) feedbackBusy.value = { ...feedbackBusy.value, [message.id]: false } }
  }
  const agentWorking = computed(() => !manualCompacting.value && !!selected.value?.executing && !(runProgress.value?.runId === activeRunID.value && runProgress.value.phase === 'finished'))
  const agentStatus = computed(() => {
    if (synchronization.value.phase === 'error') return t('conversationSyncFailed')
    if (['connecting', 'recovering'].includes(synchronization.value.phase)) return t('conversationSynchronizing')
    if (compaction.value?.phase === 'compacting') return t('contextCompacting')
    const progress = runProgress.value?.runId === activeRunID.value ? runProgress.value : null
    if (progress?.phase === 'tool') return t(toolOperationLabelKey(progress.tool)) + '…'
    const key = { model: 'agentWaitingModel', reasoning: 'agentReasoning', response: 'agentResponding', processing: 'agentProcessing' }[progress?.phase as 'model' | 'reasoning' | 'response' | 'processing']
    return t(key || 'agentStarting')
  })
  const hasTaskDetails = computed(() => !!(state.contextNotices.length || olderActivity.value.some(event => !hasVisibleRun(event.data?.runId)) || hasOlderActivity.value))
  let inputRevision = 0
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
    if (!status?.runId || !['compacting', 'compacted', 'failed'].includes(status.phase) || sequence < (compaction.value?.sequence || 0)) return
    compaction.value = { ...status, sequence }
    if (!status.manual || sequence < state.activeRunSequence || !selected.value) return
    if (status.phase === 'compacting') {
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
    if (event.kind.startsWith('input_')) inputRevision++
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
        applyCompaction({ ...compaction.value, phase: 'failed', error: event.data?.message?.content || event.data?.reason || t('contextUnavailable') }, event.sequence)
      }
      if (event.kind === 'model_request' && event.data?.runId === activeRunID.value && !event.data?.depth && !event.data?.purpose && selectionMode.value === 'auto') {
        const matchingID = modelCatalog.value.models.filter(item => item.id === event.data?.id && item.provider === event.data?.provider)
        const model = event.data?.key ? modelCatalog.value.models.find(item => item.key === event.data.key)
          : matchingID.length === 1 ? matchingID[0] : undefined
        if (model) selectedModel.value = model.key
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
    if (!current(scope)) return
    const old = state
    const updated = { ...snapshot.session, readSequence: Math.max(snapshot.session.readSequence || 0, selected.value?.readSequence || 0),
      selectionMode: selectionMode.value, selectionTier: selectionTier.value,
      manualModelKey: selected.value?.manualModelKey, manualReasoningEffort: selected.value?.manualReasoningEffort }
    let next = createConversationState(updated)
    next.messages = snapshot.messages
    const latestUser = snapshot.messages.filter(message => message.role === 'user').at(-1)
    next.activeRunID = latestUser?.runId || snapshot.messages.at(-1)?.runId || ''
    next.activeRunSequence = latestUser?.sequence || 0
    next.latestMessageSequence = snapshot.messages.at(-1)?.sequence || 0
    next.hasOlder = snapshot.hasOlder
    next.jobs = old.jobs; next.artifacts = old.artifacts; next.queuedInputs = old.queuedInputs
    for (const event of snapshot.events) next = reduceConversationEvent(next, event)
    next.lastEvent = snapshot.throughSequence
    // The database snapshot wins over older lifecycle records in its event window.
    next.selected = updated
    if (!updated.executing && next.activeRunID) {
      next.finishedRuns = { ...next.finishedRuns, [next.activeRunID]: true }
      next.runProgress = { runId: next.activeRunID, phase: 'finished' }
    }
    const rollback = (snapshot.revertedSequence || 0) > old.lastEvent
    if (old.hasNewer && !rollback) {
      next.messages = old.messages
      next.hasOlder = old.hasOlder
      next.hasNewer = (next.messages.at(-1)?.sequence || 0) < next.latestMessageSequence
    } else if (!rollback) {
      const window = new Map(old.messages.filter(message => message.sequence < (snapshot.messages[0]?.sequence || 0)).map(message => [message.id, message]))
      for (const message of next.messages) window.set(message.id, message)
      next.messages = [...window.values()].sort((a, b) => a.sequence - b.sequence).slice(-messageWindowLimit)
      next.hasOlder ||= old.hasOlder || window.size > messageWindowLimit
    }
    // A POST acknowledgement can finish after the snapshot's read boundary.
    for (const message of old.messages.filter(message => message.sequence > snapshot.throughSequence)) next = appendMessage(next, message)
    if (old.activeRunSequence > snapshot.throughSequence) {
      next.activeRunID = old.activeRunID; next.activeRunSequence = old.activeRunSequence
      next.runProgress = old.runProgress
      next.selected = { ...next.selected!, executing: old.selected?.executing || false }
    }
    const unchanged = new Set(next.messages.filter(message => old.messages.some(previous => previous.id === message.id
      && previous.content === message.content && previous.outputTurn === message.outputTurn && previous.outputBytes === message.outputBytes)).map(message => message.id))
    next.modelOutput = Object.fromEntries(Object.entries(old.modelOutput).filter(([id]) => unchanged.has(id)))
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
    Object.assign(state, pruneConversationWindow(next))
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
    if (!scope.sync) scope.sync = createConversationSync({
      client, sessionId: scope.id, scope: scope.generation, signal: scope.controller.signal,
      cursor: () => state.lastEvent, replySequence: () => acknowledgedReplySequence,
      applyEvent: (event, live) => { if (current(scope)) handleEvent(event, live) },
      applySnapshot: snapshot => applySnapshot(scope, snapshot),
      applyCheckpoint: point => {
        if (!current(scope) || !selected.value) return
        const previous = selected.value
        const verified = { ...point.session, readSequence: Math.max(point.session.readSequence || 0, previous.readSequence || 0),
          selectionMode: selectionMode.value, selectionTier: selectionTier.value,
          manualModelKey: previous.manualModelKey, manualReasoningEffort: previous.manualReasoningEffort }
        if (state.activeRunSequence > point.throughSequence) {
          verified.executing = previous.executing
          verified.pendingReply = previous.pendingReply
          verified.latestRunStatus = previous.latestRunStatus
        }
        selected.value = verified
        sessions.value = sessions.value.map(item => item.id === scope.id ? verified : item)
      },
      status: status => { if (current(scope)) synchronization.value = status },
      diagnostics: value => { if (current(scope)) syncDiagnostics.value = value },
    })
    return scope.sync.start()
  }
  function resynchronize() { currentScope?.sync?.reconnect('manual recovery', true) }
  function renderFailure(failure: unknown) { currentScope?.sync?.diagnostic('render failed', failure) }

  watch(draft, value => {
    draftRevision++
    if (restoringDraft) return
    const key = selected.value ? draftStoragePrefix + selected.value.id : welcomeDraftStorageKey
    try {
      if (value) localStorage.setItem(key, value)
      else localStorage.removeItem(key)
    } catch { /* Keep the draft when storage is unavailable. */ }
  }, { flush: 'sync' })
  watch(quotedText, quote => {
    draftRevision++
    if (restoringDraft || !selected.value) return
    try {
      const key = quoteStoragePrefix + selected.value.id
      if (quote) localStorage.setItem(key, JSON.stringify(quote))
      else localStorage.removeItem(key)
    } catch { /* Keep the quote when storage is unavailable. */ }
  }, { flush: 'sync' })
  watch(attachedImages, files => {
    for (const url of attachedPreviewURLs.value) URL.revokeObjectURL(url)
    attachedPreviewURLs.value = files.map(file => URL.createObjectURL(file))
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
    if (!catalog.models.some(model => model.key === selectedModel.value)) selectedModel.value = catalog.models.some(model => model.key === selected.value?.modelKey) ? selected.value!.modelKey : catalog.default
  }, { flush: 'sync' })
  watch([interactionPreferences.ready, rememberedModel, lastMode, lastTier, modelCatalog], initializeWelcomeSelection, { flush: 'sync', immediate: true })

  // Index loading is independent of the trace and of virtual row mounting.
  watch(() => [...new Set(messages.value.map(message => message.runId))], runs => {
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
    for (const runId of runs) if (runId && !indexedRuns.has(runId)) void loadMainOutputIndex(scope, runId)
  })
  async function loadMainOutputIndex(scope: Scope, runId: string) {
    const token = {}
    indexedRuns.set(runId, token)
    const retained = () => current(scope) && indexedRuns.get(runId) === token && hasVisibleRun(runId)
    let before = 0
    try {
      do {
        const page = await request<MainOutput[]>(scope, `/sessions/${encodeURIComponent(scope.id)}/runs/${encodeURIComponent(runId)}/main-outputs${before ? `?before=${before}` : ''}`)
        if (!retained()) return
        state.mainOutputs = pruneConversationWindow({ ...state, mainOutputs: mergeMainOutputs(state.mainOutputs, page) }).mainOutputs
        if (page.length < 100) break
        const next = page[0]?.sequence
        if (!next || before && next >= before) throw new Error('Main output index did not advance')
        before = next
      } while (retained())
    } catch (failure) {
      if (retained()) { indexedRuns.delete(runId); report(scope, failure) }
    }
  }

  function clearSession() {
    cancelEditMessage()
    rollbackFailure.value = ''
    if (!selected.value) welcomeImages = [...attachedImages.value]
    firstSendController?.abort()
    firstSendController = null
    currentScope?.controller.abort()
    currentScope = null
    synchronization.value = { phase: 'ready' }
    syncDiagnostics.value = null
    acknowledgedReplySequence = 0
    auxiliaryRevision++
    readingSequence = 0
    generation++
    if (busyOwner) { busyOwner = null; busy.value = false }
    attachedImages.value = [...welcomeImages]
    restoringDraft = true
    Object.assign(state, createConversationState())
    quotedText.value = null
    modelOutputLoading.value = {}
    modelOutputFailure.value = {}
    modelOutputRequests.clear()
    mainOutputRequests.clear()
    indexedRuns.clear()
    draft.value = readWelcomeDraft()
    restoringDraft = false
    initializeWelcomeSelection()
    feedback.value = {}
    feedbackBusy.value = {}
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
  }
  onScopeDispose(clearSession)

  async function selectSession(session: Session) {
    clearSession()
    const scope: Scope = { id: session.id, generation, controller: new AbortController() }
    currentScope = scope
    selected.value = session
    attachedImages.value = []
    draft.value = readDraft(session.id)
    quotedText.value = readQuotedText(session.id)
    selectionMode.value = session.selectionMode || lastMode.value
    selectionTier.value = session.selectionTier || lastTier.value
    const candidates = selectionMode.value === 'auto' ? [session.modelKey, modelCatalog.value.default]
      : [session.manualModelKey || session.modelKey || rememberedModel.value?.key, modelCatalog.value.default]
    inviteFeedback()
    selectedModel.value = candidates.find(key => modelCatalog.value.models.some(model => model.key === key)) || modelCatalog.value.default
    await connectStream(scope)
  }
  async function loadAuxiliary(scope: Scope) {
    const revision = ++auxiliaryRevision
    const load = async <T>(path: string, apply: (value: T) => void) => {
      try { const value = await request<T>(scope, `/sessions/${scope.id}/${path}`); if (current(scope) && revision === auxiliaryRevision) apply(value) }
      catch (failure) { scope.sync?.diagnostic(`auxiliary ${path} failed`, failure) }
    }
    await Promise.all([
      load<{ feedback: Record<string, 'up' | 'down'> }>('feedback', value => { feedback.value = value.feedback || {} }),
      load<CompactionStatus | null>('context', value => { if (value) applyCompaction(value, value.sequence || 0) }),
      refreshConversation(),
    ])
  }

  async function refreshConversation() {
    const scope = currentScope
    if (!scope || !current(scope)) return
    const revision = inputRevision
    const jobsBefore = new Map(state.jobs.map(job => [job.id, job]))
    const artifactsBefore = new Map(state.artifacts.map(artifact => [artifact.id, artifact]))
    const view = viewRevision.value
    try {
      const [inputs, sessionJobs, sessionArtifacts] = await Promise.all([
        request<QueuedInput[]>(scope, `/sessions/${scope.id}/inputs`),
        request<Job[]>(scope, `/sessions/${scope.id}/jobs`),
        request<Artifact[]>(scope, `/sessions/${scope.id}/artifacts`),
      ])
      if (!current(scope) || view !== viewRevision.value) return
      if (revision === inputRevision) state.queuedInputs = inputs
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
      olderActivity.value = page.filter(event => ['tool', 'run_finished', 'context_notice'].includes(event.kind))
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
    if (!scope || !current(scope) || !first || paging.value) return
    paging.value = true
    try {
      const page = await request<Message[]>(scope, `/sessions/${scope.id}/messages?before=${first.sequence}`)
      if (!current(scope)) return
      const next = [...page, ...state.messages]
      state.hasNewer ||= next.length > messageWindowLimit
      state.messages = next.slice(0, messageWindowLimit)
      state.hasOlder = page.length === 100
    } catch (failure) { report(scope, failure) }
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
      state.hasOlder ||= next.length > messageWindowLimit
      state.messages = next.slice(-messageWindowLimit)
      state.hasNewer = page.length === 100 || (state.messages.at(-1)?.sequence || 0) < state.latestMessageSequence
    } catch (failure) { report(scope, failure) }
    finally { if (current(scope)) paging.value = false }
  }

  function hasVisibleRun(runId: string) { return !!runId && messages.value.some(message => message.runId === runId) }
  const traceSegments = computed(() => replyTraceSegments(messages.value, agentWorking.value ? activeRunID.value : '', state.hasNewer))
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
  function resultsForTrace(message: Message) {
    const segment = traceSegment(message)
    return segment ? resultsForRun(message.runId).filter(result => result.sequence > segment.after && (!segment.through || result.sequence <= segment.through)) : []
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
    if (!message.outputTurn || !message.outputBytes || message.outputBytes <= 8192) return message.content
    return (await loadFullModelOutput(message)).content
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
      quotedText.value = editBackup.quote
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
      editBackup ||= { draft: draft.value, quote: quotedText.value, images: [...attachedImages.value] }
      editingMessage.value = result.message
      draft.value = result.message.content
      quotedText.value = null
      attachedImages.value = files
      error.value = ''
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { endBusy(scope, owner) }
  }
  async function reloadAfterRollback(clearEdit = true) {
    const session = selected.value
    if (!session) return
    const saved = { draft: draft.value, quote: quotedText.value, images: [...attachedImages.value], edit: editingMessage.value, backup: editBackup }
    const nextGeneration = generation + 1
    await selectSession(session)
    if (selected.value?.id !== session.id || generation !== nextGeneration) return
    draft.value = saved.draft
    quotedText.value = saved.quote
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
      draft.value = result.message.content
      quotedText.value = null
      attachedImages.value = files
      await reloadAfterRollback()
      return true
    } catch (failure) {
      if (current(scope)) { rollbackFailure.value = String(failure); error.value = rollbackFailure.value; await refresh() }
      return false
    } finally { endBusy(scope, owner) }
  }

  async function recallLastUserMessage(): Promise<boolean> {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.open || draft.value || quotedText.value || attachedImages.value.length || busy.value || recallingScope === scope) return false
    recallingScope = scope
    const revision = draftRevision
    const canRestore = () => current(scope) && draftRevision === revision && !draft.value && !quotedText.value && !attachedImages.value.length && !busy.value
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
    quotedText.value = { text: quote.text, role: quote.role }
  }
  function removeQuote() { quotedText.value = null }
  async function applySentMessage(scope: Scope, result: SendMessageResult) {
    if (!current(scope)) return
    if (state.hasNewer) { await selectSession(selected.value!); return }
    addMessage(result.message)
    if (result.message?.role === 'user') latestUserSequence.value = Math.max(latestUserSequence.value, result.message.sequence)
    if (result.runId && !state.finishedRuns[result.runId] && (!state.activeRunID || state.activeRunID === result.runId || (result.message?.sequence || 0) >= state.activeRunSequence)) {
      state.activeRunID = result.runId
      state.activeRunSequence = result.message?.sequence || state.activeRunSequence
      if (state.runProgress?.runId !== result.runId) state.runProgress = { runId: result.runId, phase: 'starting' }
      if (selected.value) selected.value = { ...selected.value, executing: true, pendingReply: undefined }
    }
    if (result.input) state.queuedInputs = [...state.queuedInputs.filter(input => input.id !== result.input!.id), result.input]
    error.value = ''
  }
  async function send(requestedMode: 'now' | 'later' = 'now') {
    if (busy.value || manualCompacting.value || compactionRequestPending.value || (!draft.value.trim() && !attachedImages.value.length)) return
    let scope = currentScope
    let session = selected.value
    if (session && (!scope || !current(scope) || !session.open)) return
    if (!session && (!interactionPreferences.ready.value || !modelCatalog.value.models.some(model => model.key === selectedModel.value && !!model.provider))) return
    const text = draft.value
    const quote = quotedText.value
    const content = formatQuotedReply(text, quote)
    const files = [...attachedImages.value]
    const edited = editingMessage.value
    const mode = session?.executing && !edited ? requestedMode : 'now'
    const modeChoice = selectionMode.value
    const tierChoice = selectionTier.value
    const modelKey = modeChoice === 'manual' ? selectedModel.value : undefined
    const reasoningEffort = modeChoice === 'manual' ? selectedReasoning.value : undefined
    rememberSelectedModel()
    const owner = startBusy()
    const startedGeneration = generation
    try {
      if (!session) {
        const controller = new AbortController()
        firstSendController = controller
        await interactionPreferences.flush()
        if (generation !== startedGeneration || selected.value || controller.signal.aborted) return
        const created = await client.request<Session>('/sessions', { method: 'POST', body: '{}', signal: controller.signal })
        if (generation !== startedGeneration || selected.value || controller.signal.aborted) return
        firstSendController = null
        session = { ...created, selectionMode: modeChoice, selectionTier: tierChoice,
          ...(modeChoice === 'manual' ? { manualModelKey: modelKey, manualReasoningEffort: reasoningEffort } : {}) }
        sessions.value = [...sessions.value.filter(item => item.id !== session!.id), session]
        scope = { id: session.id, generation, controller: new AbortController() }
        currentScope = scope
        selected.value = session
        welcomeImages = []
        try {
          localStorage.removeItem(welcomeDraftStorageKey)
          if (draft.value) localStorage.setItem(draftStoragePrefix + session.id, draft.value)
        } catch { /* The in-memory draft remains available. */ }
        await connectStream(scope)
        await onSessionStarted?.(session)
      }
      if (!scope) return
      if (!current(scope)) return
      const images: ImageRef[] = []
      for (const file of files) {
        images.push(await request<ImageRef>(scope, `/sessions/${scope.id}/resources?name=${encodeURIComponent(file.name)}`, {
          method: 'POST', headers: { 'Content-Type': file.type }, body: file,
        }))
        if (!current(scope)) return
      }
      const result = await request<SendMessageResult>(scope, `/sessions/${scope.id}/messages`, { method: 'POST', body: JSON.stringify({ content, images, mode, modelKey, reasoningEffort, selectionMode: modeChoice, selectionTier: tierChoice, ...(edited ? { replacesMessageId: edited.id } : {}) }) })
      if (!current(scope)) return
      if (draft.value === text) draft.value = ''
      if (quotedText.value === quote) quotedText.value = null
      if (attachedImages.value.length === files.length && files.every((file, index) => attachedImages.value[index] === file)) attachedImages.value = []
      if (edited) {
        editingMessage.value = null
        editBackup = null
        await reloadAfterRollback()
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
  function submitAlternateMessage() { if (!busy.value && (!attachedImages.value.length || canAttachImage.value)) void send('later') }
  async function submitQuestion(runId: string, answers: QuestionAnswer[]): Promise<boolean> {
    const scope = currentScope
    const session = selected.value
    const pending = session?.pendingReply
    if (!scope || !current(scope) || !session?.open || session.executing || busy.value || pending?.kind !== 'question' || pending.runId !== runId) return false
    if (!pending.questions?.length || answers.length !== pending.questions.length) return false
    const owner = startBusy()
    rememberSelectedModel()
    try {
      const manual = selectionMode.value === 'manual'
      const result = await request<SendMessageResult>(scope, `/sessions/${scope.id}/messages`, {
        method: 'POST', body: JSON.stringify({
          questionReply: { runId, answers }, selectionMode: selectionMode.value, selectionTier: selectionTier.value,
          ...(manual ? { modelKey: selectedModel.value, reasoningEffort: selectedReasoning.value } : {}),
        }),
      })
      if (!current(scope)) return false
      if (selected.value?.pendingReply?.runId === runId) selected.value = { ...selected.value, pendingReply: undefined }
      await applySentMessage(scope, result)
      return true
    } catch (failure) { report(scope, failure); return false }
    finally { endBusy(scope, owner) }
  }
  async function cancelExecution() {
    const scope = currentScope
    if (!scope || !current(scope) || !selected.value?.executing || busy.value) return
    const owner = startBusy()
    try {
      await request(scope, `/sessions/${scope.id}/cancel`, { method: 'POST' })
    } catch (failure) { report(scope, failure) }
    finally { endBusy(scope, owner) }
  }
  async function mutateInput(input: QueuedInput, action: 'edit' | 'delete' | 'withdraw', patch?: { content?: string; mode?: 'now' | 'later' }) {
    const scope = currentScope
    if (!scope || !current(scope) || pendingBusy.value[input.id]) return false
    pendingBusy.value = { ...pendingBusy.value, [input.id]: true }
    try {
      const result = await request<{ input: QueuedInput; event?: StreamEvent; images?: Array<{ mediaType: string; hash: string; name?: string }> }>(scope,
        `/sessions/${scope.id}/inputs/${input.id}${action === 'withdraw' ? '/withdraw' : ''}`,
        { method: action === 'edit' ? 'PATCH' : action === 'delete' ? 'DELETE' : 'POST', ...(patch ? { body: JSON.stringify(patch) } : {}) })
      if (!current(scope)) return false
      inputRevision++
      state.queuedInputs = action === 'edit' ? state.queuedInputs.map(item => item.id === input.id ? result.input : item) : state.queuedInputs.filter(item => item.id !== input.id)
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
    } catch (failure) { report(scope, failure); if (current(scope)) await refreshConversation(); return false }
    finally { if (current(scope)) pendingBusy.value = { ...pendingBusy.value, [input.id]: false } }
  }
  async function resumeInputs() {
    const scope = currentScope
    if (!scope || !current(scope)) return
    try {
      await request(scope, `/sessions/${scope.id}/inputs/resume`, { method: 'POST', body: '{}' })
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
    if (!message.outputTurn) return Promise.resolve()
    if (modelOutputFailure.value[message.id] && !retry) return Promise.resolve()
    return loadFullModelOutput(message).then(() => {}, () => {})
  }

  function loadMainOutput(record: MainOutputRecord): Promise<void> {
    const scope = currentScope
    if (!scope || !current(scope)) return Promise.reject(new DOMException('Session changed', 'AbortError'))
    const key = mainOutputKey(record)
    const latest = state.mainOutputs.find(output => mainOutputKey(output) === key)
    if (!latest || latest.loaded) return Promise.resolve()
    const pending = mainOutputRequests.get(key)
    if (pending) return pending
    modelOutputLoading.value[key] = true
    modelOutputFailure.value[key] = ''
    const snapshot = request<OutputView>(scope, `/sessions/${encodeURIComponent(scope.id)}/runs/${encodeURIComponent(record.runId)}/model-output?turn=${record.turn}&full=1`)
    const loading = snapshot.then(output => {
      if (!current(scope)) throw new DOMException('Session changed', 'AbortError')
      if (output.offset !== 0 || output.nextOffset !== output.availableBytes || !output.lastSequence) throw new Error('Incomplete output response')
      if (mainOutputRequests.get(key) === loading) state.mainOutputs = pruneConversationWindow({ ...state, mainOutputs: state.mainOutputs.map(item => mainOutputKey(item) === key ? hydrateMainOutput(item, output) : item) }).mainOutputs
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
    if (!message.mainOutput) return readModelOutput(message, retry)
    if (modelOutputFailure.value[message.id] && !retry) return Promise.resolve()
    return loadMainOutput(message.mainOutput).catch(() => {})
  }

  return {
    editingMessage, beginEditMessage, cancelEditMessage, withdrawMessage, rollbackFailure,
    ...refs, displayMessages, readDisplayOutput, olderActivity, hasOlderActivity, activityPaging, paging, selectedModel, selectedModelInfo,
    selectedReasoning, reasoningChoices, canAttachImage, attachedImages, attachedPreviewURLs,
    selectionMode, selectionTier, chooseSelectionMode, chooseSelectionTier, feedbackInvitationOpen,
    feedback, feedbackBusy, rateMessage, pendingInputs, inputQueuePaused, pendingBusy, mutateInput, resumeInputs, compaction, contextUsage, canCompact, compactSession, manualCompacting, compactionRequestPending, continueAfterCompaction, fillContinueDraft,
    draft, quotedText, quoteSelection, removeQuote, agentWorking, agentStatus, hasTaskDetails, chooseModel, recallLastUserMessage,
    synchronization, syncDiagnostics, viewRevision, resynchronize, renderFailure,
    selectSession, clearSession, refreshConversation, loadOlderActivity, showRecentActivity, loadOlder, loadNewer, markRead,
    send, submitMessage, submitAlternateMessage, submitQuestion, cancelExecution, readModelOutput, modelOutputLoading, modelOutputFailure,
    fullMessageText, resultsForMessage, resultsForRun, resultsForTrace, traceSegment, runStartedAt, runReplyTurns, runActivityAnchor, firstRunSequence, liveDrafts,
    hasVisibleRun, chooseImages, addImages, pasteImages, dropImages, removeImage,
  }
}
