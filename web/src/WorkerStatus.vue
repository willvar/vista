<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HeaderStatusPopover from './HeaderStatusPopover.vue'
import type { ServerStatus } from './client/types'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ memory?: ServerStatus['workerMetrics'][number]['memory']; available: boolean }>()
const { t } = useI18n()
const { localTime, mib } = useWorkspace()
const fallback = computed(() => t(props.available ? 'workerAwaitingMetrics' : 'workerRetryHint'))
const tooltip = computed(() => props.memory?.sampledAt
  ? t('workerMemory', { heap: mib(props.memory.heapBytes), rss: mib(props.memory.rssBytes), time: localTime(props.memory.sampledAt) })
  : fallback.value)
</script>

<template>
  <HeaderStatusPopover :label="t('workerResources')" :tooltip="tooltip" trigger-class="worker-status-trigger">
    <div class="worker-metrics-detail">
      <strong>{{ t('workerResources') }}</strong>
      <template v-if="memory?.sampledAt">
        <p>{{ t('workerMemoryUsage', { heap: mib(memory.heapBytes), rss: mib(memory.rssBytes) }) }}</p>
        <p>{{ t('workerMemorySample', { time: localTime(memory.sampledAt) }) }}</p>
      </template>
      <p v-else>{{ fallback }}</p>
    </div>
    <template #icon>
      <svg class="worker-memory-icon" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="6" y="6" width="12" height="12" rx="2" /><rect x="9" y="9" width="6" height="6" rx="1" />
        <path d="M9 3v3m6-3v3M9 18v3m6-3v3M3 9h3m-3 6h3m12-6h3m-3 6h3" />
      </svg>
    </template>
  </HeaderStatusPopover>
</template>
