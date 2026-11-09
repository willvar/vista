<script setup lang="ts">
import { computed, inject, onMounted, onUnmounted, onUpdated, provide, ref, watch } from 'vue'
import type { TimelineLayout } from './state/conversationTimeline'
import { historyReadKey } from './composables/useConversationHistory'
import { timelineReadKey } from './composables/useConversationScroll'

const props = defineProps<{ layout: TimelineLayout; paused: boolean; placeholder?: boolean }>()
const emit = defineEmits<{ measure: [element: Element | null] }>()
const element = ref<HTMLElement | null>(null)
const reads = ref(new Set<symbol>())
const reportRead = inject(timelineReadKey, undefined)
watch(() => reads.value.size > 0, pending => reportRead?.(String(props.layout.key), pending), { flush: 'sync' })
provide(historyReadKey, (key, pending) => {
  const next = new Set(reads.value)
  if (pending) next.add(key)
  else next.delete(key)
  reads.value = next
})
// Restoring an expanded result from a cold body cache must retain its last
// measured box while the original output is being read again.
const held = computed(() => props.paused || reads.value.size > 0)
const position = computed(() => ({ position: 'absolute' as const, top: '0', left: '0', width: '100%',
  transform: `translateY(${props.layout.start}px)`, height: held.value ? `${props.layout.size}px` : undefined,
  overflow: held.value ? 'clip' : undefined }))
onMounted(() => emit('measure', element.value))
onUpdated(() => emit('measure', element.value))
onUnmounted(() => { reportRead?.(String(props.layout.key), false); emit('measure', null) })
</script>

<template>
  <div ref="element" class="timeline-row" :data-index="layout.index" :data-timeline-key="layout.key" :style="position">
    <div v-if="placeholder" class="timeline-placeholder" :style="{ height: `${layout.size}px` }" aria-hidden="true" />
    <slot v-else />
  </div>
</template>
