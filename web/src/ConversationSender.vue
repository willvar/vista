<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconPhoto, IconArrowRight, IconArrowUp } from '@tabler/icons-vue'
import { Button as VButton, Select as VSelect, Tooltip as VTooltip } from './ui'
import Sender from './ui/Sender.vue'
import type { TextAreaRef as SenderRef } from './ui/types'
import ReasoningEffortPicker from './ReasoningEffortPicker.vue'
import { useWorkspace } from './composables/useWorkspace'
import { useQuestionComposer } from './composables/useQuestionComposer'
import type { QuestionAnswer } from './client/types'

const QuestionForm = defineAsyncComponent(() => import('./QuestionForm.vue'))
const { t } = useI18n({ useScope: 'global' })
const emit = defineEmits<{ send: [] }>()
const { selected, busy, needsProvider, workerAvailable, modelChoices, interactionPreferences, conversation, route, mobileSessionsOpen, modalQuery } = useWorkspace()
const { draft, quotedText, removeQuote, agentWorking, selectionMode, selectionTier, chooseSelectionTier,
  editingMessage, cancelEditMessage,
  selectedModel, selectedReasoning, reasoningChoices, chooseModel,
  canAttachImage, attachedImages, attachedPreviewURLs, chooseImages, pasteImages, dropImages, removeImage,
  submitMessage, submitQuestion, cancelExecution, recallLastUserMessage,
  manualCompacting, compactionRequestPending, continueAfterCompaction, fillContinueDraft, feedbackInvitationOpen } = conversation
const senderDisabled = computed(() => manualCompacting.value || compactionRequestPending.value || !interactionPreferences.ready.value || !modelChoices.value.length
  || !!selected.value && (!selected.value.open || !workerAvailable(selected.value.id)))
