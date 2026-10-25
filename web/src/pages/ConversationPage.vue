<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Input as AInput, Modal as AModal } from 'antdv-next'
import { MenuOutlined, VerticalAlignBottomOutlined, EditOutlined } from '@antdv-next/icons'
import Bubble from '@antdv-next/x/dist/bubble/Bubble'
import ConversationSender from '../ConversationSender.vue'
import VistaMark from '../VistaMark.vue'
import { elementScroll, useVirtualizer } from '@tanstack/vue-virtual'
import { provideScrollbarDrag, useDragStableMeasurement } from '../composables/useScrollbarDrag'
import RunTrace from '../RunTrace.vue'
import RenderGuard from '../RenderGuard.vue'
import MessageMarkdown from '../MessageMarkdown.vue'
import RunToolOutput from '../RunToolOutput.vue'
import BackgroundProcesses from '../BackgroundProcesses.vue'
import { createRunTraceView, type RunTraceViewState } from '../state/runTraceView'
import type { Message } from '../client/types'
import MessageUsage from '../MessageUsage.vue'
import MessageActions from '../MessageActions.vue'
import MessageImages from '../MessageImages.vue'
import MessageQuoteSelection from '../MessageQuoteSelection.vue'
import PendingInputs from '../PendingInputs.vue'
import QuestionForm from '../QuestionForm.vue'
import WorkerStatus from '../WorkerStatus.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { messageModelIdentity } from '../composables/usePresentation'

const { t } = useI18n({ useScope: 'global' })
const workspace = useWorkspace()
const { selected, needsProvider, busy, workerAvailable, modelLabel, modelCatalog, localTime, statusLabel,
  historicalModelLabel, modelNameById, formatModelWithReasoning, renamingSessionID, sessionTitleDraft, beginRename, cancelRename, saveRename,
  workerMemory, mobileSessionsOpen, attentionCount, debugMode, route, displaySessionTitle } = workspace
const selectedWorkerMemory = computed(() => selected.value ? workerMemory(selected.value.id) : undefined)
const { synchronization, viewRevision, resynchronize, renderFailure, feedbackInvitationOpen,
  beginEditMessage, withdrawMessage, rollbackFailure, draft, attachedImages,
  feedback, feedbackBusy, rateMessage, pendingInputs, inputQueuePaused, pendingBusy, mutateInput, resumeInputs,
  displayMessages: messages, processEvents, mainOutputs, modelOutput,
  contextNotices, activeRunID, agentWorking, agentStatus, jobs, artifacts,
  modelByRun,
  hasOlder, hasNewer, paging, lastEvent, loadOlder, loadNewer, submitQuestion, readDisplayOutput, modelOutputLoading, modelOutputFailure,
  fullMessageText, resultsForMessage, resultsForTrace, traceSegment, runReplyTurns, runActivityAnchor, firstRunSequence } = workspace.conversation
const pendingQuestion = computed(() => selected.value?.pendingReply?.kind === 'question' && selected.value.pendingReply.questions?.length ? selected.value.pendingReply : null)
const questionMessageID = computed(() => pendingQuestion.value ? messages.value.filter(message =>
  message.role === 'assistant' && message.runId === pendingQuestion.value!.runId && !message.outputTurn && message.content === pendingQuestion.value!.content).at(-1)?.id || '' : '')
