<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Modal as VModal, Spin as VSpin } from '../ui'
import { IconMenu2, IconArrowBarDown, IconPencil } from '@tabler/icons-vue'
import Bubble from '../ui/Bubble.vue'
import TimelineItem from '../TimelineItem.vue'
import ConversationSender from '../ConversationSender.vue'
import ContextCompaction from '../ContextCompaction.vue'
import ConversationChecklist from '../ConversationChecklist.vue'
import ConversationExecution from '../ConversationExecution.vue'
import VistaMark from '../VistaMark.vue'
import InlineSessionTitle from '../InlineSessionTitle.vue'
import { useConversationHistory } from '../composables/useConversationHistory'
import { useConversationScroll } from '../composables/useConversationScroll'
import RunTrace from '../RunTrace.vue'
import RenderGuard from '../RenderGuard.vue'
import MessageMarkdown from '../MessageMarkdown.vue'
import RunToolOutput from '../RunToolOutput.vue'
import BackgroundProcesses from '../BackgroundProcesses.vue'
import { conversationTimeline, type TimelineLayout, type TraceTimelineEntry } from '../state/conversationTimeline'
import { createRunTraceView, type RunTraceViewState } from '../state/runTraceView'
import type { Message } from '../client/types'
import MessageUsage from '../MessageUsage.vue'
import MessageActions from '../MessageActions.vue'
import FailureHelp from '../FailureHelp.vue'
import MessageImages from '../MessageImages.vue'
import MessageQuoteSelection from '../MessageQuoteSelection.vue'
import PendingInputs from '../PendingInputs.vue'
import QuestionForm from '../QuestionForm.vue'
import WorkerStatus from '../WorkerStatus.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { messageModelIdentity } from '../composables/usePresentation'
import { checklistItems } from '../state/checklist.ts'
import { displayOutputKey } from '../state/mainOutputs.ts'

const { t } = useI18n({ useScope: 'global' })
const workspace = useWorkspace()
const { selected, needsProvider, busy, workerAvailable, modelLabel, modelCatalog, localTime, statusLabel,
  historicalModelLabel, modelNameById, formatModelWithReasoning,
  workerMemory, mobileSessionsOpen, attentionCount, debugMode, route, displaySessionTitle } = workspace
const selectedWorkerMemory = computed(() => selected.value ? workerMemory(selected.value.id) : undefined)
const renamingTitle = ref(false)
watch(() => selected.value?.id, () => { renamingTitle.value = false })
const { synchronization, snapshotReady, viewRevision, resynchronize, renderFailure, feedbackInvitationOpen,
  beginEditMessage, withdrawMessage, rollbackFailure, draft, attachedImages,
  feedback, feedbackBusy, rateMessage, selectionMode, pendingInputs, inputQueuePaused, pendingBusy, mutateInput, resumeInputs,
  displayMessages: messages, processEvents, mainOutputs, modelOutput,
  agentWorking, agentStatus, jobs, artifacts, currentChecklist, runClock,
  contextUsage, canCompact, compactSession,
  modelByRun,
  hasOlder, hasNewer, lastEvent, loadOlder, submitQuestion, readOutputIndexes, readDisplayOutput, releaseDisplayOutput, modelOutputLoading, modelOutputFailure,
  fullMessageText, resultsForMessage, resultsForTrace, traceSegment, runReplyTurns, runActivityAnchor, firstRunSequence } = workspace.conversation
