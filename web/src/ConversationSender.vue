<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PictureOutlined, MoreOutlined, ArrowRightOutlined } from '@antdv-next/icons'
import { Button as AButton, Select as ASelect, Tooltip as ATooltip, Dropdown as ADropdown } from 'antdv-next'
import Sender from '@antdv-next/x/dist/sender/Sender'
import type { SenderRef } from '@antdv-next/x/dist/sender/interface'
import ContextStatus from './ContextStatus.vue'
import ContextUsage from './ContextUsage.vue'
import ReasoningEffortPicker from './ReasoningEffortPicker.vue'
import { jobActive } from './state/jobs'
import { useWorkspace } from './composables/useWorkspace'

const { t } = useI18n({ useScope: 'global' })
const emit = defineEmits<{ send: []; openProcesses: [] }>()
const { selected, busy, needsProvider, workerAvailable, modelChoices, interactionPreferences, conversation, route, mobileSessionsOpen, modalQuery } = useWorkspace()
const { draft, quotedText, removeQuote, agentWorking, agentStatus, selectionMode, selectionTier, chooseSelectionTier,
  editingMessage, cancelEditMessage,
  selectedModel, selectedReasoning, reasoningChoices, chooseModel,
  canAttachImage, attachedImages, attachedPreviewURLs, chooseImages, pasteImages, dropImages, removeImage,
  submitMessage, submitAlternateMessage, cancelExecution, recallLastUserMessage,
  jobs, compaction, contextUsage, canCompact, compactSession, manualCompacting, compactionRequestPending, continueAfterCompaction, fillContinueDraft, feedbackInvitationOpen } = conversation
const runningJobs = computed(() => jobs.value.filter(jobActive).length)
const compacting = computed(() => manualCompacting.value || compactionRequestPending.value
  || agentWorking.value && compaction.value?.phase === 'compacting')
const senderDisabled = computed(() => manualCompacting.value || compactionRequestPending.value || !interactionPreferences.ready.value || needsProvider.value
  || !!selected.value && (!selected.value.open || !workerAvailable(selected.value.id)))
const sender = ref<SenderRef | null>(null)
const activeInputKey = computed(() => {
  if (senderDisabled.value || mobileSessionsOpen.value || feedbackInvitationOpen.value) return undefined
  if (route.name === 'home') return 'home'
  if (route.name === 'session' && route.params.id === selected.value?.id) return selected.value?.id
  return undefined
})
watch(activeInputKey, async id => {
  if (!id) return
  await nextTick()
  if (activeInputKey.value === id) sender.value?.focus({ cursor: 'end', preventScroll: true })
}, { immediate: true, flush: 'post' })
watch(quotedText, async quote => {
  if (!activeInputKey.value) return
  const inputKey = activeInputKey.value
  await nextTick()
  if (activeInputKey.value === inputKey && quotedText.value === quote) sender.value?.focus({ cursor: 'end', preventScroll: true })
}, { flush: 'post' })
watch(editingMessage, async message => {
  if (!message || !activeInputKey.value) return
  const id = activeInputKey.value
  await nextTick()
  if (activeInputKey.value === id) sender.value?.focus({ cursor: 'end', preventScroll: true })
}, { flush: 'post' })
const continueCompletion = computed(() => continueAfterCompaction.value && !senderDisabled.value)
const senderPlaceholder = computed(() => t(quotedText.value ? 'askAboutQuote' : continueCompletion.value ? 'continuePlaceholder' : 'sendMessage'))
const imageInput = ref<HTMLInputElement | null>(null)
const imageInputDisabled = computed(() => busy.value || senderDisabled.value || !canAttachImage.value)
const imageDragDepth = ref(0)
const imageDropHint = computed(() => t(canAttachImage.value ? 'dropImagesHere' : 'imageUnsupported'))
function hasDraggedFiles(event: DragEvent) { return Array.from(event.dataTransfer?.types || []).includes('Files') }
function resetImageDrag() { imageDragDepth.value = 0 }
function enterImageDrag(event: DragEvent) {
  if (!hasDraggedFiles(event)) return
  event.preventDefault()
  if (!busy.value && !senderDisabled.value) imageDragDepth.value++
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
  if (!busy.value && !senderDisabled.value) dropImages(event)
}
watch([senderDisabled, busy, canAttachImage, () => selected.value?.id], resetImageDrag)
const shortViewport = window.matchMedia('(max-height: 520px)')
const compactSender = ref(shortViewport.matches)
const senderAutoSize = computed(() => compactSender.value
  ? { minRows: 1, maxRows: draft.value.trim() ? 4 : 1 }
  : { minRows: 2, maxRows: 8 })
