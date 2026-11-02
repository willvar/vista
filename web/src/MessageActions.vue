<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Popover, TypographyText } from 'antdv-next'
import { CommentOutlined, LikeOutlined, LikeFilled, DislikeOutlined, DislikeFilled, EditOutlined, UndoOutlined } from '@antdv-next/icons'
import Actions from '@antdv-next/x/dist/actions/Actions'
import ActionsCopy from '@antdv-next/x/dist/actions/ActionsCopy'
import { useEscapeDismiss } from './composables/useEscape'
const props = defineProps<{ text: string; fullText?: () => Promise<string>; allowFeedback?: boolean; compactFeedback?: boolean; feedback?: 'up' | 'down'; busy?: boolean; allowMutation?: boolean; mutationDisabled?: boolean }>()
const emit = defineEmits<{ feedback: [value: 'up' | 'down']; edit: []; withdraw: [] }>()
const { t } = useI18n()
const items = computed(() => [{ key: 'copy', label: t('copyReply') }, ...(props.allowMutation ? [{ key: 'edit', label: t('editSentMessage') }, { key: 'withdraw', label: t('withdrawMessage') }] : []), ...(props.allowFeedback ? props.compactFeedback ? [{ key: 'feedback', label: t('rateReply') }] : [{ key: 'up', label: t('helpfulReply') }, { key: 'down', label: t('unhelpfulReply') }] : [])])
const feedbackOpen = ref(false)
const feedbackChoices = ref<HTMLElement | null>(null)
const feedbackTrigger = ref<HTMLButtonElement | null>(null)
useEscapeDismiss(feedbackOpen)
watch(() => props.compactFeedback, () => { feedbackOpen.value = false })
watch(feedbackOpen, async open => {
  if (!open) {
    if (feedbackChoices.value?.contains(document.activeElement)) feedbackTrigger.value?.focus({ preventScroll: true })
    return
  }
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
  <Actions class="message-actions" :items="items">
    <template #actionRender="{ item }">
      <template v-if="item.key === 'copy'"><TypographyText v-if="fullText" class="reply-action reply-copy" :copyable="{ text: fullText, tooltips: [t('copyReply'), t('copiedReply')] }" /><ActionsCopy v-else class="reply-action reply-copy" :text="props.text" :aria-label="t('copyReply')" /></template>
      <button v-else-if="item.key === 'edit' || item.key === 'withdraw'" type="button" class="reply-action" :aria-label="item.label" :title="item.label" :disabled="busy || mutationDisabled" @click="item.key === 'edit' ? emit('edit') : emit('withdraw')"><EditOutlined v-if="item.key === 'edit'" /><UndoOutlined v-else /></button>
      <button v-else-if="item.key === 'up' || item.key === 'down'" type="button" class="reply-action reply-feedback" :class="{ selected: feedback === item.key }" :aria-label="item.label" :title="item.label" :aria-pressed="feedback === item.key" :disabled="busy" @click="rate(item.key)"><component :is="item.key === 'up' ? feedback === 'up' ? LikeFilled : LikeOutlined : feedback === 'down' ? DislikeFilled : DislikeOutlined" /></button>
      <Popover v-else v-model:open="feedbackOpen" placement="top" trigger="click">
        <template #content>
          <div ref="feedbackChoices" class="message-feedback-choices" role="group" :aria-label="t('rateReply')">
            <button v-for="value in (['up', 'down'] as const)" :key="value" type="button" class="reply-action reply-feedback" :class="{ selected: feedback === value }" :aria-label="t(value === 'up' ? 'helpfulReply' : 'unhelpfulReply')" :title="t(value === 'up' ? 'helpfulReply' : 'unhelpfulReply')" :aria-pressed="feedback === value" :disabled="busy" @click="rate(value)"><component :is="value === 'up' ? feedback === 'up' ? LikeFilled : LikeOutlined : feedback === 'down' ? DislikeFilled : DislikeOutlined" /></button>
          </div>
        </template>
        <button ref="feedbackTrigger" type="button" class="reply-action reply-feedback-trigger" :class="{ selected: !!feedback }" :aria-label="item.label" :title="feedback ? t(feedback === 'up' ? 'helpfulReply' : 'unhelpfulReply') : item.label" :aria-expanded="feedbackOpen" :disabled="busy"><component :is="feedback === 'up' ? LikeFilled : feedback === 'down' ? DislikeFilled : CommentOutlined" /></button>
      </Popover>
    </template>
  </Actions>
</template>