const withdrawTarget = ref<Message | null>(null)
watch(() => selected.value?.id, () => { withdrawTarget.value = null })
function requestWithdrawal(message: Message) { rollbackFailure.value = ''; withdrawTarget.value = message }
async function confirmWithdrawal() {
  const target = withdrawTarget.value
  if (target && await withdrawMessage(target) && withdrawTarget.value === target) withdrawTarget.value = null
}
function runModelName(message: Message) {
  return historicalModelLabel(messageModelIdentity(message, modelByRun.value))
}
const messageList = ref<HTMLElement | null>(null)
const processes = ref<InstanceType<typeof BackgroundProcesses> | null>(null)
let dragReleaseFrame = 0
let inspectingProcess = false
const { dragging: scrollbarDragging, pointerDown: scrollbarPointerDown, finish: finishScrollbarDrag } = provideScrollbarDrag(messageList, () => {
  cancelAnimationFrame(dragReleaseFrame)
  followingMessages.value = false
  inspectingProcess = false
  smoothFollowingUntil = 0
}, () => {
  const element = messageList.value
  if (!element) return
  const atEnd = !!element && element.scrollHeight - element.clientHeight - element.scrollTop <= 3
  const viewport = element.getBoundingClientRect()
  const anchor = [...element.querySelectorAll<HTMLElement>('.antd-thought-chain-node-title [id], .message-row article, .trace-report .markdown > *')]
    .find(item => { const bounds = item.getBoundingClientRect(); return bounds.top >= viewport.top && bounds.top < viewport.bottom && bounds.height > 0 })
  const anchorTop = anchor?.getBoundingClientRect().top
  followingMessages.value = atEnd
  // Inner lists resume measurement first, then the outer message list. Keep
  // the row visible at release in place after both commit their real heights.
  void nextTick(() => {
    dragReleaseFrame = requestAnimationFrame(() => {
      dragReleaseFrame = 0
      if (messageList.value !== element || scrollbarDragging.value) return
      if (atEnd) scrollToLatest()
      else if (anchor?.isConnected && anchorTop !== undefined) element.scrollTop += anchor.getBoundingClientRect().top - anchorTop
    })
  })
})
const measureMessageSize = useDragStableMeasurement(scrollbarDragging, () => messageVirtualizer.value, true)
const followingMessages = ref(true)
const showJumpToLatest = ref(false)
const traceViews = ref<Record<string, RunTraceViewState>>({})
watch(() => workspace.conversation.messages.value.map(message => traceSegment(message)?.key), keys => {
  const retained = new Set(keys)
  for (const key of Object.keys(traceViews.value)) if (!retained.has(key)) delete traceViews.value[key]
})
function traceView(message: Message) { return traceViews.value[traceSegment(message).key] ||= createRunTraceView() }
const contextDetailsRunId = computed(() => activeRunID.value || messages.value.at(-1)?.runId || '')
let smoothFollowingUntil = 0
let observer: ResizeObserver | undefined
let outputObserver: IntersectionObserver | undefined
let readObserver: IntersectionObserver | undefined
const visibleReplies = new Set<HTMLElement>()
function markVisibleReplies() {
  if (document.visibilityState !== 'visible' || route.name !== 'session' || mobileSessionsOpen.value || feedbackInvitationOpen.value) return
  let sequence = 0
  for (const article of visibleReplies) {
    if (article.isConnected && article.dataset.sessionId === selected.value?.id) sequence = Math.max(sequence, Number(article.dataset.replySequence) || 0)
  }
  if (sequence) void workspace.conversation.markRead(sequence)
}
watch([selected, () => route.name, mobileSessionsOpen, feedbackInvitationOpen], markVisibleReplies, { flush: 'post' })
onMounted(() => document.addEventListener('visibilitychange', markVisibleReplies))
const visibleOutputKeys = ref(new Set<string>())
function readVisibleOutput(index: number) {
  const message = messages.value[index]
  if (message?.mainOutput || message?.role === 'assistant' && (message.outputBytes || 0) > 8192) void readDisplayOutput(message)
}
function noticesForTrace(message: Message) {
  const segment = traceSegment(message)
  return segment ? contextNotices.value.filter(notice => (notice.runId ? notice.runId === message.runId : message.runId === contextDetailsRunId.value)
    && notice.sequence > segment.after && (!segment.through || notice.sequence <= segment.through)) : []
}
function updateJumpVisibility() {
  const element = messageList.value
  showJumpToLatest.value = !!element && element.scrollHeight - element.scrollTop - element.clientHeight > Math.max(400, element.clientHeight)
}
function updateMessageScroll() {
  const element = messageList.value
  if (!element) return
  if (scrollbarDragging.value) { updateJumpVisibility(); return }
  const distance = element.scrollHeight - element.scrollTop - element.clientHeight
  if (Date.now() < smoothFollowingUntil && distance > 10) return
  smoothFollowingUntil = 0
  // Collapsing a tall process can clamp scrollTop to the bottom. That layout
  // change must not resume following while the user is navigating its steps.
  followingMessages.value = distance <= 96 && !inspectingProcess
  updateJumpVisibility()
}
function manualScrollIntent() {
  if (messageList.value?.classList.contains('usage-scroll-locked')) return
  cancelAnimationFrame(dragReleaseFrame)
  smoothFollowingUntil = 0
  inspectingProcess = false
}
function inspectProcess() {
  inspectingProcess = true
  followingMessages.value = false
  smoothFollowingUntil = 0
}
function preserveTraceScroll(event: Event) {
  if (event instanceof PointerEvent) scrollbarPointerDown(event)
  const target = event.target
  if (target instanceof Element && target.closest('.run-trace')) inspectProcess()
}
function scrollToLatest(behavior: ScrollBehavior = 'auto') {
  const element = messageList.value
  if (!element || scrollbarDragging.value) return
  const motion = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : behavior
  smoothFollowingUntil = motion === 'smooth' ? Date.now() + 1500 : 0
  element.scrollTo({ top: element.scrollHeight, behavior: motion })
  followingMessages.value = true
  inspectingProcess = false
  showJumpToLatest.value = false
}
function prepareForSend() {
  cancelAnimationFrame(dragReleaseFrame)
  smoothFollowingUntil = 0
  followingMessages.value = true
  inspectingProcess = false
  scrollToLatest()
  void nextTick(() => {
    scrollToLatest()
    requestAnimationFrame(() => scrollToLatest())
  })
}
watch(viewRevision, () => {
  const element = messageList.value
  if (!element || followingMessages.value) return
  const viewport = element.getBoundingClientRect()
  const anchor = [...element.querySelectorAll<HTMLElement>('.message-row')].find(row => row.getBoundingClientRect().bottom > viewport.top)
  const key = anchor?.dataset.displayKey
  const top = anchor?.getBoundingClientRect().top
  const sessionId = selected.value?.id
  void nextTick(() => requestAnimationFrame(() => {
    if (messageList.value !== element || selected.value?.id !== sessionId || followingMessages.value || !key || top === undefined) return
    const retained = [...element.querySelectorAll<HTMLElement>('.message-row')].find(row => row.dataset.displayKey === key)
    if (retained) element.scrollTop += retained.getBoundingClientRect().top - top
  }))
}, { flush: 'sync' })
watch(messageList, async element => {
  observer?.disconnect()
  outputObserver?.disconnect()
  readObserver?.disconnect()
  visibleReplies.clear()
  if (!element) return
  await nextTick()
  if (messageList.value !== element) return
  observer = new ResizeObserver(() => { if (followingMessages.value) scrollToLatest(); else updateJumpVisibility() })
  observer.observe(element.querySelector('.message-space') || element)
  observer.observe(element)
  outputObserver = new IntersectionObserver(entries => {
    const visible = new Set(visibleOutputKeys.value)
    for (const entry of entries) {
      const row = entry.target as HTMLElement
      const key = row.dataset.displayKey!
      if (entry.isIntersecting) { visible.add(key); readVisibleOutput(Number(row.dataset.index)) }
      else visible.delete(key)
      if (!row.isConnected) outputObserver?.unobserve(row)
    }
    visibleOutputKeys.value = visible
  }, { root: element, rootMargin: '200px' })
  element.querySelectorAll('.message-row').forEach(row => outputObserver!.observe(row))
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
const messageVirtualizer = useVirtualizer(computed(() => ({
  count: messages.value.length,
  getScrollElement: () => messageList.value,
  estimateSize: () => 112,
  overscan: 5,
  getItemKey: (index: number) => messages.value[index]?.displayKey ?? index,
  measureElement: measureMessageSize,
  scrollToFn: (...args: Parameters<typeof elementScroll<HTMLElement>>) => { if (!scrollbarDragging.value) elementScroll(...args) },
  useAnimationFrameWithResizeObserver: true,
})))
const virtualizedMessages = computed(() => messages.value.length > 100)
const visibleMessages = computed(() => {
  const rows = virtualizedMessages.value ? messageVirtualizer.value.getVirtualItems()
    : messages.value.map((message, index) => ({ key: message.displayKey, index, start: 0 }))
  return rows.flatMap(row => messages.value[row.index] ? [{ ...row, message: messages.value[row.index] }] : [])
})
watch(() => [visibleMessages.value, messages.value, visibleOutputKeys.value], () => {
  for (const row of visibleMessages.value) if (visibleOutputKeys.value.has(messages.value[row.index]?.displayKey)) readVisibleOutput(row.index)
}, { immediate: true })
function measureMessage(element: Element | ComponentPublicInstance | null) {
  if (element instanceof Element) {
    if (virtualizedMessages.value) messageVirtualizer.value.measureElement(element)
    outputObserver?.observe(element)
    const article = element.querySelector('[data-reply-sequence]')
    if (article) readObserver?.observe(article)
  }
}
async function showOlderMessages() {
  const sessionId = selected.value?.id
  const element = messageList.value
  const previousHeight = element?.scrollHeight ?? 0
  followingMessages.value = false
  await loadOlder()
  await nextTick()
  if (element && selected.value?.id === sessionId) element.scrollTop += element.scrollHeight - previousHeight
}
watch(() => selected.value?.id, async () => {
  finishScrollbarDrag()
  traceViews.value = {}
  visibleOutputKeys.value = new Set()
  smoothFollowingUntil = 0
  followingMessages.value = true
  await nextTick()
  scrollToLatest()
})
watch(() => [messages.value.length, processEvents.value.length, mainOutputs.value.reduce((total, item) => total + item.bytes, 0)], async () => {
  if (!followingMessages.value) return
  await nextTick()
  if (followingMessages.value) scrollToLatest()
})
onUnmounted(() => {
  observer?.disconnect(); outputObserver?.disconnect(); readObserver?.disconnect()
  document.removeEventListener('visibilitychange', markVisibleReplies)
  cancelAnimationFrame(dragReleaseFrame)
})
</script>

<template>
      <section v-if="selected" class="workspace">
        <div class="conversation-header"><a-button type="text" class="mobile-sessions-trigger" :aria-label="t('sessions')" :aria-expanded="mobileSessionsOpen" @click="mobileSessionsOpen = true"><MenuOutlined /><span v-if="attentionCount" class="session-attention-dot" role="img" :aria-label="t('sessionsNeedAttention', { count: attentionCount })"></span></a-button><div class="conversation-heading"><form v-if="renamingSessionID === selected.id" class="session-rename" @submit.prevent="saveRename(selected)"><a-input v-model:value="sessionTitleDraft" :maxlength="200" :aria-label="t('sessionTitle')" /><a-button html-type="submit" size="small" type="primary" :disabled="busy || !sessionTitleDraft.trim()">{{ t('saveChanges') }}</a-button><a-button size="small" @click="cancelRename">{{ t('cancel') }}</a-button></form><div v-else class="session-heading-title"><h2 :title="displaySessionTitle(selected)">{{ displaySessionTitle(selected) }}</h2><a-button type="text" :aria-label="t('editTitle')" @click="beginRename(selected)"><EditOutlined /></a-button></div></div><div class="conversation-header-tools">
          <WorkerStatus v-if="debugMode && selected.open" :key="selected.id" :memory="selectedWorkerMemory" :available="workerAvailable(selected.id)" />
        </div></div>
        <a-alert v-if="needsProvider" class="session-setup-notice" type="warning" :message="t('noModelConfigured')" />
        <div v-if="synchronization.phase !== 'ready' && synchronization.phase !== 'stopped'" class="conversation-sync-notice" :role="synchronization.phase === 'error' ? 'alert' : 'status'">
          <span>{{ t(synchronization.phase === 'error' ? 'conversationSyncFailed' : 'conversationSynchronizing') }}</span>
          <a-button v-if="synchronization.phase === 'error'" size="small" @click="resynchronize">{{ t('conversationResynchronize') }}</a-button>
        </div>
        <div class="workspace-body">
        <div class="conversation-column">
        <div class="conversation-history">
        <a-button v-if="hasOlder" :disabled="paging" @click="showOlderMessages">{{ t('loadOlderMessages') }}</a-button>
        <div ref="messageList" class="messages" :class="{ empty: !messages.length }" @scroll="updateMessageScroll" @wheel.passive="manualScrollIntent" @touchmove.passive="manualScrollIntent" @pointerdown.capture="preserveTraceScroll" @keydown.enter.capture="preserveTraceScroll" @keydown.space.capture="preserveTraceScroll"><p v-if="!messages.length" class="conversation-empty">{{ t('conversationEmpty') }}</p><div class="message-space" :data-count="messages.length" :class="{ virtualized: virtualizedMessages }" :style="virtualizedMessages ? { height: `${messageVirtualizer.getTotalSize()}px` } : undefined"><div v-for="row in visibleMessages" :key="String(row.key)" :ref="measureMessage" :data-index="row.index" :data-display-key="row.message.displayKey" :data-progress="row.message.progress || undefined" class="message-row" :class="row.message.role" :style="virtualizedMessages ? { transform: `translateY(${row.start}px)` } : undefined"><RenderGuard v-if="runActivityAnchor(row.message)" :reset-key="traceSegment(row.message).key" @failure="renderFailure"><RunTrace
            :session-id="selected.id"
            :run-id="row.message.runId"
            :view-state="traceView(row.message)"
            :events="processEvents"
            :active="traceSegment(row.message).active"
            :after="traceSegment(row.message).after"
            :through="traceSegment(row.message).through"
            :status="agentStatus"
            :before="Math.max(row.message.sequence, lastEvent) + 1"
            :first="firstRunSequence(row.message.runId)"
            :reply-turns="runReplyTurns(row.message.runId)"
            :context-notices="noticesForTrace(row.message)"
            :calls="resultsForTrace(row.message).length"
            :model-results="resultsForTrace(row.message)"
            :started-at="traceSegment(row.message).startedAt"
            :model-name="runModelName(row.message)"
            @inspect="inspectProcess"
          >
            <template #tool-output="{ node, expanded }">
              <RunToolOutput
                :session-id="selected.id"
                :node="node"
                :expanded="expanded"
                :run-id="row.message.runId"
                :all-artifacts="artifacts"
                :all-jobs="jobs"
                @open-job="id => processes?.open(id)"
              />
            </template>

          </RunTrace><template #fallback="{ retry }"><div class="conversation-render-notice"><span>{{ t('executionDetailsUnavailable') }}</span><a-button size="small" @click="retry">{{ t('retry') }}</a-button></div></template></RenderGuard>
          <article :data-session-id="selected.id" :data-reply-sequence="row.message.role === 'assistant' && !row.message.progress ? row.message.sequence : undefined" :class="[row.message.role, { 'main-output': row.message.progress, 'live-response': row.message.progress && !row.message.mainOutput?.complete && agentWorking }]"><Bubble :streaming="!!row.message.progress && !row.message.mainOutput?.complete && agentWorking" :content="row.message.content" :placement="row.message.role === 'user' ? 'end' : 'start'" :variant="row.message.role === 'user' ? 'filled' : 'borderless'" :styles="{ body: { maxWidth: '100%', width: row.message.role === 'assistant' ? '100%' : undefined } }"><template #header><div class="message-meta"><small v-if="row.message.role === 'user'">{{ t('you') }}</small><time :datetime="row.message.createdAt" :title="localTime(row.message.createdAt)">{{ localTime(row.message.createdAt) }}</time></div></template><template #content><QuestionForm v-if="pendingQuestion && questionMessageID === row.message.id" :key="`${selected.id}/${pendingQuestion.runId}`" :session-id="selected.id" :run-id="pendingQuestion.runId" :questions="pendingQuestion.questions!" :busy="busy || !selected.open" :submit="submitQuestion" /><div v-else-if="row.message.failure" class="markdown run-failure-message" :data-quote-role="row.message.role" role="alert">{{ row.message.content }}</div><MessageMarkdown v-else :content="modelOutput[row.message.id]?.content ?? row.message.content" :role="row.message.role" @failure="renderFailure" /><p v-if="row.message.failure" class="run-failure-help">{{ t('runFailureHelp') }}</p><MessageImages v-if="row.message.imageCount && selected" :session-id="selected.id" :message-id="row.message.id" :count="row.message.imageCount!" /><small v-if="modelOutputLoading[row.message.id]" class="message-output-loading" role="status">{{ t('outputLoading') }}</small><div v-if="modelOutputFailure[row.message.id]" class="message-output-error"><small role="alert" :title="modelOutputFailure[row.message.id]">{{ t('outputLoadFailed') }}</small><a-button size="small" @click="readDisplayOutput(row.message, true)">{{ t('retry') }}</a-button></div></template><template v-if="!row.message.progress" #footer><div class="reply-footer"><MessageActions :allow-mutation="row.message.role === 'user'" :mutation-disabled="busy || !selected.open" @edit="beginEditMessage(row.message)" @withdraw="requestWithdrawal(row.message)" :text="modelOutput[row.message.id]?.content ?? row.message.content" :allow-feedback="row.message.role === 'assistant'" :feedback="feedback[row.message.id]" :busy="feedbackBusy[row.message.id]" @feedback="rateMessage(row.message, $event)" :full-text="row.message.outputBytes && row.message.outputBytes! > 8192 ? () => fullMessageText(row.message) : undefined" /><MessageUsage v-if="row.message.role === 'assistant'" :results="resultsForMessage(row.message)" :model-name="runModelName(row.message)" /></div></template></Bubble></article>
        </div></div>
        <Bubble v-if="pendingQuestion && !questionMessageID" class="pending-question-fallback" :content="pendingQuestion.content" variant="borderless" :styles="{ body: { maxWidth: '100%', width: '100%' } }"><template #content><QuestionForm :key="`${selected.id}/${pendingQuestion.runId}`" :session-id="selected.id" :run-id="pendingQuestion.runId" :questions="pendingQuestion.questions!" :busy="busy || !selected.open" :submit="submitQuestion" /></template></Bubble>

        </div>
        <Transition name="jump-latest">
          <a-button v-if="showJumpToLatest" class="jump-latest" type="text" :aria-label="t('jumpToLatest')" :title="t('jumpToLatest')" @click="scrollToLatest('smooth')"><VerticalAlignBottomOutlined aria-hidden="true" /></a-button>
        </Transition>
        <a-button v-if="hasNewer" :disabled="paging" @click="loadNewer">{{ t('loadNewerMessages') }}</a-button>
        <PendingInputs :items="pendingInputs" :paused="inputQueuePaused" :busy="pendingBusy" :mutate="mutateInput" @resume="resumeInputs" />
        </div>
        <MessageQuoteSelection :root="messageList" :session-id="selected.id" :disabled="busy || !selected.open || route.name !== 'session' || mobileSessionsOpen || feedbackInvitationOpen" @quote="workspace.conversation.quoteSelection" />
        <ConversationSender @send="prepareForSend" @open-processes="processes?.open()" />
        </div></div>
      </section>
      <section v-else class="workspace welcome">
        <div class="welcome-toolbar"><a-button type="text" class="mobile-sessions-trigger" :aria-label="t('sessions')" :aria-expanded="mobileSessionsOpen" @click="mobileSessionsOpen = true"><MenuOutlined /><span v-if="attentionCount" class="session-attention-dot" role="img" :aria-label="t('sessionsNeedAttention', { count: attentionCount })"></span></a-button></div>
        <div class="welcome-content">
          <div class="welcome-intro"><div class="welcome-icon"><VistaMark /></div><h2>{{ t('welcomeHeading') }}</h2></div>
          <ConversationSender @send="prepareForSend" />
          <p v-if="!needsProvider" class="composer-hint"><kbd>Enter</kbd> {{ t('enterToSend') }}<span>·</span><kbd>Shift + Enter</kbd> {{ t('newLine') }}</p>
        </div>
      </section>

      <BackgroundProcesses v-if="selected" :key="selected.id" ref="processes" :session-id="selected.id" :jobs="jobs" />
      <a-modal :open="!!withdrawTarget" centered :width="480" :title="t('withdrawMessageTitle')" :confirm-loading="busy" :ok-text="t('withdrawMessage')" :cancel-text="t('cancel')" :mask-closable="!busy" :closable="!busy" :cancel-button-props="{ disabled: busy }" @cancel="withdrawTarget = null" @ok="confirmWithdrawal">
        <p>{{ t('withdrawMessageBody') }}</p>
        <p class="rollback-scope">{{ t('rollbackFilesRetained') }}</p>
        <p v-if="draft || attachedImages.length">{{ t('withdrawDraftWarning') }}</p>
        <a-alert v-if="rollbackFailure" type="error" show-icon :message="rollbackFailure" />
      </a-modal>
      <a-modal v-model:open="feedbackInvitationOpen" centered :width="460" :title="t('feedbackInvitationTitle')" :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto' } }" class="feedback-invitation">
        <p>{{ t('feedbackInvitationBody') }}</p><p class="feedback-invitation-scope">{{ t('feedbackInvitationScope') }}</p>
        <template #footer><a-button @click="feedbackInvitationOpen = false">{{ t('feedbackInvitationSkip') }}</a-button><a-button type="primary" @click="feedbackInvitationOpen = false">{{ t('feedbackInvitationAccept') }}</a-button></template>
      </a-modal>
</template>

<style scoped>
.conversation-sync-notice,.conversation-render-notice{display:flex;align-items:center;gap:12px;color:var(--muted);font-size:var(--font-size-body);line-height:1.6}
.conversation-sync-notice{padding:8px 16px;border-bottom:1px solid var(--border)}
.conversation-sync-notice[role="alert"]{color:var(--danger)}
.conversation-render-notice{padding:12px 0}
</style>
