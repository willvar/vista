<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconMessage2, IconArrowUp } from '@tabler/icons-vue'
import { Button as VButton, Popover } from './ui'
import MessageMarkdown from './MessageMarkdown.vue'
import MessageImages from './MessageImages.vue'
import MessageActions from './MessageActions.vue'
import { sentMessageBody } from './state/quotedReply'
import type { ConversationItem } from './state/mainOutputs'
import { useEscapeDismiss } from './composables/useEscape'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ sessionId: string; message: ConversationItem }>()
const emit = defineEmits<{ locate: [message: ConversationItem] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const { localTime } = useWorkspace()
const open = ref(false)
useEscapeDismiss(open)
watch([() => props.sessionId, () => props.message.id, () => props.message.content], () => { open.value = false })
const content = computed(() => props.message.content)
const imageLabel = computed(() => props.message.imageCount
  ? `${t('attachedImage')} × ${new Intl.NumberFormat(locale.value).format(props.message.imageCount)}` : '')
const preview = computed(() => {
  // Shorten only the reference label. The original uses unchanged message text.
  const text = sentMessageBody(content.value).replace(/\s+/g, ' ').trim()
  if (!text) return imageLabel.value || t('quotedText')
  const characters = Array.from(text)
  return characters.length > 160 ? characters.slice(0, 160).join('') + '…' : text
})
function locate() { open.value = false; emit('locate', props.message) }
</script>

<template>
  <div class="conversation-recent-message" role="region" :aria-label="t('recentSent')" :data-message-id="message.id">
    <Popover v-model:open="open" trigger="click" placement="bottomRight" root-class="recent-sent-popover" :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
      <template #content>
        <section class="recent-sent-detail" :aria-label="t('recentSent')">
          <div class="recent-sent-detail-heading"><strong>{{ t('recentSent') }}</strong><time v-if="message.createdAt" :datetime="message.createdAt">{{ localTime(message.createdAt) }}</time></div>
          <div class="recent-sent-original" tabindex="0">
            <MessageMarkdown v-if="content" :content="content" role="user" />
            <p v-if="imageLabel" class="recent-sent-images">{{ imageLabel }}</p>
            <MessageImages v-if="message.imageCount" :session-id="sessionId" :message-id="message.id" :count="message.imageCount" inline />
          </div>
          <div class="recent-sent-detail-actions"><MessageActions v-if="content" :text="content" /><v-button variant="text" @click="locate">{{ t('recentSentLocate') }}</v-button></div>
        </section>
      </template>
      <button type="button" class="recent-sent-trigger" :aria-label="t('recentSentOriginal')" :aria-description="preview" :aria-expanded="open" :title="t('recentSentOriginal')">
        <IconMessage2 aria-hidden="true" /><span class="recent-sent-label">{{ t('recentSent') }}</span><span class="recent-sent-preview">{{ preview }}</span><span class="recent-sent-view">{{ t('recentSentOriginal') }}</span>
      </button>
    </Popover>
    <v-button variant="text" class="icon-button recent-sent-locate" :aria-label="t('recentSentLocate')" :title="t('recentSentLocate')" @click="locate"><IconArrowUp aria-hidden="true" /></v-button>
  </div>
</template>

<style scoped>
.conversation-recent-message{display:flex;align-items:center;gap:4px;flex:0 0 100%;width:100%;min-width:0;color:var(--muted)}
.recent-sent-trigger{display:flex;align-items:center;gap:8px;flex:1;min-width:0;padding:5px 9px;border:1px solid var(--border-subtle);border-radius:8px;background:var(--surface-subtle);color:inherit;font:inherit;font-size:12px;line-height:1.5;text-align:start;cursor:pointer}
.recent-sent-trigger>svg{flex:none;width:16px;height:16px}
.recent-sent-trigger:hover{color:var(--text-secondary);border-color:var(--border-strong)}
.recent-sent-trigger:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:2px}
.recent-sent-label,.recent-sent-view{flex:none;white-space:nowrap}
.recent-sent-label{font-weight:500}
.recent-sent-preview{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.recent-sent-view{color:var(--accent)}
.recent-sent-detail{width:min(40rem,calc(100vw - 3.5rem));min-width:0;font-size:14px;line-height:1.6}
.recent-sent-detail-heading{display:flex;align-items:baseline;flex-wrap:wrap;gap:4px 12px;margin-bottom:8px}
.recent-sent-detail-heading time{color:var(--muted);font-size:12px}
.recent-sent-original{max-height:min(55dvh,28rem);overflow:auto;overscroll-behavior:contain;overflow-wrap:anywhere;padding:2px}
.recent-sent-original:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:-2px}
.recent-sent-original :deep(.markdown){white-space:normal;overflow-wrap:anywhere;font-size:14px;line-height:1.65}
.recent-sent-images{margin:8px 0 0;color:var(--muted);font-size:12px}
.recent-sent-detail-actions{display:flex;justify-content:space-between;align-items:center;gap:8px;margin-top:8px}
@media(max-width:640px){.recent-sent-view{display:none}}
@media(max-height:520px){
  .conversation-recent-message{flex:0 0 auto;width:auto}
  .recent-sent-trigger{justify-content:center;flex:none;width:32px;min-width:32px;height:32px;padding:0;background:transparent;border-color:transparent}
  .recent-sent-label,.recent-sent-preview,.recent-sent-view,.recent-sent-locate{display:none}
}
</style>
<style>
.recent-sent-popover .ui-popover-inner{max-height:calc(100dvh - 2rem);overflow:auto;overscroll-behavior:contain}
</style>