const senderClass = computed(() => 'composer sender-card'
  + (selectionMode.value === 'auto' ? ' sender-card-auto' : '')
  + (compacting.value ? ' sender-card-compacting' : ''))
function updateSenderSize() { compactSender.value = shortViewport.matches }
const selectionTierOptions = computed(() => [{ label: t('speedTier'), value: 'speed' }, { label: t('qualityTier'), value: 'quality' }])
const defaultSendHint = computed(() => editingMessage.value ? t('resendEditedMessage') : agentWorking.value ? t('sendAsSteer') : t('send'))
const sendButtonLabel = computed(() => agentWorking.value && !draft.value.trim() && !attachedImages.value.length ? t('cancelExecution') : t('send'))
let composing = false
let compositionEndedAt = -Infinity
function compositionStart() { composing = true }
function compositionEnd() { composing = false; compositionEndedAt = performance.now() }
function notifySend() {
  if (busy.value || (!draft.value.trim() && !attachedImages.value.length) || (!!attachedImages.value.length && !canAttachImage.value)) return
  emit('send')
}
function sendMessage() {
  notifySend()
  submitMessage()
}
function sendAlternateMessage() {
  notifySend()
  submitAlternateMessage()
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
  if ((event.key === 'ArrowUp' || event.key === 'Tab' && continueCompletion.value)
    && !event.defaultPrevented && !event.repeat && !event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
    && !event.isComposing && event.keyCode !== 229 && !composing && performance.now() - compositionEndedAt >= 200
    && !draft.value && !quotedText.value && !attachedImages.value.length && !senderDisabled.value && !busy.value && selected.value
    && event.target instanceof HTMLTextAreaElement && !event.target.value) {
    event.preventDefault()
    event.stopPropagation()
    if (event.key === 'Tab') fillContinueDraft()
    else void recallMessage(event.target)
    return false
  }
  if (event.key !== 'Enter' || event.shiftKey || event.altKey) return
  if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || composing || performance.now() - compositionEndedAt < 200) return false
  event.preventDefault()
  event.stopPropagation()
  // Sender's built-in submit guard only knows about text. Route Enter through
  // Vista's submission guard so an attachment-only draft works the same way.
  if (!event.repeat && !senderDisabled.value) {
    if (event.ctrlKey || event.metaKey) sendAlternateMessage()
    else sendMessage()
  }
  return false
}
onMounted(() => {
  shortViewport.addEventListener('change', updateSenderSize)
  window.addEventListener('dragend', resetImageDrag)
  window.addEventListener('drop', resetImageDrag)
})
onUnmounted(() => {
  shortViewport.removeEventListener('change', updateSenderSize)
  window.removeEventListener('dragend', resetImageDrag)
  window.removeEventListener('drop', resetImageDrag)
})
</script>

<template>
        <router-link v-if="needsProvider" class="composer sender-card sender-setup" :to="{ path: '/settings/models', query: modalQuery() }">
          <span>{{ t('configureModelToStart') }}</span><ArrowRightOutlined aria-hidden="true" />
        </router-link>
        <Sender v-else ref="sender" v-model:value="draft" :class="senderClass" :auto-size="senderAutoSize" :disabled="senderDisabled" :loading="busy || agentWorking && !draft.trim() && !attachedImages.length" :placeholder="senderPlaceholder" :aria-label="t('sendMessage')" :suffix="false" :on-key-down="senderKeyDown" @compositionstart.capture="compositionStart" @compositionend.capture="compositionEnd" @submit="sendMessage" @cancel="cancelExecution" @paste="pasteImages" @dragenter="enterImageDrag" @dragover="overImageDrag" @dragleave="leaveImageDrag" @drop="receiveImageDrop">
          <template #header>
          <div v-if="editingMessage" class="sender-editing" role="status"><span>{{ t('editingSentMessage') }}</span><a-button type="text" size="small" :disabled="busy" @click="cancelEditMessage">{{ t('cancelEditMessage') }}</a-button></div>
          <ContextStatus v-if="selected" :key="selected.id" :status="compaction" :compacting="compacting" :manual="manualCompacting || compactionRequestPending" :cancel-disabled="busy || !manualCompacting" :has-jobs="!!jobs.length" :job-count="runningJobs" @cancel="cancelExecution" @open-processes="emit('openProcesses')" />
          <div v-if="quotedText" class="sender-quote" role="group" :aria-label="t('quotedText')">
            <span class="sender-quote-mark" aria-hidden="true">“</span>
            <div class="sender-quote-body"><span class="sender-quote-label">{{ t(quotedText.role === 'assistant' ? 'quotedReply' : 'quotedMessage') }}</span><p>{{ quotedText.text }}</p></div>
            <a-button type="text" class="sender-quote-remove icon-close" :aria-label="t('removeQuote')" :disabled="busy" @click="removeQuote">×</a-button>
          </div>
          <div v-if="imageDragDepth" class="sender-drop-hint" :class="{ unsupported: !canAttachImage }" role="status"><PictureOutlined /><strong>{{ imageDropHint }}</strong><small v-if="canAttachImage">{{ t('imageLimit') }}</small></div>
          <div v-if="attachedImages.length" class="sender-attachments" aria-live="polite">
            <div v-for="(file, index) in attachedImages" :key="index" class="sender-attachment">
              <img :src="attachedPreviewURLs[index]" :alt="file.name">
              <span>{{ file.name }}<small>{{ Math.ceil(file.size / 1024) }} KiB</small></span>
              <a-button type="text" class="attachment-close icon-close" :aria-label="t('removeAttachment')" :disabled="busy" @click="removeImage(index)">×</a-button>
            </div>
          </div>

