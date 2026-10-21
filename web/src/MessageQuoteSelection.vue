<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QuotedText } from './state/quotedReply'

const props = defineProps<{ root: HTMLElement | null; sessionId: string; disabled: boolean }>()
const emit = defineEmits<{ quote: [quote: QuotedText] }>()
const { t } = useI18n({ useScope: 'global' })
const selection = ref<{ quote: QuotedText; source: HTMLElement; sessionId: string; left: number; top: number } | null>(null)
let timer = 0
let selecting = false
let selectionChanged = false

function selectedSource(node: Node | null) {
  return (node instanceof Element ? node : node?.parentElement)?.closest<HTMLElement>('[data-quote-role]')
}
function readSelection(): { selected: Selection; source: HTMLElement; quote: QuotedText } | null {
  const selected = window.getSelection()
  const source = selectedSource(selected?.anchorNode || null)
  if (props.disabled || !props.root || !selected || selected.isCollapsed || selected.rangeCount !== 1
    || !source || !props.root.contains(source) || source !== selectedSource(selected.focusNode)) return null
  const role = source.dataset.quoteRole
  const text = selected.toString()
  if (!text.trim() || role !== 'user' && role !== 'assistant') return null
  return { selected, source, quote: { text, role } }
}
function updateSelection() {
  timer = 0
  const current = readSelection()
  if (!current || !props.root || selecting) return
  const { selected, source, quote } = current
  const viewport = window.visualViewport
  const top = viewport?.offsetTop || 0
  const bottom = top + (viewport?.height || window.innerHeight)
  const bounds = props.root.getBoundingClientRect()
  const rect = [...selected.getRangeAt(0).getClientRects()].filter(rect => rect.width > 0 && rect.height > 0
    && rect.bottom > Math.max(top, bounds.top) && rect.top < Math.min(bottom, bounds.bottom)).at(-1)
  if (!rect) { selection.value = null; return }
  const below = window.matchMedia('(pointer: coarse)').matches || rect.top - 44 < Math.max(top, bounds.top)
  selection.value = {
    quote, source, sessionId: props.sessionId,
    left: Math.max(8, Math.min(rect.left, document.documentElement.clientWidth - 160)),
    top: Math.max(top + 8, Math.min(below ? rect.bottom + 8 : rect.top - 44, bottom - 44)),
  }
}
function hideAction() {
  window.clearTimeout(timer)
  timer = 0
  selection.value = null
}
function scheduleUpdate() {
  hideAction()
  if (selecting) { selectionChanged = true; return }
  if (readSelection()) timer = window.setTimeout(updateSelection, 350)
}
function scrollSelection() {
  // Selecting text can scroll it into view before the action is ready.
  if (timer) scheduleUpdate()
  else hideAction()
}
function startSelection() {
  selecting = true
  selectionChanged = false
  hideAction()
}
function finishSelection() {
  selecting = false
  if (selectionChanged) scheduleUpdate()
  selectionChanged = false
}
function resetSelection() {
  selecting = false
  selectionChanged = false
  hideAction()
}
function quoteSelection() {
  const current = selection.value
  if (!current || props.disabled || current.sessionId !== props.sessionId || !props.root?.contains(current.source)) return
  emit('quote', current.quote)
  hideAction()
  window.getSelection()?.removeAllRanges()
}
function dismiss(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !selection.value && !timer) return
  event.preventDefault()
  event.stopImmediatePropagation()
  resetSelection()
  window.getSelection()?.removeAllRanges()
}
watch(() => [props.root, props.sessionId, props.disabled], resetSelection)
onMounted(() => {
  document.addEventListener('selectionchange', scheduleUpdate)
  document.addEventListener('pointerdown', startSelection)
  document.addEventListener('pointerup', finishSelection)
  document.addEventListener('pointercancel', finishSelection)
  document.addEventListener('copy', hideAction)
  document.addEventListener('wheel', hideAction, { capture: true, passive: true })
  document.addEventListener('touchmove', hideAction, { capture: true, passive: true })
  document.addEventListener('scroll', scrollSelection, true)
  document.addEventListener('keydown', dismiss, true)
  window.addEventListener('blur', resetSelection)
  window.addEventListener('resize', hideAction)
  window.visualViewport?.addEventListener('resize', hideAction)
  window.visualViewport?.addEventListener('scroll', hideAction)
})
onUnmounted(() => {
  hideAction()
  document.removeEventListener('selectionchange', scheduleUpdate)
  document.removeEventListener('pointerdown', startSelection)
  document.removeEventListener('pointerup', finishSelection)
  document.removeEventListener('pointercancel', finishSelection)
  document.removeEventListener('copy', hideAction)
  document.removeEventListener('wheel', hideAction, true)
  document.removeEventListener('touchmove', hideAction, true)
  document.removeEventListener('scroll', scrollSelection, true)
  document.removeEventListener('keydown', dismiss, true)
  window.removeEventListener('blur', resetSelection)
  window.removeEventListener('resize', hideAction)
  window.visualViewport?.removeEventListener('resize', hideAction)
  window.visualViewport?.removeEventListener('scroll', hideAction)
})
</script>

<template>
  <Teleport to="body">
    <Transition name="quote-action">
      <button v-if="selection" type="button" class="message-quote-action" :style="{ left: `${selection.left}px`, top: `${selection.top}px` }" @pointerdown.stop.prevent @pointerup.stop @click="quoteSelection">
        <span class="quote-mark" aria-hidden="true">“</span>{{ t('quoteSelection') }}
      </button>
    </Transition>
  </Teleport>
</template>

<style scoped>
.message-quote-action{position:fixed;z-index:900;display:flex;align-items:center;gap:7px;height:36px;padding:0 12px;border:1px solid var(--accent);border-radius:9px;background:var(--accent);color:var(--accent-text);box-shadow:0 4px 16px #0002;font:inherit;font-size:14px;white-space:nowrap;cursor:pointer;touch-action:manipulation}
.message-quote-action:hover{filter:brightness(1.08)}
.message-quote-action:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
.quote-mark{height:24px;font-family:Georgia,serif;font-size:32px;line-height:1}
@media(prefers-reduced-motion:no-preference){
  .quote-action-enter-active{transition:opacity .14s ease-out,transform .14s ease-out}
  .quote-action-enter-from{opacity:0;transform:translateY(4px)}
}
</style>