const pendingQuestion = computed(() => selected.value?.pendingReply?.kind === 'question' && selected.value.pendingReply.questions?.length ? selected.value.pendingReply : null)
const questionMessageID = computed(() => pendingQuestion.value ? messages.value.filter(message =>
  message.role === 'assistant' && !message.contextSummary && message.runId === pendingQuestion.value!.runId && !message.outputTurn && message.content === pendingQuestion.value!.content).at(-1)?.id || '' : '')
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
function checklistText(message: Message) {
  const summary = message.checklist!
  return t('checklistSummaryProgress', { done: summary.items.filter(item => item.status === 'done').length, total: summary.items.length })
    + '\n' + checklistItems(summary.items, false, summary.status)
      .map((item, index) => t('checklistSummaryItem', { index: index + 1, status: statusLabel(item.status), text: item.text })).join('\n')
}
const messageList = ref<HTMLElement | null>(null)
const processes = ref<InstanceType<typeof BackgroundProcesses> | null>(null)
const paused = ref(false)
const expectedTraces = ref<string[]>([])
const traceEntries = shallowRef<Record<string, TraceTimelineEntry[]>>({})
const history = useConversationHistory({
  items: messages, session: () => selected.value?.id, revision: viewRevision,
  synchronized: () => snapshotReady.value,
  paused: () => paused.value,
  indexes: readOutputIndexes,
  output: message => message.mainOutput || (message.outputBytes || 0) > 8192 ? readDisplayOutput(message) : Promise.resolve(),
  text: message => message.mainOutput ? message.content : modelOutput.value[message.id]?.content ?? message.content,
  release: releaseDisplayOutput,
  traceKeys: () => expectedTraces.value,
  hasOlder: () => hasOlder.value,
  older: loadOlder,
})
const traceSources = computed(() => {
  if (history.start.value === undefined) return []
  const runs = new Set(history.candidates.value.map(message => message.runId))
  return workspace.conversation.messages.value.filter(message => runs.has(message.runId)
    && runActivityAnchor(message) && (traceSegment(message).through ?? Infinity) >= history.start.value!).map(message => ({
      message, segment: traceSegment(message), replyTurns: runReplyTurns(message.runId), modelResults: resultsForTrace(message),
    }))
})
watch(traceSources, sources => { expectedTraces.value = sources.map(source => source.segment.key) }, { immediate: true, flush: 'sync' })
const timeline = computed(() => history.prepared.value ? conversationTimeline(history.items.value,
  Object.fromEntries(expectedTraces.value.map(key => [key, traceEntries.value[key] || []])),
  history.items.value[0]?.displaySequence ?? Infinity) : [])
const scrolling = useConversationScroll({ element: messageList, rows: timeline, session: () => selected.value?.id,
  prepared: history.prepared, missing: history.missing, paused, earlier: history.earlier })
