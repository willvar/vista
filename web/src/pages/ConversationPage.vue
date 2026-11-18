<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Modal as VModal, Spin as VSpin } from '../ui'
import { IconMenu2, IconArrowBarDown, IconPencil, IconSubtask } from '@tabler/icons-vue'
import Bubble from '../ui/Bubble.vue'
import TimelineItem from '../TimelineItem.vue'
import ConversationSender from '../ConversationSender.vue'
import ContextCompaction from '../ContextCompaction.vue'
import ConversationExecution from '../ConversationExecution.vue'
import ConversationRecentMessage from '../ConversationRecentMessage.vue'
import VistaMark from '../VistaMark.vue'
import InlineSessionTitle from '../InlineSessionTitle.vue'
import { useConversationHistory } from '../composables/useConversationHistory'
import { useConversationScroll } from '../composables/useConversationScroll'
import RunTrace from '../RunTrace.vue'
import RenderGuard from '../RenderGuard.vue'
import MessageMarkdown from '../MessageMarkdown.vue'
import RunToolOutput from '../RunToolOutput.vue'
import BackgroundProcesses from '../BackgroundProcesses.vue'
import { conversationTimeline, recentSentMessage, type TimelineLayout, type TraceTimelineEntry } from '../state/conversationTimeline'
import { createRunTraceView, type RunTraceViewState } from '../state/runTraceView'
import type { Message } from '../client/types'
import type { ReplyTraceSegment } from '../state/conversation'
import MessageUsage from '../MessageUsage.vue'
import MessageActions from '../MessageActions.vue'
import FailureHelp from '../FailureHelp.vue'
import MessageImages from '../MessageImages.vue'
import MessageQuoteSelection from '../MessageQuoteSelection.vue'
import PendingInputs from '../PendingInputs.vue'
import WorkerStatus from '../WorkerStatus.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { messageModelIdentity } from '../composables/usePresentation'
import { displayOutputKey, needsFullMessageText, type ConversationItem } from '../state/mainOutputs.ts'
import { jobActive } from '../state/jobs.ts'
import type { QuotedExcerpt, QuoteSource } from '../state/quotedReply'

const { t } = useI18n({ useScope: 'global' })
const workspace = useWorkspace()
const { selected, needsProvider, busy, error, workerAvailable, modelLabel, modelCatalog, localTime,
  historicalModelLabel, modelNameById, formatModelWithReasoning,
  workerMemory, mobileSessionsOpen, attentionCount, debugMode, route, displaySessionTitle } = workspace
const selectedWorkerMemory = computed(() => selected.value ? workerMemory(selected.value.id) : undefined)
const renamingTitle = ref(false)
watch(() => selected.value?.id, () => { renamingTitle.value = false })
const { synchronization, snapshotReady, viewRevision, resynchronize, renderFailure,
  beginEditMessage, withdrawMessage, rollbackFailure, draft, attachedImages,
  pendingInputs, inputQueuePaused, pendingBusy, mutateInput, resumeInputs,
  displayMessages: messages, processEvents, mainOutputs, modelOutput,
  agentWorking, agentStatus, activeRunID, jobs, artifacts, runClock,
  contextUsage, canCompact, compactSession, compacting, compactionResult, compaction, manualCompacting, compactionRequestPending,
  modelByRun,
  hasOlder, hasNewer, lastEvent, loadOlder, readOutputIndexes, readDisplayOutput, releaseDisplayOutput, modelOutputLoading, modelOutputFailure,
  fullMessageText, resultsForMessage, resultsForTrace, traceSegment, trailingTrace, runReplyTurns, runActivityAnchor, firstRunSequence } = workspace.conversation