const sender = ref<SenderRef | null>(null)
const composing = ref(false)
const inputEdited = ref(false)
watch(() => selected.value?.id, () => { composing.value = false; inputEdited.value = false }, { flush: 'sync' })
const questionComposer = useQuestionComposer(() => selected.value, () => {
  const input = sender.value?.input
  return composing.value || !!input && (document.activeElement === input && inputEdited.value
    && !!(draft.value || quotedText.value || attachedImages.value.length || editingMessage.value)
    || !!input.closest('.sender-card')?.querySelector('.sender-attach')?.contains(document.activeElement))
})
const { pending: pendingQuestion, requestKey: questionKey, answering } = questionComposer
const answerRegion = ref<HTMLElement | null>(null)
const inputViewKey = computed(() => {
  if (senderDisabled.value || mobileSessionsOpen.value || feedbackInvitationOpen.value) return undefined
  if (route.name === 'home') return 'home'
  if (route.name === 'session' && route.params.id === selected.value?.id) return selected.value?.id
  return undefined
})
const activeInputKey = computed(() => answering.value ? undefined : inputViewKey.value)
let lastInputKey: string | undefined
watch(activeInputKey, async id => {
  if (!id) return
  await nextTick()
  if (activeInputKey.value === id) {
    sender.value?.focus({ cursor: lastInputKey === id ? undefined : 'end', preventScroll: true })
    lastInputKey = id
  }
}, { immediate: true, flush: 'post' })
watch(quotedText, async quote => {
  if (!inputViewKey.value) return
  if (quote && pendingQuestion.value) questionComposer.showText()
  const inputKey = inputViewKey.value
  await nextTick()
  if (activeInputKey.value === inputKey && quotedText.value === quote) sender.value?.focus({ cursor: 'end', preventScroll: true })
}, { flush: 'post' })
watch(editingMessage, async message => {
  if (!message || !inputViewKey.value) return
  if (pendingQuestion.value) questionComposer.showText()
  const id = inputViewKey.value
  await nextTick()
  if (activeInputKey.value === id) sender.value?.focus({ cursor: 'end', preventScroll: true })
}, { flush: 'post' })
async function showTextReply() {
  const view = inputViewKey.value
  inputEdited.value = true
  questionComposer.showText()
  await nextTick()
  if (view && activeInputKey.value === view) sender.value?.focus({ preventScroll: true })
}
async function showAnswers() {
  if (busy.value || composing.value) return
  const key = questionKey.value, view = inputViewKey.value
  questionComposer.showAnswers()
  await nextTick()
  if (view && view === inputViewKey.value && answering.value && questionKey.value === key) answerRegion.value?.focus({ preventScroll: true })
}
async function answerQuestions(runId: string, answers: QuestionAnswer[], callId?: string) {
  const accepted = await submitQuestion(runId, answers, callId)
  if (accepted) emit('send')
  return accepted
}
const continueCompletion = computed(() => continueAfterCompaction.value && !senderDisabled.value)
const senderPlaceholder = computed(() => t(quotedText.value ? 'askAboutQuote' : continueCompletion.value ? 'continuePlaceholder' : 'sendMessage'))
const imageInput = ref<HTMLInputElement | null>(null)
const imageInputDisabled = computed(() => busy.value || senderDisabled.value || !canAttachImage.value || answering.value)
const imageDragDepth = ref(0)
const imageDropHint = computed(() => t(canAttachImage.value ? 'dropImagesHere' : 'imageUnsupported'))
function hasDraggedFiles(event: DragEvent) { return Array.from(event.dataTransfer?.types || []).includes('Files') }
function resetImageDrag() { imageDragDepth.value = 0 }
function enterImageDrag(event: DragEvent) {
  if (!hasDraggedFiles(event)) return
  event.preventDefault()
  if (!busy.value && !senderDisabled.value && !answering.value) imageDragDepth.value++
}
function overImageDrag(event: DragEvent) {
  if (!hasDraggedFiles(event)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = imageInputDisabled.value ? 'none' : 'copy'
}
function leaveImageDrag() { imageDragDepth.value = Math.max(0, imageDragDepth.value - 1) }
function receiveImageDrop(event: DragEvent) {
  resetImageDrag()
  if (!hasDraggedFiles(event)) return
  event.preventDefault()
  if (!busy.value && !senderDisabled.value && !answering.value) dropImages(event)
}
watch([senderDisabled, busy, canAttachImage, answering, () => selected.value?.id], resetImageDrag)
function pasteMessageImages(event: ClipboardEvent) { if (!answering.value) pasteImages(event) }
function chooseMessageImages(event: Event) {
  const count = attachedImages.value.length
  chooseImages(event)
  if (pendingQuestion.value && attachedImages.value.length > count) questionComposer.showText()
}
const shortViewport = window.matchMedia('(max-height: 520px)')
const smallViewport = window.matchMedia('(max-width: 830px)')
const compactSender = ref(shortViewport.matches)
const smallScreen = ref(smallViewport.matches)
const senderAutoSize = computed(() => compactSender.value
  ? { minRows: 1, maxRows: draft.value.trim() ? 4 : 1 }
  : { minRows: 2, maxRows: 8 })
const senderClass = computed(() => 'composer sender-card'
  + (selectionMode.value === 'auto' ? ' sender-card-auto' : ''))
function updateSenderSize() {
  compactSender.value = shortViewport.matches
  smallScreen.value = smallViewport.matches
}
const selectionTierOptions = computed(() => [{ label: t('speedTier'), value: 'speed' }, { label: t('qualityTier'), value: 'quality' }])
const defaultSendHint = computed(() => editingMessage.value ? t('resendEditedMessage') : agentWorking.value ? t('sendAsSteer') : t('send'))
const sendButtonLabel = computed(() => agentWorking.value && !draft.value.trim() && !attachedImages.value.length ? t('cancelExecution') : t('send'))
const sendButtonText = computed(() => t(agentWorking.value && !draft.value.trim() && !attachedImages.value.length ? 'stopResponse' : busy.value ? 'sendingMessage' : 'send'))
let compositionEndedAt = -Infinity
function trackInput(event: Event) { if (event.target === sender.value?.input) inputEdited.value = true }
function compositionStart(event: CompositionEvent) { if (event.target === sender.value?.input) composing.value = true }
function compositionEnd(event: CompositionEvent) {
  if (event.target !== sender.value?.input) return
  composing.value = false; compositionEndedAt = performance.now()
}
function notifySend() {
  if (busy.value || (!draft.value.trim() && !attachedImages.value.length) || (!!attachedImages.value.length && !canAttachImage.value)) return
  emit('send')
}
function sendMessage() {
  if (answering.value || busy.value) return
  inputEdited.value = false
  notifySend()
  submitMessage()
}
async function recallMessage(input: HTMLTextAreaElement) {
  const sessionId = selected.value?.id
  if (!await recallLastUserMessage()) return
  await nextTick()
  if (selected.value?.id === sessionId && input.isConnected && document.activeElement === input) {
    input.setSelectionRange(input.value.length, input.value.length)
  }
}
function senderKeyDown(event: KeyboardEvent) {
  if (answering.value) return false
  if ((event.key === 'ArrowUp' || event.key === 'Tab' && continueCompletion.value)
    && !event.defaultPrevented && !event.repeat && !event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
    && !event.isComposing && event.keyCode !== 229 && !composing.value && performance.now() - compositionEndedAt >= 200
    && !draft.value && !quotedText.value && !attachedImages.value.length && !senderDisabled.value && !busy.value && selected.value
    && event.target instanceof HTMLTextAreaElement && !event.target.value) {
    event.preventDefault()
    event.stopPropagation()
    if (event.key === 'Tab') fillContinueDraft()
    else void recallMessage(event.target)
    return false
  }
  if (event.key !== 'Enter' || event.shiftKey || event.altKey) return
  if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || composing.value) return false
  event.preventDefault()
  event.stopPropagation()
  // Use the same submission guard for text, quotes, and attachment-only drafts.
  if (!event.repeat && !senderDisabled.value) {
    sendMessage()
  }
  return false
}
onMounted(() => {
  shortViewport.addEventListener('change', updateSenderSize)
  smallViewport.addEventListener('change', updateSenderSize)
  window.addEventListener('dragend', resetImageDrag)
  window.addEventListener('drop', resetImageDrag)
})
onUnmounted(() => {
  shortViewport.removeEventListener('change', updateSenderSize)
  smallViewport.removeEventListener('change', updateSenderSize)
  window.removeEventListener('dragend', resetImageDrag)
  window.removeEventListener('drop', resetImageDrag)
})
</script>