const { showJumpToLatest } = scrolling
const visibleMessages = computed(() => scrolling.visible.value.flatMap(layout => {
  const row = timeline.value[layout.index]!
  return row.message ? [{ key: row.key, index: layout.index, message: row.message, layout }] : []
}))
watch([() => visibleMessages.value.map(row => row.key), paused], ([keys]) => history.setVisible(keys), { flush: 'post' })
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
watch(() => workspace.conversation.messages.value.map(message => traceSegment(message)?.key), keys => {
  const retained = new Set(keys)
  for (const key of Object.keys(traceViews.value)) if (!retained.has(key)) delete traceViews.value[key]
})
function traceView(message: Message) { return traceViews.value[traceSegment(message).key] ||= createRunTraceView() }
let readObserver: IntersectionObserver | undefined
const visibleReplies = new Set<HTMLElement>()
function markVisibleReplies() {
  if (scrolling.loading.value || document.visibilityState !== 'visible' || route.name !== 'session' || mobileSessionsOpen.value || feedbackInvitationOpen.value) return
  let sequence = 0
  for (const article of visibleReplies) {
    if (article.isConnected && article.dataset.sessionId === selected.value?.id) sequence = Math.max(sequence, Number(article.dataset.replySequence) || 0)
  }
  if (sequence) void workspace.conversation.markRead(sequence)
}
watch([selected, () => route.name, mobileSessionsOpen, feedbackInvitationOpen, scrolling.loading], markVisibleReplies, { flush: 'post' })
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
        <div class="conversation-header"><v-button variant="text" class="icon-button mobile-sessions-trigger" :aria-label="t('sessions')" :aria-expanded="mobileSessionsOpen" @click="mobileSessionsOpen = true"><IconMenu2 /><span v-if="attentionCount" class="session-attention-dot" role="img" :aria-label="t('sessionsNeedAttention', { count: attentionCount })"></span></v-button><div class="conversation-heading"><InlineSessionTitle :key="selected.id" :session="selected" v-model:editing="renamingTitle"><div class="session-heading-title"><h2 :title="displaySessionTitle(selected)">{{ displaySessionTitle(selected) }}</h2><v-button class="icon-button" variant="text" :aria-label="t('editTitle')" @click="renamingTitle = true"><IconPencil /></v-button></div></InlineSessionTitle></div><div class="conversation-header-tools">
          <WorkerStatus v-if="debugMode && selected.open" :key="selected.id" :memory="selectedWorkerMemory" :available="workerAvailable(selected.id)" />
          <ContextCompaction :key="selected.id" :usage="contextUsage" :disabled="!canCompact || !workerAvailable(selected.id)" @compact="compactSession" />
        </div></div>
        <v-alert v-if="needsProvider" class="session-setup-notice" type="warning" :message="t('noModelConfigured')" />
        <div v-if="synchronization.phase === 'error' || scrolling.presented.value && synchronization.phase !== 'ready' && synchronization.phase !== 'stopped'" class="conversation-sync-notice" :role="synchronization.phase === 'error' ? 'alert' : 'status'">
          <span>{{ t(synchronization.phase === 'error' ? 'conversationSyncFailed' : 'conversationSynchronizing') }}</span>
          <v-button v-if="synchronization.phase === 'error'" @click="resynchronize">{{ t('conversationResynchronize') }}</v-button>
        </div>
        <div class="workspace-body">
        <div class="conversation-column" :class="{ 'has-checklist': currentChecklist.length, 'has-execution': agentWorking }">
        <div class="conversation-history">
        <div ref="messageList" class="messages" :class="{ empty: !messages.length, 'history-preparing': !scrolling.presented.value, 'history-scroll-locked': scrolling.locked.value }"
          :data-history-start="history.items.value[0]?.displaySequence" :data-history-loading="history.loading.value" :aria-busy="scrolling.loading.value" :inert="!scrolling.presented.value" @scroll="scrolling.onScroll" @wheel="scrolling.onWheel"
          @touchstart="scrolling.onTouchStart" @touchmove="scrolling.onTouchMove" @touchend.passive="scrolling.onTouchEnd" @touchcancel.passive="scrolling.onTouchEnd"
          @pointerdown.capture="scrolling.onPointerDown" @keydown.capture="scrolling.onKeyDown">
          <v-empty v-if="!messages.length" class="vista-empty-state conversation-empty" :description="t('conversationEmpty')" />
          <div class="message-space virtualized" :style="{ height: `${scrolling.totalSize.value}px` }" :data-count="history.items.value.length" :data-row-count="timeline.length">
          <RenderGuard v-for="source in traceSources" :key="`${source.segment.key}/${viewRevision}`" :reset-key="source.segment.key" @failure="error => { renderFailure(error); history.traceReady(source.segment.key, true) }"><RunTrace
            :key="`${source.segment.key}/${viewRevision}`"
            @ready="ready => history.traceReady(source.segment.key, ready)"
            :session-id="selected.id"
            :run-id="source.message.runId"
            :view-state="traceView(source.message)"
            :paused="paused" :layout="traceLayouts[source.segment.key] || {}" :placeholders="scrolling.placeholders.value"
            @entries="entries => receiveTrace(source.segment.key, entries)" @measure="scrolling.measure"
            :events="processEvents"
            :active="source.segment.active"
            :after="source.segment.after"
            :through="source.segment.through"
            :status="agentStatus"
            :before="Math.max(source.message.sequence, lastEvent) + 1"
            :first="firstRunSequence(source.message.runId)"
            :reply-turns="source.replyTurns"
            :model-results="source.modelResults"
            :started-at="source.segment.startedAt"
            :model-name="runModelName(source.message)"
            @inspect="scrolling.inspect"
          >
            <template #tool-output="{ node, expanded }">
              <RunToolOutput
                :session-id="selected.id"
                :node="node"
                :expanded="expanded"
                :run-id="source.message.runId"
                :all-artifacts="artifacts"
                :all-jobs="jobs"
                @open-job="id => processes?.open(id)"
              />
            </template>

          </RunTrace><template #fallback="{ retry }"><div class="conversation-render-notice"><span>{{ t('executionDetailsUnavailable') }}</span><v-button @click="retry">{{ t('retry') }}</v-button></div></template></RenderGuard>

          <TimelineItem v-for="row in visibleMessages" :key="row.key" :layout="row.layout" :paused="paused || history.missing.value.has(row.key)" :placeholder="scrolling.placeholders.value.has(row.key) || history.missing.value.has(row.key)" @measure="observeMessage" :data-display-key="row.message.displayKey" :data-timeline-sequence="row.message.displaySequence" :data-progress="row.message.progress || undefined"
            class="timeline-row message-row" :class="row.message.role">
          <article v-if="row.message.content || !row.message.checklist" :data-session-id="selected.id" :data-context-summary="row.message.contextSummary || undefined" :data-reply-sequence="row.message.role === 'assistant' && !row.message.contextSummary && !row.message.progress ? row.message.sequence : undefined" :class="[row.message.role, { 'question-reply': row.message.questionCallId, 'main-output': row.message.progress, 'live-response': row.message.progress && !row.message.mainOutput?.complete && agentWorking }]"><Bubble :streaming="!!row.message.progress && !row.message.mainOutput?.complete && agentWorking" :content="row.message.content" :placement="row.message.role === 'user' ? 'end' : 'start'" :variant="row.message.role === 'user' ? 'filled' : 'borderless'" :styles="{ body: { maxWidth: '100%', width: row.message.role === 'assistant' ? '100%' : undefined } }"><template #header><div class="message-meta"><time :datetime="row.message.createdAt" :title="localTime(row.message.createdAt)">{{ localTime(row.message.createdAt) }}</time></div></template><template #content><QuestionForm v-if="pendingQuestion && questionMessageID === row.message.id" :key="`${selected.id}/${pendingQuestion.runId}`" :session-id="selected.id" :run-id="pendingQuestion.runId" :questions="pendingQuestion.questions!" :busy="busy || !selected.open" :submit="submitQuestion" /><div v-else-if="row.message.failure" class="markdown run-failure-message" :data-quote-role="row.message.role" role="alert">{{ row.message.content }}</div><MessageMarkdown v-else :content="row.message.content" :role="row.message.role" @failure="renderFailure" /><FailureHelp v-if="row.message.failure" :failure="{ kind: 'execution', runId: row.message.runId, error: row.message.content, sequence: row.message.sequence, messageId: row.message.id, createdAt: row.message.createdAt, provider: row.message.provider, modelId: row.message.modelId, modelKey: row.message.modelKey }" /><MessageImages v-if="row.message.imageCount && selected" :session-id="selected.id" :message-id="row.message.id" :count="row.message.imageCount!" /><small v-if="modelOutputLoading[displayOutputKey(row.message)]" class="message-output-loading" role="status">{{ t('outputLoading') }}</small><div v-if="modelOutputFailure[displayOutputKey(row.message)]" class="message-output-error"><small role="alert" :title="modelOutputFailure[displayOutputKey(row.message)]">{{ t('outputLoadFailed') }}</small><v-button @click="readDisplayOutput(row.message, true)">{{ t('retry') }}</v-button></div></template><template v-if="!row.message.progress" #footer><div class="reply-footer"><MessageActions :allow-mutation="row.message.role === 'user'" :mutation-disabled="busy || !selected.open" @edit="beginEditMessage(row.message)" @withdraw="requestWithdrawal(row.message)" :text="row.message.content" :allow-feedback="row.message.role === 'assistant' && !row.message.contextSummary" :compact-feedback="selectionMode === 'manual'" :feedback="feedback[row.message.id]" :busy="feedbackBusy[row.message.id]" @feedback="rateMessage(row.message, $event)" :full-text="row.message.mainOutput || row.message.outputBytes && row.message.outputBytes! > 8192 ? () => fullMessageText(row.message) : undefined" /><MessageUsage v-if="row.message.role === 'assistant'" :results="resultsForMessage(row.message)" :model-name="runModelName(row.message)" /></div></template></Bubble></article>
          <article v-if="row.message.checklist" class="assistant checklist-message" :data-checklist-summary="row.message.runId">
            <Bubble variant="borderless" :styles="{ body: { maxWidth: '100%', width: '100%' } }">
              <template #header><div class="message-meta"><time :datetime="row.message.createdAt" :title="localTime(row.message.createdAt)">{{ localTime(row.message.createdAt) }}</time></div></template>
              <template #content><div class="markdown checklist-summary-text" data-quote-role="assistant">{{ checklistText(row.message) }}</div></template>
              <template #footer><div class="reply-footer"><MessageActions :text="checklistText(row.message)" /></div></template>
            </Bubble>
          </article>
        </TimelineItem></div>
        <Bubble v-if="pendingQuestion && !questionMessageID" class="pending-question-fallback" :content="pendingQuestion.content" variant="borderless" :styles="{ body: { maxWidth: '100%', width: '100%' } }"><template #content><QuestionForm :key="`${selected.id}/${pendingQuestion.runId}`" :session-id="selected.id" :run-id="pendingQuestion.runId" :questions="pendingQuestion.questions!" :busy="busy || !selected.open" :submit="submitQuestion" /></template></Bubble>

        </div>
        <div v-if="scrolling.loading.value || history.prepending.value" class="history-loading" :class="{ 'history-loading-earlier': scrolling.presented.value }" role="status" :aria-label="t('uiLoading')"><v-spin /></div>
        <Transition name="jump-latest">
          <v-button v-if="scrolling.presented.value && showJumpToLatest" class="jump-latest" variant="text" :aria-label="t('jumpToLatest')" @click="jumpToLatest"><IconArrowBarDown aria-hidden="true" /><span>{{ t('jumpToLatest') }}</span></v-button>
        </Transition>

        <PendingInputs :items="pendingInputs" :paused="inputQueuePaused" :busy="pendingBusy" :mutate="mutateInput" @resume="resumeInputs" />
        </div>
        <MessageQuoteSelection :root="messageList" :session-id="selected.id" :disabled="busy || !selected.open || route.name !== 'session' || mobileSessionsOpen || feedbackInvitationOpen" @quote="workspace.conversation.quoteSelection" />
        <ConversationExecution v-if="agentWorking" :status="agentStatus" :clock="runClock" />
        <ConversationChecklist :items="currentChecklist" :active="agentWorking" :status="selected.latestRunStatus" />
        <ConversationSender @send="prepareForSend" @open-processes="processes?.open()" />
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
      <v-modal v-model:open="feedbackInvitationOpen" centered :width="460" :title="t('feedbackInvitationTitle')" :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto' } }" class="feedback-invitation">
        <p>{{ t('feedbackInvitationBody') }}</p><p class="feedback-invitation-scope">{{ t('feedbackInvitationScope') }}</p>
        <template #footer><v-button @click="feedbackInvitationOpen = false">{{ t('feedbackInvitationSkip') }}</v-button><v-button variant="primary" @click="feedbackInvitationOpen = false">{{ t('feedbackInvitationAccept') }}</v-button></template>
      </v-modal>
</template>

<style scoped>
.checklist-summary-text{white-space:pre-wrap;overflow-wrap:anywhere}
.conversation-sync-notice,.conversation-render-notice{display:flex;align-items:center;gap:12px;color:var(--muted);font-size:var(--font-size-body);line-height:1.6}
.conversation-sync-notice{padding:8px 16px;border-bottom:1px solid var(--border)}
.conversation-sync-notice[role="alert"]{color:var(--danger)}
.conversation-render-notice{padding:12px 0}
@media(max-height:520px){
  .conversation-column.has-checklist .jump-latest,.conversation-column.has-execution .jump-latest{left:auto;right:.5rem;transform:none}
  .conversation-column.has-checklist .jump-latest-enter-from,.conversation-column.has-checklist .jump-latest-leave-to,.conversation-column.has-execution .jump-latest-enter-from,.conversation-column.has-execution .jump-latest-leave-to{transform:translateY(6px) scale(.88)}
}
</style>