const runningJobs = computed(() => jobs.value.filter(jobActive).length)
const showExecution = computed(() => agentWorking.value || compacting.value || !!compactionResult.value)
const executionClock = computed(() => manualCompacting.value || compactionRequestPending.value || compactionResult.value ? null : runClock.value)
const withdrawTarget = ref<Message | null>(null)
watch(() => selected.value?.id, () => { withdrawTarget.value = null })
function requestWithdrawal(message: Message) { rollbackFailure.value = ''; withdrawTarget.value = message }
async function confirmWithdrawal() {
  const target = withdrawTarget.value
  if (target && await withdrawMessage(target) && withdrawTarget.value === target) withdrawTarget.value = null
}
function runModelName(message: Message) {
  if (message.contextSummary) {
    const result = resultsForMessage(message).at(-1)
    return result ? formatModelWithReasoning(historicalModelLabel({ key: message.modelKey, provider: result.provider, id: result.id }), result.reasoningEffort) : ''
  }
  return historicalModelLabel(messageModelIdentity(message, modelByRun.value))
}
const messageList = ref<HTMLElement | null>(null)
const processes = ref<InstanceType<typeof BackgroundProcesses> | null>(null)
const paused = ref(false)
const requestedTraces = shallowRef(new Set<string>())
const openingIndexes = ref(true)
let openingRead: Promise<void> | undefined
const traceEntries = shallowRef<Record<string, TraceTimelineEntry[]>>({})
const history = useConversationHistory({
  items: messages, session: () => selected.value?.id, revision: viewRevision,
  synchronized: () => snapshotReady.value,
  paused: () => paused.value,
  output: readDisplayOutput,
  text: message => message.mainOutput ? message.content : modelOutput.value[message.id]?.content ?? message.content,
  release: releaseDisplayOutput,
  hasOlder: () => hasOlder.value,
  older: loadOlder,
})
const availableTraces = computed(() => {
  if (history.start.value === undefined) return []
  const runs = new Set(history.candidates.value.map(message => message.runId))
  const sources: { segment: ReplyTraceSegment; message?: Message }[] = workspace.conversation.messages.value
    .filter(message => runs.has(message.runId) && runActivityAnchor(message) && (traceSegment(message).through ?? Infinity) >= history.start.value!)
    .map(message => ({ message, segment: traceSegment(message) }))
  if (trailingTrace.value) sources.push({ segment: trailingTrace.value })
  return sources.map(source => ({ ...source, replyTurns: runReplyTurns(source.segment.runId), modelResults: resultsForTrace(source.segment) }))
})
const traceSources = computed(() => availableTraces.value.filter(source => requestedTraces.value.has(source.segment.key)))
watch(() => selected.value?.id, () => {
  openingIndexes.value = true; openingRead = undefined
  requestedTraces.value = new Set(); traceEntries.value = {}
}, { flush: 'sync' })
const timeline = computed(() => history.prepared.value ? conversationTimeline(history.items.value,
  Object.fromEntries(traceSources.value.map(({ segment }) => [segment.key, traceEntries.value[segment.key] || []])),
  history.hasEarlier.value ? history.items.value[0]?.displaySequence ?? Infinity : 0) : [])
const empty = computed(() => !messages.value.length && !timeline.value.length)
const scrolling = useConversationScroll({ element: messageList, rows: timeline, session: () => selected.value?.id,
  prepared: history.prepared, missing: history.missing, paused, earlier: history.earlier })