<template>
        <router-link v-if="needsProvider" class="composer sender-card sender-setup" :to="{ path: '/settings/models', query: modalQuery() }">
          <span>{{ t('configureModelToStart') }}</span><IconArrowRight aria-hidden="true" />
        </router-link>
        <Sender v-else ref="sender" v-model:value="draft" :class="[senderClass, { 'sender-card-answering': answering }]" :content-active="answering" :data-composer-mode="answering ? 'answers' : pendingQuestion ? 'text' : 'message'" :auto-size="senderAutoSize" :disabled="senderDisabled" :loading="busy || agentWorking && !draft.trim() && !attachedImages.length" :placeholder="senderPlaceholder" :aria-label="t('sendMessage')" :on-key-down="senderKeyDown" @input.capture="trackInput" @compositionstart.capture="compositionStart" @compositionend.capture="compositionEnd" @paste="pasteMessageImages" @dragenter="enterImageDrag" @dragover="overImageDrag" @dragleave="leaveImageDrag" @drop="receiveImageDrop">
          <template #header>
          <div v-if="pendingQuestion && !answering" class="sender-question-notice" role="status">
            <span>{{ t('questionPendingNotice') }}</span>
            <v-button type="button" variant="text" :disabled="busy || composing" @click="showAnswers">{{ t('questionReturnToAnswers') }}</v-button>
          </div>
          <div v-if="editingMessage" v-show="!answering" class="sender-editing" role="status"><span>{{ t('editingSentMessage') }}</span><v-button variant="text" :disabled="busy" @click="cancelEditMessage">{{ t('cancelEditMessage') }}</v-button></div>
          <div v-if="quotedText" v-show="!answering" class="sender-quote" role="group" :aria-label="t('quotedText')">
            <span class="sender-quote-mark" aria-hidden="true">“</span>
            <div class="sender-quote-body"><span class="sender-quote-label">{{ t(quotedText.role === 'assistant' ? 'quotedReply' : 'quotedMessage') }}</span><p>{{ quotedText.text }}</p></div>
            <v-button variant="text" class="icon-button sender-quote-remove icon-close" :aria-label="t('removeQuote')" :disabled="busy" @click="removeQuote">×</v-button>
          </div>
          <div v-if="imageDragDepth" class="sender-drop-hint" :class="{ unsupported: !canAttachImage }" role="status"><IconPhoto /><strong>{{ imageDropHint }}</strong><small v-if="canAttachImage">{{ t('imageLimit') }}</small></div>
          <div v-if="attachedImages.length" v-show="!answering" class="sender-attachments" aria-live="polite">
            <div v-for="(file, index) in attachedImages" :key="index" class="sender-attachment">
              <img :src="attachedPreviewURLs[index]" :alt="file.name">
              <span>{{ file.name }}<small>{{ Math.ceil(file.size / 1024) }} KiB</small></span>
              <v-button variant="text" class="icon-button attachment-close icon-close" :aria-label="t('removeAttachment')" :disabled="busy" @click="removeImage(index)">×</v-button>
            </div>
          </div>

