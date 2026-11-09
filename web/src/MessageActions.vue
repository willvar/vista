<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Popover, Tooltip } from './ui'
import { IconMessage, IconThumbUp, IconThumbUpFilled, IconThumbDown, IconThumbDownFilled, IconPencil, IconArrowBackUp } from '@tabler/icons-vue'
import CopyAction from './ui/CopyAction.vue'
import { useEscapeDismiss } from './composables/useEscape'
const props = defineProps<{ text: string; fullText?: () => Promise<string>; allowFeedback?: boolean; compactFeedback?: boolean; feedback?: 'up' | 'down'; busy?: boolean; allowMutation?: boolean; mutationDisabled?: boolean }>()
const emit = defineEmits<{ feedback: [value: 'up' | 'down']; edit: []; withdraw: [] }>()
const { t } = useI18n()
const items = computed(() => [{ key: 'copy', label: t('copyReply') }, ...(props.allowMutation ? [{ key: 'edit', label: t('editSentMessage') }, { key: 'withdraw', label: t('withdrawMessage') }] : []), ...(props.allowFeedback ? props.compactFeedback ? [{ key: 'feedback', label: t('rateReply') }] : [{ key: 'up', label: t('helpfulReply') }, { key: 'down', label: t('unhelpfulReply') }] : [])])
const feedbackOpen = ref(false)
const feedbackChoices = ref<HTMLElement | null>(null)
function setFeedbackChoices(element: unknown) { feedbackChoices.value = element instanceof HTMLElement ? element : null }
useEscapeDismiss(feedbackOpen)
watch(() => props.compactFeedback, () => { feedbackOpen.value = false })
watch(feedbackOpen, async open => {
  if (!open) return
  await nextTick()
  if (feedbackOpen.value) {
    const choices = feedbackChoices.value
    const selected = choices?.querySelector<HTMLButtonElement>('[aria-pressed="true"]') || choices?.querySelector('button')
    selected?.focus({ preventScroll: true })
  }
}, { flush: 'post' })
function rate(value: 'up' | 'down') {
  if (props.busy) return
  emit('feedback', value)
  feedbackOpen.value = false
}
</script>
<template>
  <div class="message-actions">
    <template v-for="item in items" :key="item.key">
      <CopyAction v-if="item.key === 'copy'" :text="props.text" :full-text="fullText" />
      <Tooltip v-else-if="item.key === 'edit' || item.key === 'withdraw'" :title="item.label">
        <button type="button" class="reply-action" :aria-label="item.label" :disabled="busy || mutationDisabled" @click="item.key === 'edit' ? emit('edit') : emit('withdraw')"><IconPencil v-if="item.key === 'edit'" /><IconArrowBackUp v-else /></button>
      </Tooltip>
      <button v-else-if="item.key === 'up' || item.key === 'down'" type="button" class="reply-action reply-feedback" :class="{ selected: feedback === item.key }" :aria-label="item.label" :title="item.label" :aria-pressed="feedback === item.key" :disabled="busy" @click="rate(item.key)"><component :is="item.key === 'up' ? feedback === 'up' ? IconThumbUpFilled : IconThumbUp : feedback === 'down' ? IconThumbDownFilled : IconThumbDown" /></button>
      <Popover v-else v-model:open="feedbackOpen" placement="top" trigger="click">
        <template #content>
          <div :ref="setFeedbackChoices" class="message-feedback-choices" role="group" :aria-label="t('rateReply')">
            <button v-for="value in (['up', 'down'] as const)" :key="value" type="button" class="reply-action reply-feedback" :class="{ selected: feedback === value }" :aria-label="t(value === 'up' ? 'helpfulReply' : 'unhelpfulReply')" :title="t(value === 'up' ? 'helpfulReply' : 'unhelpfulReply')" :aria-pressed="feedback === value" :disabled="busy" @click="rate(value)"><component :is="value === 'up' ? feedback === 'up' ? IconThumbUpFilled : IconThumbUp : feedback === 'down' ? IconThumbDownFilled : IconThumbDown" /></button>
          </div>
        </template>
        <button type="button" class="reply-action reply-feedback-trigger" :class="{ selected: !!feedback }" :aria-label="item.label" :title="feedback ? t(feedback === 'up' ? 'helpfulReply' : 'unhelpfulReply') : item.label" :aria-expanded="feedbackOpen" :disabled="busy"><component :is="feedback === 'up' ? IconThumbUpFilled : feedback === 'down' ? IconThumbDownFilled : IconMessage" /></button>
      </Popover>
    </template>
  </div>
</template>
