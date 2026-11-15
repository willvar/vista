<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconArrowsMaximize } from '@tabler/icons-vue'
import { Modal } from './ui'
import CopyAction from './ui/CopyAction.vue'
import { useEscapeDismiss } from './composables/useEscape'

withDefaults(defineProps<{ title: string; text: string; summary?: string; kind?: 'text' | 'markdown' | 'search' | 'instructions' }>(), { kind: 'text' })
const { t } = useI18n()
const enlarged = ref(false)
const viewport = ref<HTMLElement | null>(null)
const enlargedViewport = ref<HTMLElement | null>(null)
const retainedHeight = ref(0)
let scrollTop = 0, scrollLeft = 0
useEscapeDismiss(enlarged)
function enlarge() {
  const element = viewport.value
  if (!element) return
  retainedHeight.value = element.offsetHeight
  scrollTop = element.scrollTop
  scrollLeft = element.scrollLeft
  enlarged.value = true
}
watch(enlarged, async open => {
  if (open) return
  await nextTick()
  if (!enlarged.value && viewport.value) {
    viewport.value.scrollTop = scrollTop
    viewport.value.scrollLeft = scrollLeft
  }
})
</script>

<template>
  <section class="trace-result-card" :data-result-kind="kind" :aria-label="title">
    <header class="trace-result-heading">
      <div class="trace-result-caption"><strong>{{ title }}</strong><small v-if="summary" class="trace-result-summary">{{ summary }}</small></div>
      <div class="trace-result-actions">
        <CopyAction :text="text" :label="t('traceCopyResult')" />
        <button type="button" class="reply-action" :aria-label="t('traceEnlargeResult')" :title="t('traceEnlargeResult')" @click="enlarge"><IconArrowsMaximize aria-hidden="true" /></button>
      </div>
    </header>
    <div ref="viewport" class="trace-result-viewport" :class="{ 'trace-result-text': kind === 'text' }" :style="enlarged ? { height: `${retainedHeight}px` } : undefined" :tabindex="enlarged ? -1 : 0" :aria-hidden="enlarged || undefined" :aria-label="title">
      <slot v-if="!enlarged" />
    </div>
  </section>
  <Modal v-model:open="enlarged" :title="title" :footer="null" width="min(1100px, calc(100vw - 2rem))" root-class="trace-result-modal" destroy-on-hidden :initial-focus-el="() => enlargedViewport"
    :styles="{ container: { display: 'flex', flexDirection: 'column', height: 'min(80dvh, 52rem)', maxHeight: 'calc(100dvh - 2rem)' }, body: { display: 'flex', flexDirection: 'column', minHeight: 0, padding: 0, overflow: 'hidden' } }">
    <template #header-actions><div class="trace-result-actions"><CopyAction :text="text" :label="t('traceCopyResult')" /></div></template>
    <div v-if="enlarged" ref="enlargedViewport" class="trace-result-viewport trace-result-enlarged" :class="{ 'trace-result-text': kind === 'text' }" tabindex="0" :aria-label="title"><slot /></div>
  </Modal>
</template>

<style scoped>
.trace-result-card{min-width:0;max-width:100%;border:1px solid var(--border);border-radius:8px;overflow:hidden;background:var(--surface);color:var(--text)}
.trace-result-heading{display:flex;align-items:center;justify-content:space-between;gap:.75rem;padding:.45rem .65rem .45rem .75rem;border-bottom:1px solid var(--border);background:var(--code-bg)}
.trace-result-caption{display:flex;align-items:baseline;flex-wrap:wrap;gap:.2rem .75rem;flex:1;min-width:0;font-size:14px;line-height:1.5;overflow-wrap:anywhere}
.trace-result-caption strong{font-weight:500}.trace-result-caption small{font-size:12px;color:var(--muted)}
.trace-result-actions{display:flex;align-items:center;gap:2px;flex:none}
.trace-result-actions :deep(.reply-action){display:inline-flex;align-items:center;justify-content:center;flex:none;width:30px;height:30px;min-width:30px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--muted);font-size:16px;line-height:1;cursor:pointer}
.trace-result-actions :deep(.reply-action:hover:not(:disabled)){color:var(--accent);background:var(--hover)}
.trace-result-actions :deep(.reply-action:focus-visible){outline:var(--focus-outline,2px solid var(--accent));outline-offset:2px}
.trace-result-actions :deep(.reply-action:disabled){opacity:.45;cursor:not-allowed}
.trace-result-actions :deep(.tabler-icon){width:17px;height:17px}
.trace-result-viewport{box-sizing:border-box;min-width:0;max-width:100%;max-height:min(24rem,40dvh);padding:.75rem;overflow:auto;scrollbar-gutter:stable;overscroll-behavior:auto;overflow-wrap:anywhere;font-size:14px;line-height:1.7}
.trace-result-viewport:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:-2px}
.trace-result-text{background:var(--code-bg)}
.trace-result-text :deep(.trace-output){font-family:var(--font-mono,monospace);font-size:14px;line-height:1.7;color:var(--text-secondary)}
.trace-result-viewport :deep(.markdown){font-size:14px;color:var(--text);line-height:1.7}
.trace-result-viewport :deep(.markdown>:first-child){margin-top:0}.trace-result-viewport :deep(.markdown>:last-child){margin-bottom:0}
.trace-result-viewport :deep(.markdown pre){max-height:none}
.trace-result-enlarged{flex:1;max-height:none;min-height:0}
@media(max-width:600px){.trace-result-heading{gap:.5rem;padding:.35rem .4rem .35rem .6rem}.trace-result-actions :deep(.reply-action){width:34px;height:34px;min-width:34px}.trace-result-viewport{padding:.6rem}}
@media(max-height:520px){.trace-result-heading{padding-block:.25rem}.trace-result-viewport{max-height:40dvh}.trace-result-enlarged{max-height:none}}
</style>