</template>
          <template v-if="pendingQuestion && selected" #content>
            <section ref="answerRegion" class="sender-question-pane" tabindex="-1" :aria-label="t('questionAnswerMode')">
              <QuestionForm :key="questionKey" :session-id="selected.id" :run-id="pendingQuestion.runId" :call-id="pendingQuestion.callId" :questions="pendingQuestion.questions" :busy="busy || senderDisabled || !answering" :submit="answerQuestions" embedded />
            </section>
          </template>
          <template #footer><div class="sender-footer">            <div class="sender-options">
              <label for="selection-tier" class="visually-hidden">{{ t('selectionTier') }}</label><label for="selected-model" class="visually-hidden">{{ t('model') }}</label>
              <v-select variant="borderless" id="selection-tier" v-if="selectionMode === 'auto'" :value="selectionTier" class="composer-tier sender-picker" :options="selectionTierOptions" :aria-label="t('selectionTier')" :title="t('autoSelectionHint')" :disabled="senderDisabled" @update:value="chooseSelectionTier($event === 'quality' ? 'quality' : 'speed')" />
              <v-select variant="borderless" id="selected-model" v-if="selectionMode === 'manual'" :value="selectedModel" class="composer-model sender-picker" :options="modelChoices" show-search option-filter-prop="searchText" :option-label-prop="smallScreen ? undefined : 'fullLabel'" :same-width="!smallScreen" :aria-label="t('model')" :disabled="senderDisabled" @update:value="chooseModel(String($event))" />
              <ReasoningEffortPicker v-if="selectionMode === 'manual' && reasoningChoices.length" v-model:value="selectedReasoning" class="composer-reasoning" :options="reasoningChoices" :disabled="senderDisabled" />
            </div>

            <div class="sender-actions">
            <v-button v-if="answering" type="button" variant="text" :disabled="busy" @click="showTextReply">{{ t('questionReplyWithText') }}</v-button>
             <div v-show="!answering" class="composer-submit">
              <v-tooltip v-if="canAttachImage" :title="t('addImagesHint')"><span class="sender-attach">
                <v-button class="icon-button" variant="text" :aria-label="t('addImage')" :disabled="imageInputDisabled" @click="imageInput?.click()"><IconPhoto /><span class="sender-attach-label">{{ t('addImage') }}</span></v-button>
                <input ref="imageInput" type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple tabindex="-1" aria-hidden="true" :aria-label="t('addImage')" :disabled="imageInputDisabled" @change="chooseMessageImages">
              </span></v-tooltip>
              <v-button variant="primary" :loading="busy" class="composer-send" :class="{ 'execution-stop-button': agentWorking && !draft.trim() && !attachedImages.length }" :disabled="answering || busy || senderDisabled || (!!attachedImages.length && !canAttachImage) || (!agentWorking && !draft.trim() && !attachedImages.length)" :aria-label="busy ? sendButtonText : sendButtonLabel" :title="agentWorking && !draft.trim() && !attachedImages.length ? `${t('cancelExecution')} · Esc` : defaultSendHint" :aria-keyshortcuts="agentWorking && !draft.trim() && !attachedImages.length ? 'Escape' : undefined" @click="agentWorking && !draft.trim() && !attachedImages.length ? cancelExecution() : sendMessage()"><template v-if="!busy"><span v-if="agentWorking && !draft.trim() && !attachedImages.length" class="composer-stop-mark" aria-hidden="true" /><IconArrowUp v-else aria-hidden="true" /></template><span class="composer-send-label">{{ sendButtonText }}</span></v-button>
            </div></div></div></template>
        </Sender>
</template>

