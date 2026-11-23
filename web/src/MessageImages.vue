<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import { messageImageKey, messageImagePath, useMessageImageRoute } from './composables/useMessageImageRoute'

const props = defineProps<{ sessionId: string; messageId: string; count: number; inline?: boolean }>()
const { t } = useI18n({ useScope: 'global' })
const { client, route } = useWorkspace()
const preview = useMessageImageRoute()
const failed = ref<Record<number, boolean>>({})
const retries = ref<Record<number, number>>({})
const sources = computed(() => Array.from({ length: props.count }, (_, index) =>
  client.url(`${messageImagePath(props.sessionId, props.messageId, index)}${retries.value[index] ? `?retry=${retries.value[index]}` : ''}`)))
function openPreview(index: number, event: MouseEvent) {
  const image = (event.currentTarget as HTMLButtonElement).querySelector('img')
  if (route.params.id === props.sessionId && image?.complete && image.naturalWidth) {
    void preview.open(messageImageKey(props.messageId, index))
  }
}
function retry(index: number) {
  retries.value[index] = (retries.value[index] || 0) + 1
  failed.value[index] = false
}
watch(() => `${props.sessionId}/${props.messageId}`, () => {
  failed.value = {}
  retries.value = {}
})
</script>

<template>
  <div class="message-images">
    <template v-for="(src, index) in sources" :key="src">
      <div v-if="failed[index]" class="message-image-error">
        <span>{{ t('imageLoadFailed') }}</span><v-button @click="retry(index)">{{ t('retry') }}</v-button>
      </div>
      <button v-else-if="!inline" type="button" class="message-image-thumbnail" :aria-label="t('zoomImage', { index: index + 1 })" :data-image-preview="messageImageKey(messageId, index)" @click.stop="openPreview(index, $event)">
        <img :src="src" :alt="t('attachedImage')" loading="lazy" decoding="async" @error="failed[index] = true">
      </button>
      <img v-else class="message-image-inline" :src="src" :alt="t('attachedImage')" loading="lazy" decoding="async" @error="failed[index] = true">
    </template>
  </div>
</template>

<style scoped>
.message-images{display:flex;flex-wrap:wrap;align-items:flex-start;gap:.5rem;max-width:100%;margin-top:.55rem}
.message-image-inline{display:block;max-width:100%;height:auto;object-fit:contain}
.message-image-thumbnail{display:flex;align-items:center;justify-content:center;flex:0 1 240px;width:240px;height:180px;max-width:100%;margin:0;padding:0;border:0;border-radius:8px;background:var(--surface);cursor:zoom-in;line-height:0;overflow:hidden}
.message-image-thumbnail:focus-visible{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:3px}
.message-image-thumbnail img{display:block;width:100%;height:100%;object-fit:contain;border-radius:8px}
.message-image-error{display:flex;align-items:center;justify-content:center;flex-wrap:wrap;gap:.5rem;flex:0 1 240px;width:240px;height:180px;max-width:100%;padding:.7rem;border:1px solid var(--border-strong);border-radius:8px;color:var(--text-secondary);font-size:14px}
</style>