const { showJumpToLatest } = scrolling
function retainHistoryWindow() {
  history.retainWindow()
  openingIndexes.value = false
}
watch(scrolling.following, following => {
  if (!following && scrolling.presented.value) retainHistoryWindow()
}, { flush: 'sync' })
const recentSent = computed(() => {
  // Navigation preparation keeps the readable window; keep its header geometry too.
  if (!scrolling.presented.value || hasNewer.value || route.name !== 'session'
    || pendingInputs.value.length || mobileSessionsOpen.value) return
  const first = scrolling.viewport.value[0]
  return recentSentMessage(messages.value, first ? timeline.value[first.index]?.sequence : undefined)
})
const visibleMessages = computed(() => scrolling.visible.value.flatMap(layout => {
  const row = timeline.value[layout.index]!
  return row.message ? [{ key: row.key, index: layout.index, message: row.message, layout }] : []
}))
const demandedRows = computed(() => scrolling.demand.value.map(layout => timeline.value[layout.index]).filter(row => !!row))
const detailedTraces = computed(() => new Set(demandedRows.value.flatMap(row => row.trace ? [row.trace] : [])))
watch([() => demandedRows.value.map(row => row.key).join('\0'), viewRevision, scrolling.presented, scrolling.following, activeRunID, agentWorking, () => trailingTrace.value?.key], () => {
  const rows = demandedRows.value
  const demandedMessages = rows.flatMap(row => row.message ? [row.message] : [])
  history.setVisible(demandedMessages.map(message => message.displayKey))
  const runs = new Set(demandedMessages.map(message => message.runId))
  if (scrolling.presented.value && !hasNewer.value && scrolling.following.value) {
    if (agentWorking.value && activeRunID.value) runs.add(activeRunID.value)
    if (trailingTrace.value) runs.add(trailingTrace.value.runId)
  }
  for (const source of traceSources.value) if (detailedTraces.value.has(source.segment.key)) runs.add(source.segment.runId)
  if (!scrolling.presented.value) return
  const next = new Set(requestedTraces.value)
  for (const source of availableTraces.value) if (runs.has(source.segment.runId)) next.add(source.segment.key)
  if (next.size !== requestedTraces.value.size) requestedTraces.value = next
  const read = readOutputIndexes([...runs])
  if (openingIndexes.value && !openingRead) {
    const first = read.then(() => {
      if (openingRead !== first) return
      if (openingIndexes.value) history.settleOpening()
      openingIndexes.value = false
    })
    openingRead = first
  }
}, { flush: 'post' })
const traceLayouts = computed(() => {
  const groups: Record<string, Record<string, TimelineLayout>> = {}
  for (const layout of scrolling.visible.value) {
    const row = timeline.value[layout.index]!
    if (row.trace) (groups[row.trace] ||= {})[row.step!] = layout
  }
  return groups
})
function receiveTrace(key: string, entries: TraceTimelineEntry[]) {
  traceEntries.value = { ...traceEntries.value, [key]: entries }
}
const traceViews = ref<Record<string, RunTraceViewState>>({})
watch(() => [...workspace.conversation.messages.value.map(message => traceSegment(message)?.key), trailingTrace.value?.key], keys => {
  const retained = new Set(keys)
  for (const key of Object.keys(traceViews.value)) if (!retained.has(key)) delete traceViews.value[key]
  const entries = Object.entries(traceEntries.value).filter(([key]) => retained.has(key))
  if (entries.length !== Object.keys(traceEntries.value).length) traceEntries.value = Object.fromEntries(entries)
})
function traceView(key: string) { return traceViews.value[key] ||= createRunTraceView() }
let readObserver: IntersectionObserver | undefined
const visibleReplies = new Set<HTMLElement>()
function markVisibleReplies() {
  if (scrolling.loading.value || document.visibilityState !== 'visible' || route.name !== 'session' || mobileSessionsOpen.value) return
  let sequence = 0
  for (const article of visibleReplies) {
    if (article.isConnected && article.dataset.sessionId === selected.value?.id) sequence = Math.max(sequence, Number(article.dataset.replySequence) || 0)
  }
  if (sequence) void workspace.conversation.markRead(sequence)
}
watch([selected, () => route.name, mobileSessionsOpen, scrolling.loading], markVisibleReplies, { flush: 'post' })
onMounted(() => document.addEventListener('visibilitychange', markVisibleReplies))
function prepareForSend() { scrolling.follow() }
watch(messageList, async element => {
  readObserver?.disconnect()
  visibleReplies.clear()
  if (!element) return
  await nextTick()
  if (messageList.value !== element) return
  readObserver = new IntersectionObserver(entries => {
    for (const entry of entries) {
      const article = entry.target as HTMLElement
      if (entry.isIntersecting && entry.intersectionRect.height > 0) visibleReplies.add(article)
      else visibleReplies.delete(article)
      if (!article.isConnected) readObserver?.unobserve(article)
    }
    markVisibleReplies()
  }, { root: element })
  element.querySelectorAll('[data-reply-sequence]').forEach(article => readObserver!.observe(article))
})
function observeMessage(element: Element | null) {
  scrolling.measure(element)
  if (element instanceof Element) {
    const article = element.querySelector('[data-reply-sequence]')
    if (article) readObserver?.observe(article)
  }
}
async function jumpToLatest() {
  scrolling.beginJump()
  const sessionId = selected.value?.id
  if (hasNewer.value && !await workspace.conversation.loadLatest()) { scrolling.inspect(); return }
  if (selected.value?.id !== sessionId) return
  await history.prepare()
  await nextTick()
  if (selected.value?.id === sessionId && scrolling.following.value) scrolling.jump()
}
const quoteSourceKey = ref('')
function locateQuote(quote: QuotedExcerpt) {
  if (quote.source) return locateSource(quote.source, t('quoteSourceUnavailable'))
}
function locateRecentSent(message: ConversationItem) {
  return locateSource({ key: message.displayKey, sequence: message.displaySequence, createdAt: message.createdAt }, t('recentSentUnavailable'))
}
async function locateSource(source: QuoteSource, unavailable: string) {
  if (!selected.value) return
  retainHistoryWindow()
  const signal = scrolling.beginLocation()
  quoteSourceKey.value = ''
  let revealed = false
  try {
    const settled = () => history.prepared.value || synchronization.value.phase === 'error'
    if (!settled()) await new Promise<void>(resolve => {
      const done = () => { stop(); signal.removeEventListener('abort', done); resolve() }
      const stop = watch(settled, ready => { if (ready) done() })
      signal.addEventListener('abort', done, { once: true })
    })
    if (signal.aborted) return
    if (!history.prepared.value) throw new Error('Could not open conversation history')
    const conversation = workspace.conversation
    if (hasNewer.value && source.sequence > (messages.value.at(-1)?.displaySequence || 0)
      && !await conversation.loadLatest()) throw new Error('Could not read the latest messages')
    const run = /^output\/([^/]+)\//.exec(source.key)?.[1]
    let indexed = false
    const covered = () => history.start.value !== undefined && history.start.value <= source.sequence
      && (!hasOlder.value || (conversation.messages.value[0]?.sequence ?? Infinity) <= source.sequence)
    while (!signal.aborted) {
      // Resolve output indexes explicitly. The target may be outside every
      // mounted range, even though its run is in the current message page.
      if (run && !indexed && (conversation.messages.value.some(message => message.runId === run) || activeRunID.value === run)) {
        if (!await readOutputIndexes([run])) throw new Error('Could not read the referenced output index')
        if (signal.aborted) return
        indexed = true
      }
      const item = history.candidates.value.find(item => item.displayKey === source.key || item.id === source.key)
      if (item) {
        await nextTick()
        if (signal.aborted) return
        revealed = scrolling.revealLocation(signal, item.displayKey, () => { error.value = unavailable })
        if (revealed) {
          quoteSourceKey.value = item.displayKey
          signal.addEventListener('abort', () => { quoteSourceKey.value = '' }, { once: true })
        }
        break
      }
      if (covered() || !history.hasEarlier.value) break
      const before = history.start.value
      if (await history.earlier() === false) throw new Error('Could not read earlier messages')
      if (history.start.value === before) break
    }
    if (!signal.aborted && !revealed) error.value = unavailable
  } catch {
    if (!signal.aborted) error.value = t('outputLoadFailed')
  } finally {
    if (!signal.aborted && !revealed) scrolling.restoreLocation(signal)
  }
}
watch(() => selected.value?.id, () => { quoteSourceKey.value = '' })
watch(() => selected.value?.id, () => {
  traceViews.value = {}
  traceEntries.value = {}
})
watch(viewRevision, () => { traceViews.value = {} })
onUnmounted(() => {
  readObserver?.disconnect()
  document.removeEventListener('visibilitychange', markVisibleReplies)
})
</script>

