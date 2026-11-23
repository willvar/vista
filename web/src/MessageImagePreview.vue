<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { IconX } from '@tabler/icons-vue'
import { createProfileClient, routeProfileId } from './client/profileScope'
import { isPlainEscape } from './composables/useEscape'
import { messageImagePath, readMessageImageKey, useMessageImageRoute } from './composables/useMessageImageRoute'
import { Button as VButton } from './ui'
import { messageImageZoom } from './ui/messageImageZoom'

const route = useRoute()
const layer = useMessageImageRoute()
const { t } = useI18n({ useScope: 'global' })
const presenting = ref(false)
const closeControl = ref<InstanceType<typeof VButton> | null>(null)
const visible = computed(() => layer.current.value !== undefined || presenting.value)
const profileId = computed(() => routeProfileId(route.params))
const client = computed(() => createProfileClient(() => document.documentElement.lang, profileId.value))
const selection = computed(() => {
  const key = layer.current.value
  if (key === undefined) return
  const image = readMessageImageKey(key)
  const session = route.params.id
  if (!image || typeof session !== 'string') return null
  return { key, routePath: route.path, url: client.value.url(messageImagePath(session, image.messageId, image.index)) }
})
let viewer: { opened: Promise<unknown>; closed: Promise<void> } | undefined
let transition = Promise.resolve()

async function dismissViewer() {
  const previous = viewer
  if (!previous) return
  await previous.opened
  if (viewer === previous) await messageImageZoom().close()
  // If the library began closing by itself, a second close() resolves before the animation ends.
  await previous.closed
}

function restoreSource(url: string, signal: AbortSignal) {
  const image = new Image()
  image.className = 'message-image-preview-source'
  image.alt = t('attachedImage')
  return new Promise<HTMLImageElement | undefined>(resolve => {
    function finish(ok: boolean) {
      image.onload = image.onerror = null
      signal.removeEventListener('abort', abort)
      if (!ok) { image.remove(); image.removeAttribute('src') }
      resolve(ok ? image : undefined)
    }
    function abort() { finish(false) }
    signal.addEventListener('abort', abort, { once: true })
    image.onload = () => finish(!!image.naturalWidth)
    image.onerror = abort
    document.body.append(image)
    image.src = url
  })
}

watch(selection, (next, _, cleanup) => {
  const request = new AbortController()
  const closeEntry = layer.captureClose()
  cleanup(() => request.abort())
  transition = transition.then(async () => {
    await dismissViewer()
    if (next === undefined || request.signal.aborted) return
    if (next === null) { layer.drop(); return }
    await nextTick()
    if (request.signal.aborted) return
    closeControl.value?.$el?.focus({ preventScroll: true })
    // Prefer the real thumbnail for the zoom animation. A refresh or a
    // virtualized row does not have to mount that thumbnail to restore a preview.
    const candidate = document.querySelector<HTMLButtonElement>(`button[data-image-preview="${CSS.escape(next.key)}"]`)
    const source = candidate?.querySelector('img')
    const expected = new URL(next.url, location.origin)
    const actual = source && new URL(source.src)
    const matchesRoute = actual && actual.origin === expected.origin && actual.pathname === expected.pathname
    const trigger = matchesRoute ? candidate : undefined
    const thumb = matchesRoute ? source : undefined
    const fromThumbnail = !!thumb?.complete && !!thumb.naturalWidth && !!thumb.getClientRects().length
    const image = fromThumbnail ? thumb : await restoreSource(next.url, request.signal)
    if (!image || request.signal.aborted) {
      if (!fromThumbnail) image?.remove()
      if (!request.signal.aborted) closeEntry()
      return
    }
    const zoom = messageImageZoom()
    presenting.value = true
    const closed = new Promise<void>(resolve => image.addEventListener('medium-zoom:closed', () => {
      // The library clears its active image after emitting closed.
      queueMicrotask(() => { zoom.detach(image); if (!fromThumbnail) image.remove() })
      viewer = undefined
      presenting.value = false
      closeEntry() // A library-originated dismissal consumes this exact entry only.
      if (route.path === next.routePath && (!selection.value || selection.value.key === next.key)
        && trigger?.isConnected && trigger.dataset.imagePreview === next.key) trigger.focus({ preventScroll: true })
      resolve()
    }, { once: true }))
    viewer = { closed, opened: zoom.attach(image).open({ target: image }) }
  })
}, { immediate: true })

function keydown(event: KeyboardEvent) {
  if (!visible.value) return
  if (isPlainEscape(event)) {
    event.preventDefault(); event.stopPropagation(); layer.close()
  } else if (event.key === 'Tab') {
    event.preventDefault(); closeControl.value?.$el?.focus({ preventScroll: true })
  }
}
function click(event: MouseEvent) {
  if (!visible.value || !(event.target instanceof Element) || !event.target.closest('.medium-zoom-overlay, .medium-zoom-image--opened')) return
  event.preventDefault(); event.stopPropagation(); layer.close()
}
watch(visible, (open, _, cleanup) => {
  if (!open) return
  document.addEventListener('keydown', keydown)
  document.addEventListener('click', click, true)
  cleanup(() => {
    document.removeEventListener('keydown', keydown)
    document.removeEventListener('click', click, true)
  })
}, { immediate: true })
onScopeDispose(() => { void dismissViewer() })
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="message-image-preview-controls" :class="{ 'preview-loading': !presenting }" role="dialog" @click.self="layer.close" aria-modal="true" :aria-label="t('attachedImage')">
      <v-button ref="closeControl" class="message-image-preview-close" variant="text" shape="circle" :aria-label="t('close')" @click="layer.close"><template #icon><IconX /></template></v-button>
    </div>
  </Teleport>
</template>

<style>
.message-image-preview-source:not(.medium-zoom-image--opened){position:fixed;top:50%;left:51%;max-width:min(120px,35vw);max-height:min(90px,35vh);transform:translate(-50%,-50%);visibility:hidden;pointer-events:none}
.message-image-preview-controls{position:fixed;inset:0;z-index:1102;pointer-events:none}
.message-image-preview-controls.preview-loading{background:rgba(7,12,21,.88);pointer-events:auto}
.message-image-preview-close.ui-btn{position:absolute;top:max(12px,env(safe-area-inset-top));right:max(12px,env(safe-area-inset-right));width:44px;height:44px;background:var(--surface);color:var(--text);pointer-events:auto}
.medium-zoom-overlay{z-index:1100}
.medium-zoom-image--opened{z-index:1101}
/* medium-zoom's own transition is !important and waits for transitionend. */
@media(prefers-reduced-motion:reduce){body .medium-zoom-overlay,body .medium-zoom-image{transition-duration:1ms!important}}
</style>
