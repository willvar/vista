<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as AButton } from 'antdv-next'
import { CloseOutlined } from '@antdv-next/icons'
import { isPlainEscape } from './composables/useEscape'
import { messageImageZoom } from './ui/messageImageZoom'

const props = defineProps<{ sessionId: string; messageId: string; count: number }>()
const { t } = useI18n({ useScope: 'global' })
const failed = ref<Record<number, boolean>>({})
const retries = ref<Record<number, number>>({})
const sources = computed(() => Array.from({ length: props.count }, (_, index) =>
  `/api/sessions/${encodeURIComponent(props.sessionId)}/messages/${encodeURIComponent(props.messageId)}/images/${index}${retries.value[index] ? `?retry=${retries.value[index]}` : ''}`))
const previewOpen = ref(false)
const closeControl = ref<InstanceType<typeof AButton> | null>(null)
let image: HTMLImageElement | undefined
let trigger: HTMLButtonElement | undefined
let opening: Promise<unknown> | undefined
let closing: Promise<void> | undefined
let disposed = false

function closed() {
  previewOpen.value = false
  document.removeEventListener('keydown', previewKeydown)
  document.removeEventListener('click', previewClick, true)
  const closedImage = image
  closedImage?.removeEventListener('medium-zoom:closed', closed)
  image = undefined
  // medium-zoom emits closed before clearing its active image. Detach in the
  // next microtask so detach() cannot start another close during that event.
  if (closedImage) queueMicrotask(() => messageImageZoom().detach(closedImage))
  if (!disposed && trigger?.isConnected) trigger.focus({ preventScroll: true })
  trigger = undefined
}
function closePreview() {
  if (!previewOpen.value) return Promise.resolve()
  // The library ignores close() while its opening animation is in flight.
  return closing ||= (async () => {
    await opening
    await messageImageZoom().close()
    closing = undefined
  })()
}
function previewKeydown(event: KeyboardEvent) {
  if (isPlainEscape(event)) {
    event.preventDefault()
    event.stopPropagation()
    void closePreview()
  } else if (event.key === 'Tab') {
    event.preventDefault()
    closeControl.value?.$el?.focus({ preventScroll: true })
  }
}
function previewClick(event: MouseEvent) {
  if (!(event.target instanceof Element) || !event.target.closest('.medium-zoom-overlay, .medium-zoom-image--opened')) return
  // The library drops clicks during its opening animation. Keep that dismissal
  // intent and close as soon as opening finishes, just like Escape and Close.
  event.preventDefault()
  event.stopPropagation()
  void closePreview()
}
async function openPreview(event: MouseEvent) {
  const button = event.currentTarget as HTMLButtonElement
  const target = button.querySelector('img')
  if (!target?.complete || !target.naturalWidth || previewOpen.value) return
  const zoom = messageImageZoom()
  if (zoom.getZoomedImage()) return
  trigger = button
  image = target
  image.addEventListener('medium-zoom:closed', closed)
  previewOpen.value = true
  document.addEventListener('keydown', previewKeydown)
  document.addEventListener('click', previewClick, true)
  opening = zoom.attach(target).open({ target })
  await nextTick()
  closeControl.value?.$el?.focus({ preventScroll: true })
}
function retry(index: number) {
  retries.value[index] = (retries.value[index] || 0) + 1
  failed.value[index] = false
}
watch(() => `${props.sessionId}/${props.messageId}`, () => {
  void closePreview()
  failed.value = {}
  retries.value = {}
})
onBeforeUnmount(() => {
  disposed = true
  void closePreview()
})
</script>

<template>
  <div class="message-images">
    <template v-for="(src, index) in sources" :key="src">
      <div v-if="failed[index]" class="message-image-error">
        <span>{{ t('imageLoadFailed') }}</span><a-button size="small" @click="retry(index)">{{ t('retry') }}</a-button>
      </div>
      <button v-else type="button" class="message-image-thumbnail" :aria-label="t('zoomImage', { index: index + 1 })" @click.stop="openPreview">
        <img :src="src" :alt="t('attachedImage')" loading="lazy" decoding="async" @error="failed[index] = true">
      </button>
    </template>
  </div>
  <Teleport to="body">
    <div v-if="previewOpen" class="message-image-preview-controls" role="dialog" aria-modal="true" :aria-label="t('attachedImage')">
      <a-button ref="closeControl" class="message-image-preview-close" type="text" shape="circle" :aria-label="t('close')" @click="closePreview"><template #icon><CloseOutlined /></template></a-button>
    </div>
  </Teleport>
</template>

<style scoped>
.message-images{display:flex;flex-wrap:wrap;align-items:flex-start;gap:.5rem;max-width:100%;margin-top:.55rem}
.message-image-thumbnail{display:block;flex:0 1 auto;max-width:min(240px,100%);margin:0;padding:0;border:0;border-radius:8px;background:var(--surface);cursor:zoom-in;line-height:0;overflow:hidden}
.message-image-thumbnail:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
.message-image-thumbnail img{display:block;max-width:100%;max-height:240px;width:auto;height:auto;object-fit:contain;border-radius:8px}
.message-image-error{display:flex;align-items:center;flex-wrap:wrap;gap:.5rem;max-width:100%;padding:.7rem;border:1px solid var(--border-strong);border-radius:8px;color:var(--text-secondary);font-size:14px}
.message-image-preview-controls{position:fixed;inset:0;z-index:1102;pointer-events:none}
.message-image-preview-close.ant-btn{position:absolute;top:max(12px,env(safe-area-inset-top));right:max(12px,env(safe-area-inset-right));width:44px;height:44px;background:var(--surface);color:var(--text);pointer-events:auto}
:global(.medium-zoom-overlay){z-index:1100}
:global(.medium-zoom-image--opened){z-index:1101}
/* medium-zoom's own transition is !important and waits for transitionend. */
@media(prefers-reduced-motion:reduce){:global(body .medium-zoom-overlay),:global(body .medium-zoom-image){transition-duration:1ms!important}}
</style>