<template>
      <section v-if="selected" class="workspace">
        <div class="conversation-header" :class="{ 'has-recent-sent': !!recentSent }"><v-button variant="text" class="icon-button mobile-sessions-trigger" :aria-label="t('sessions')" :aria-expanded="mobileSessionsOpen" @click="mobileSessionsOpen = true"><IconMenu2 /><span v-if="attentionCount" class="session-attention-dot" role="img" :aria-label="t('sessionsNeedAttention', { count: attentionCount })"></span></v-button><div class="conversation-heading"><InlineSessionTitle :key="selected.id" :session="selected" v-model:editing="renamingTitle"><div class="session-heading-title"><h2 :title="displaySessionTitle(selected)">{{ displaySessionTitle(selected) }}</h2><v-button class="icon-button" variant="text" :aria-label="t('editTitle')" @click="renamingTitle = true"><IconPencil /></v-button></div></InlineSessionTitle></div><div class="conversation-header-tools">
          <WorkerStatus v-if="debugMode && selected.open" :key="selected.id" :memory="selectedWorkerMemory" :available="workerAvailable(selected.id)" />
          <ContextCompaction :key="selected.id" :usage="contextUsage" :disabled="!canCompact || !workerAvailable(selected.id)" @compact="compactSession" />
          <v-button v-if="jobs.length" variant="text" class="icon-button header-status-trigger background-processes-trigger"
            :aria-label="t('backgroundProcessCount', { count: runningJobs })" :title="t('backgroundProcessCount', { count: runningJobs })" @click="processes?.open()">
            <IconSubtask aria-hidden="true" /><span class="background-processes-count">{{ runningJobs }}</span></v-button>
        </div>
          <ConversationRecentMessage v-if="recentSent" :key="`${selected.id}/${recentSent.id}`" :session-id="selected.id" :message="recentSent" @locate="locateRecentSent" />
        </div>
        <v-alert v-if="needsProvider" class="session-setup-notice" type="warning" :message="t('noModelConfigured')" />
        <div v-if="synchronization.phase === 'error' || scrolling.presented.value && synchronization.phase !== 'ready' && synchronization.phase !== 'stopped'" class="conversation-sync-notice" :role="synchronization.phase === 'error' ? 'alert' : 'status'">
          <span>{{ t(synchronization.phase === 'error' ? 'conversationSyncFailed' : 'conversationSynchronizing') }}</span>
          <v-button v-if="synchronization.phase === 'error'" @click="resynchronize">{{ t('conversationResynchronize') }}</v-button>
        </div>
        <div class="workspace-body">
        <div class="conversation-column" :class="{ 'has-execution': showExecution }">
        <div class="conversation-history">
        <div ref="messageList" class="messages" :class="{ empty, 'history-preparing': !scrolling.presented.value, 'history-scroll-locked': scrolling.locked.value }"
          :data-history-start="history.items.value[0]?.displaySequence" :data-history-loading="history.loading.value" :aria-busy="scrolling.loading.value || openingIndexes" :inert="!scrolling.presented.value" @scroll="scrolling.onScroll" @wheel="scrolling.onWheel"
          @touchstart="scrolling.onTouchStart" @touchmove="scrolling.onTouchMove" @touchend.passive="scrolling.onTouchEnd" @touchcancel.passive="scrolling.onTouchEnd"
          @pointerdown.capture="scrolling.onPointerDown" @keydown.capture="scrolling.onKeyDown">
          <v-empty v-if="empty" class="vista-empty-state conversation-empty" :description="t('conversationEmpty')" />
          <div class="message-space virtualized" :style="{ height: `${scrolling.totalSize.value}px` }" :data-count="history.items.value.length" :data-row-count="timeline.length">
          <RenderGuard v-for="source in traceSources" :key="`${source.segment.key}/${viewRevision}`" :reset-key="source.segment.key" @failure="renderFailure"><RunTrace
            :key="`${source.segment.key}/${viewRevision}`"
            :detailed="detailedTraces.has(source.segment.key)"
            :session-id="selected.id"
            :run-id="source.segment.runId"
            :view-state="traceView(source.segment.key)"
            :paused="paused" :layout="traceLayouts[source.segment.key] || {}" :placeholders="scrolling.placeholders.value"
            @entries="entries => receiveTrace(source.segment.key, entries)" @measure="scrolling.measure"
            :events="processEvents"
            :active="source.segment.active"
            :after="source.segment.after"
            :through="source.segment.through"
            :status="agentStatus"
            :before="Math.max(source.message?.sequence ?? 0, lastEvent) + 1"
            :first="source.segment.firstSequence ?? firstRunSequence(source.segment.runId)"
            :reply-turns="source.replyTurns"
            :model-results="source.modelResults"
            :started-at="source.segment.startedAt"
            :model-name="source.message ? runModelName(source.message) : undefined"
            @inspect="scrolling.inspect"
          >
            <template #tool-output="{ node, expanded }">
              <RunToolOutput
                :session-id="selected.id"
                :node="node"
                :expanded="expanded"
                :run-id="source.segment.runId"
                :all-artifacts="artifacts"
                :all-jobs="jobs"
                @open-job="id => processes?.open(id)"
              />
            </template>

          </RunTrace><template #fallback="{ retry }"><div class="conversation-render-notice"><span>{{ t('executionDetailsUnavailable') }}</span><v-button @click="retry">{{ t('retry') }}</v-button></div></template></RenderGuard>

          <TimelineItem v-for="row in visibleMessages" :key="row.key" :layout="row.layout" :paused="paused || history.missing.value.has(row.key)" :placeholder="scrolling.placeholders.value.has(row.key)" @measure="observeMessage" :data-display-key="row.message.displayKey" :data-timeline-sequence="row.message.displaySequence" :data-progress="row.message.progress || undefined"
            class="timeline-row message-row" :class="[row.message.role, { 'quote-source-target': quoteSourceKey === row.message.displayKey }]">
          <div v-if="history.missing.value.has(row.key)" class="message-output-loading" role="status">{{ t('outputLoading') }}</div>
          <article v-else :data-session-id="selected.id" :data-context-summary="row.message.contextSummary || undefined" :data-reply-sequence="row.message.role === 'assistant' && !row.message.contextSummary && !row.message.progress ? row.message.sequence : undefined" :class="[row.message.role, { 'question-reply': row.message.questionCallId, 'main-output': row.message.progress, 'live-response': row.message.progress && !row.message.mainOutput?.complete && agentWorking }]"><Bubble :streaming="!!row.message.progress && !row.message.mainOutput?.complete && agentWorking" :content="row.message.content" :placement="row.message.role === 'user' ? 'end' : 'start'" :variant="row.message.role === 'user' ? 'filled' : 'borderless'" :styles="{ body: { maxWidth: '100%', width: row.message.role === 'assistant' ? '100%' : undefined } }"><template #header><div class="message-meta"><time :datetime="row.message.createdAt" :title="localTime(row.message.createdAt)">{{ localTime(row.message.createdAt) }}</time></div></template><template #content><div v-if="row.message.failure" class="markdown run-failure-message" :data-quote-role="row.message.role" role="alert">{{ row.message.content }}</div><MessageMarkdown v-else :content="row.message.content" :role="row.message.role" @failure="renderFailure" /><FailureHelp v-if="row.message.failure" :failure="{ kind: 'execution', runId: row.message.runId, error: row.message.content, sequence: row.message.sequence, messageId: row.message.id, createdAt: row.message.createdAt, provider: row.message.provider, modelId: row.message.modelId, modelKey: row.message.modelKey }" /><MessageImages v-if="row.message.imageCount && selected" :session-id="selected.id" :message-id="row.message.id" :count="row.message.imageCount!" /><small v-if="modelOutputLoading[displayOutputKey(row.message)]" class="message-output-loading" role="status">{{ t('outputLoading') }}</small><div v-if="modelOutputFailure[displayOutputKey(row.message)]" class="message-output-error"><small role="alert" :title="modelOutputFailure[displayOutputKey(row.message)]">{{ t('outputLoadFailed') }}</small><v-button @click="history.retry(row.message)">{{ t('retry') }}</v-button></div></template><template v-if="!row.message.progress" #footer><div class="reply-footer"><MessageActions :allow-mutation="row.message.role === 'user'" :mutation-disabled="busy || !selected.open" @edit="beginEditMessage(row.message)" @withdraw="requestWithdrawal(row.message)" :text="row.message.content" :full-text="row.message.mainOutput || needsFullMessageText(row.message) ? () => fullMessageText(row.message) : undefined" /><MessageUsage v-if="row.message.role === 'assistant'" :results="resultsForMessage(row.message)" :model-name="runModelName(row.message)" /></div></template></Bubble></article>
        </TimelineItem></div>

        </div>
        <div v-if="scrolling.loading.value || history.prepending.value" class="history-loading" :class="{ 'history-loading-earlier': scrolling.presented.value }" role="status" :aria-label="t('uiLoading')"><v-spin /></div>
        <Transition name="jump-latest">
          <v-button v-if="scrolling.presented.value && showJumpToLatest" class="jump-latest" variant="text" :aria-label="t('jumpToLatest')" @click="jumpToLatest"><IconArrowBarDown aria-hidden="true" /><span>{{ t('jumpToLatest') }}</span></v-button>
        </Transition>

        </div>
        <MessageQuoteSelection :root="messageList" :session-id="selected.id" :disabled="busy || !selected.open || route.name !== 'session' || mobileSessionsOpen" @quote="workspace.conversation.quoteSelection" />
        <PendingInputs :items="pendingInputs" :paused="inputQueuePaused" :busy="pendingBusy" :mutate="mutateInput" @resume="resumeInputs" />
        <ConversationSender @send="prepareForSend" @locate-quote="locateQuote">
          <template v-if="showExecution" #execution>
            <ConversationExecution :key="selected.id" :status="agentStatus" :clock="executionClock" :compacting="compacting" :manual="manualCompacting || compactionRequestPending" :compaction="compactionResult || (compacting ? compaction : null)" />
          </template>
        </ConversationSender>
        </div></div>
      </section>
      <section v-else class="workspace welcome">
        <div class="welcome-toolbar"><v-button variant="text" class="icon-button mobile-sessions-trigger" :aria-label="t('sessions')" :aria-expanded="mobileSessionsOpen" @click="mobileSessionsOpen = true"><IconMenu2 /><span v-if="attentionCount" class="session-attention-dot" role="img" :aria-label="t('sessionsNeedAttention', { count: attentionCount })"></span></v-button></div>
        <div class="welcome-content">
          <div class="welcome-intro"><div class="welcome-icon"><VistaMark /></div><h2>{{ t('welcomeHeading') }}</h2></div>
          <ConversationSender @send="prepareForSend" />
          <p v-if="!needsProvider" class="composer-hint"><kbd>Enter</kbd> {{ t('enterToSend') }}<span>·</span><kbd>Shift + Enter</kbd> {{ t('newLine') }}</p>
        </div>
      </section>

      <BackgroundProcesses v-if="selected" :key="selected.id" ref="processes" :session-id="selected.id" :jobs="jobs" />
      <v-modal :open="!!withdrawTarget" centered :width="480" :title="t('withdrawMessageTitle')" :confirm-loading="busy" :ok-text="t('withdrawMessage')" :cancel-text="t('cancel')" :mask-closable="!busy" :closable="!busy" :keyboard="!busy" :cancel-button-props="{ disabled: busy }" @cancel="withdrawTarget = null" @ok="confirmWithdrawal">
        <p>{{ t('withdrawMessageBody') }}</p>
        <p class="rollback-scope">{{ t('rollbackFilesRetained') }}</p>
        <p v-if="draft || attachedImages.length">{{ t('withdrawDraftWarning') }}</p>
        <v-alert v-if="rollbackFailure" type="error" show-icon :message="rollbackFailure" />
      </v-modal>
</template>

<style scoped>
.quote-source-target{border-inline-start:2px solid var(--accent);border-radius:2px}
.conversation-header.has-recent-sent{flex-wrap:wrap;row-gap:6px}
@media(max-height:520px){.conversation-header.has-recent-sent{flex-wrap:nowrap}}
.background-processes-trigger.ui-btn{gap:.25rem;min-width:32px;width:auto}
.background-processes-count{font-size:12px;font-variant-numeric:tabular-nums}
.conversation-sync-notice,.conversation-render-notice{display:flex;align-items:center;gap:12px;color:var(--muted);font-size:var(--font-size-body);line-height:1.6}
.conversation-sync-notice{padding:8px 16px;border-bottom:1px solid var(--border)}
.conversation-sync-notice[role="alert"]{color:var(--danger)}
.conversation-render-notice{padding:12px 0}
@media(max-height:520px){
  .conversation-column.has-execution .jump-latest{left:auto;right:.5rem;transform:none}
  .conversation-column.has-execution .jump-latest-enter-from,.conversation-column.has-execution .jump-latest-leave-to{transform:translateY(6px) scale(.88)}
}
</style>