</template>
          <template #footer="{ components }"><div class="sender-footer">            <div class="sender-options">
              <label for="selection-tier" class="visually-hidden">{{ t('selectionTier') }}</label><label for="selected-model" class="visually-hidden">{{ t('model') }}</label>
              <a-select variant="borderless" id="selection-tier" v-if="selectionMode === 'auto'" :value="selectionTier" class="composer-tier sender-picker" :options="selectionTierOptions" :aria-label="t('selectionTier')" :title="t('autoSelectionHint')" :disabled="senderDisabled" @update:value="chooseSelectionTier($event === 'quality' ? 'quality' : 'speed')" />
              <a-select variant="borderless" id="selected-model" v-if="selectionMode === 'manual'" :value="selectedModel" class="composer-model sender-picker" :options="modelChoices" show-search option-filter-prop="searchText" option-label-prop="fullLabel" :aria-label="t('model')" :disabled="senderDisabled" @update:value="chooseModel(String($event))" />
              <ReasoningEffortPicker v-if="selectionMode === 'manual' && reasoningChoices.length" v-model:value="selectedReasoning" class="composer-reasoning" :options="reasoningChoices" :disabled="senderDisabled" />
            </div>

            <div class="sender-actions">
            <ContextUsage v-if="selected && contextUsage" :key="selected.id" :usage="contextUsage" />
            <div class="composer-submit">
              <a-dropdown v-if="selected" :trigger="['click']" :menu="{ items: [{ key: 'compact', label: t('compactContext'), disabled: !canCompact || senderDisabled }], onClick: compactSession }">
                <a-button type="text" :aria-label="t('conversationOptions')" :title="t('conversationOptions')"><MoreOutlined /></a-button>
              </a-dropdown>
              <span v-if="!compacting" class="sender-state" role="status" aria-live="polite" :title="agentWorking ? agentStatus : undefined">{{ agentWorking ? agentStatus : '' }}</span>
              <a-button v-if="agentWorking && (draft.trim() || attachedImages.length)" class="alternate-delivery" size="small" type="text" :title="t('alternateQueueHint')" :disabled="busy || senderDisabled || (!!attachedImages.length && !canAttachImage)" @click="sendAlternateMessage">{{ t('queue') }}</a-button>
              <a-tooltip v-if="canAttachImage" :title="t('addImagesHint')"><span class="sender-attach">
                <a-button type="text" :aria-label="t('addImage')" :disabled="imageInputDisabled" @click="imageInput?.click()"><PictureOutlined /><span class="sender-attach-label">{{ t('addImage') }}</span></a-button>
                <input ref="imageInput" type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple tabindex="-1" aria-hidden="true" :aria-label="t('addImage')" :disabled="imageInputDisabled" @change="chooseImages">
              </span></a-tooltip>
              <component :is="busy || agentWorking && !draft.trim() && !attachedImages.length ? components.LoadingButton : components.SendButton" class="composer-send" :class="{ 'execution-stop-button': agentWorking && !draft.trim() && !attachedImages.length }" shape="circle" :disabled="busy || senderDisabled || (!!attachedImages.length && !canAttachImage) || (!agentWorking && !draft.trim() && !attachedImages.length)" :aria-label="sendButtonLabel" :title="agentWorking && !draft.trim() && !attachedImages.length ? `${t('cancelExecution')} · Esc` : defaultSendHint" :aria-keyshortcuts="agentWorking && !draft.trim() && !attachedImages.length ? 'Escape' : undefined" @click="attachedImages.length && !draft.trim() ? sendMessage() : undefined"><span class="visually-hidden">{{ sendButtonLabel }}</span></component>
            </div></div></div></template>
        </Sender>
</template>

