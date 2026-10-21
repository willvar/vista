<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, TypographyText } from 'antdv-next'
import { LikeOutlined, LikeFilled, DislikeOutlined, DislikeFilled, EditOutlined, UndoOutlined } from '@antdv-next/icons'
import Actions from '@antdv-next/x/dist/actions/Actions'
import ActionsCopy from '@antdv-next/x/dist/actions/ActionsCopy'
const props = defineProps<{ text: string; fullText?: () => Promise<string>; allowFeedback?: boolean; feedback?: 'up' | 'down'; busy?: boolean; allowMutation?: boolean; mutationDisabled?: boolean }>()
const emit = defineEmits<{ feedback: [value: 'up' | 'down']; edit: []; withdraw: [] }>()
const { t } = useI18n()
const items = computed(() => [{ key: 'copy', label: t('copyReply') }, ...(props.allowMutation ? [{ key: 'edit', label: t('editSentMessage') }, { key: 'withdraw', label: t('withdrawMessage') }] : []), ...(props.allowFeedback ? [{ key: 'up', label: t('helpfulReply') }, { key: 'down', label: t('unhelpfulReply') }] : [])])
</script>
<template>
  <Actions class="message-actions" :items="items">
    <template #actionRender="{ item }">
      <template v-if="item.key === 'copy'"><TypographyText v-if="fullText" class="reply-action reply-copy" :copyable="{ text: fullText, tooltips: [t('copyReply'), t('copiedReply')] }" /><ActionsCopy v-else class="reply-action reply-copy" :text="props.text" :aria-label="t('copyReply')" /></template>
      <Button v-else-if="item.key === 'edit' || item.key === 'withdraw'" type="text" size="small" class="reply-action" :aria-label="item.label" :title="item.label" :disabled="busy || mutationDisabled" @click="item.key === 'edit' ? emit('edit') : emit('withdraw')"><EditOutlined v-if="item.key === 'edit'" /><UndoOutlined v-else /></Button>
      <Button v-else type="text" size="small" class="reply-action reply-feedback" :class="{ selected: feedback === item.key }" :aria-label="item.label" :title="item.label" :aria-pressed="feedback === item.key" :disabled="busy" @click="emit('feedback', item.key as 'up' | 'down')"><component :is="item.key === 'up' ? feedback === 'up' ? LikeFilled : LikeOutlined : feedback === 'down' ? DislikeFilled : DislikeOutlined" /></Button>
    </template>
  </Actions>
</template>