<style scoped>
.sender-setup{display:flex;align-items:flex-start;gap:20px;min-height:136px;padding:22px 24px;color:var(--muted);font-size:16px;line-height:1.7;text-decoration:none;cursor:pointer;transition:border-color .2s,box-shadow .2s,color .2s}
.sender-setup>span:first-child{flex:1;min-width:0}
.sender-setup>.tabler-icon{flex:none;margin-top:6px;color:var(--accent)}
.sender-setup:hover{border-color:var(--accent)!important;color:var(--text)}
.sender-setup:focus-visible{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:3px}
@media(max-width:830px){.sender-setup{min-height:120px;padding:18px 20px}}
@media(max-height:520px){.sender-setup{min-height:88px;padding:16px 18px}}
@media(prefers-reduced-motion:reduce){.sender-setup{transition:none}}
.sender-quote{display:flex;align-items:flex-start;gap:10px;margin:12px 12px 0;padding:10px 10px 10px 12px;min-width:0;border-inline-start:3px solid var(--accent);border-radius:4px 9px 9px 4px;background:color-mix(in srgb,var(--accent) 7%,var(--surface));color:var(--text)}
.sender-quote-mark{flex:none;height:26px;color:var(--accent);font-family:Georgia,serif;font-size:38px;line-height:1}
.sender-quote-body{min-width:0;flex:1}
.sender-quote-label{display:block;margin-bottom:3px;color:var(--muted);font-size:12px}
.sender-quote-body p{max-height:5.4em;margin:0;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px;line-height:1.6}
.sender-drop-hint{position:absolute;inset:0;z-index:5;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:.3rem;padding:.6rem;pointer-events:none;border:2px dashed var(--accent);border-radius:inherit;background:var(--surface);text-align:center}
.sender-drop-hint>.tabler-icon{font-size:24px}
.sender-drop-hint strong{font-size:16px;font-weight:500}
.sender-drop-hint small{font-size:13px;overflow-wrap:anywhere}
.sender-drop-hint.unsupported{border-color:var(--faint);color:var(--muted)}
.sender-card:not(.ui-sender-disabled):hover{border-color:var(--primary-hover)}
.sender-card:not(.ui-sender-disabled):focus-within{border-color:var(--accent);box-shadow:0 0 0 2px color-mix(in srgb,var(--accent) 18%,transparent)}
.sender-card .composer-send.ui-btn:focus-visible{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:2px}
.sender-actions{display:flex;align-items:center;gap:8px;min-width:0;max-width:100%;margin-inline-start:auto}
.sender-card .composer-submit{width:auto;margin-left:0}
.sender-card-auto .sender-footer{flex-wrap:nowrap}.sender-card-auto .sender-options{flex:1 1 auto;flex-wrap:nowrap}.sender-card-auto .composer-submit{width:auto;flex:0 1 auto;flex-wrap:nowrap}
@media(max-height:520px){
  .sender-quote{gap:7px;margin:6px 8px 0;padding:6px 8px}
  .sender-quote-body p{max-height:3.2em}
  .sender-card :deep(.ui-sender-content){padding:4px 8px}
  .sender-card :deep(.ui-sender-footer){padding:0 8px 6px}
  .sender-card .sender-footer{flex-wrap:wrap;gap:.35rem;padding:0}
  .sender-actions{gap:.3rem}
  .sender-card .sender-options{flex:1 1 auto;flex-wrap:nowrap;gap:.3rem;min-width:0}
  .sender-card .sender-options .composer-tier{flex:0 1 7rem;width:7rem}
  .sender-card .composer-submit{flex:0 1 auto;flex-wrap:nowrap;width:auto;gap:.3rem;min-width:0}
}

.sender-question-notice{display:flex;align-items:center;justify-content:space-between;gap:.5rem;padding:8px 12px;border-bottom:1px solid var(--border-subtle);color:var(--muted);font-size:14px;line-height:1.5}
.sender-question-notice>span{flex:1;min-width:0;overflow-wrap:anywhere}.sender-question-notice>.ui-btn{flex:none}
.sender-card-answering{display:flex;flex-direction:column;flex:0 1 auto;min-height:0;max-height:min(60dvh,34rem)}
.sender-card-answering :deep(.ui-sender-header),.sender-card-answering :deep(.ui-sender-footer){flex:none}
.sender-card-answering :deep(.ui-sender-alternate){display:flex;flex:1 1 auto;min-height:0;overflow:hidden;padding:12px 16px 8px}
.sender-question-pane{display:flex;flex:1 1 auto;min-width:0;min-height:0;outline:none}
.sender-card-answering .sender-options{flex:1 1 auto;flex-wrap:nowrap;min-width:0;overflow:hidden}
.sender-card-answering .sender-footer{flex-wrap:nowrap}.sender-card-answering .sender-actions{flex:none}
@media(max-width:520px){.sender-card-answering :deep(.ui-sender-alternate){padding:8px 10px}.sender-question-notice{padding:6px 8px}}
@media(max-height:520px){.sender-card-answering{max-height:calc(100dvh - 7rem)}.sender-card-answering :deep(.ui-sender-alternate){padding:4px 8px}.sender-question-notice{padding:4px 8px;font-size:13px}}
</style>
