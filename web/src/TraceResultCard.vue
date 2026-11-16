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