<style scoped>
.sender-setup{display:flex;align-items:flex-start;gap:20px;min-height:136px;padding:22px 24px;color:var(--muted);font-size:16px;line-height:1.7;text-decoration:none;cursor:pointer;transition:border-color .2s,box-shadow .2s,color .2s}
.sender-setup>span:first-child{flex:1;min-width:0}
.sender-setup>.anticon{flex:none;margin-top:6px;color:var(--accent)}
.sender-setup:hover{border-color:var(--accent)!important;color:var(--text)}
.sender-setup:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
@media(max-width:830px){.sender-setup{min-height:120px;padding:18px 20px}}
@media(max-height:520px){.sender-setup{min-height:88px;padding:16px 18px}}
@media(prefers-reduced-motion:reduce){.sender-setup{transition:none}}
.sender-quote{display:flex;align-items:flex-start;gap:10px;margin:12px 12px 0;padding:10px 10px 10px 12px;min-width:0;border-inline-start:3px solid var(--accent);border-radius:4px 9px 9px 4px;background:color-mix(in srgb,var(--accent) 7%,var(--surface));color:var(--text)}
.sender-quote-mark{flex:none;height:26px;color:var(--accent);font-family:Georgia,serif;font-size:38px;line-height:1}
.sender-quote-body{min-width:0;flex:1}
.sender-quote-label{display:block;margin-bottom:3px;color:var(--muted);font-size:12px}
.sender-quote-body p{max-height:5.4em;margin:0;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px;line-height:1.6}
.sender-quote .sender-quote-remove{flex:none;width:28px;height:28px;padding:0;color:var(--muted)}
.sender-card-compacting{border-color:var(--border-strong)!important}
.sender-attach{flex:none;width:auto;height:32px;overflow:visible}
.sender-attach .ant-btn{display:inline-flex;align-items:center;justify-content:center;gap:.35rem;width:auto;height:100%;padding-inline:.55rem;font-size:15px}
.sender-attach-label{font-size:14px}
.sender-drop-hint{position:absolute;inset:0;z-index:5;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:.3rem;padding:.6rem;pointer-events:none;border:2px dashed var(--ant-color-primary);border-radius:inherit;background:var(--ant-color-bg-container);text-align:center}
.sender-drop-hint>.anticon{font-size:24px}
.sender-drop-hint strong{font-size:16px;font-weight:500}
.sender-drop-hint small{font-size:13px;overflow-wrap:anywhere}
.sender-drop-hint.unsupported{border-color:var(--ant-color-text-disabled);color:var(--ant-color-text-secondary)}
.sender-card:not(.antd-sender-disabled):hover{border-color:var(--ant-color-primary-hover,var(--accent))}
.sender-card:not(.antd-sender-disabled):focus-within{border-color:var(--ant-color-primary,var(--accent));box-shadow:0 0 0 2px color-mix(in srgb,var(--ant-color-primary,var(--accent)) 18%,transparent)}
.sender-card .composer-send.ant-btn:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.sender-actions{display:flex;align-items:center;gap:8px;min-width:0;max-width:100%;margin-inline-start:auto}
.sender-card .composer-submit{width:auto;margin-left:0}
.sender-card-auto .sender-footer{flex-wrap:nowrap}.sender-card-auto .sender-options{flex:1 1 auto;flex-wrap:nowrap}.sender-card-auto .composer-submit{width:auto;flex:0 1 auto;flex-wrap:nowrap}.sender-card-auto .sender-state{max-width:clamp(4rem,23vw,12rem);min-width:0}
@media(max-height:520px){
  .sender-quote{gap:7px;margin:6px 8px 0;padding:6px 8px}
  .sender-quote-body p{max-height:3.2em}
  .sender-card :deep(.antd-sender-content){padding:4px 8px}
  .sender-card :deep(.antd-sender-footer){padding:0 8px 6px}
  .sender-card .sender-footer{flex-wrap:nowrap;gap:.35rem;padding:0}
  .sender-actions{gap:.3rem}
  .sender-card .sender-options{flex:1 1 0;flex-wrap:nowrap;gap:.3rem;min-width:0}
  .sender-card .sender-options .composer-model{width:auto;min-width:0;flex:1 1 9rem}
  .sender-card .sender-options .composer-tier{flex:0 1 7rem;width:7rem}
  .sender-card .composer-submit{flex:0 1 auto;flex-wrap:nowrap;width:auto;gap:.3rem;min-width:0}
  .sender-card .sender-state{max-width:clamp(3rem,20vw,9rem);font-size:calc(.67rem + 2px);min-width:0}
  .sender-card .sender-attach{height:28px;flex:none}
  .sender-card .sender-attach .ant-btn{padding-inline:.4rem}
  .sender-card .sender-attach-label{font-size:13px}
  .sender-card .alternate-delivery{padding-inline:4px}
}
</style>
