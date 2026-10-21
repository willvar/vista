<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Modal } from 'antdv-next'
import { BugOutlined } from '@antdv-next/icons'
import type { StreamEvent } from './client/types'
import type { TraceAgentHierarchyEntry } from './state/runTrace'

const props = defineProps<{ runId: string; agents: TraceAgentHierarchyEntry[]; events: StreamEvent[] }>()
const { t } = useI18n()
const open = ref(false)
const details = computed(() => open.value ? JSON.stringify({ runId: props.runId,
  agents: props.agents.map(({ task, number, depth, parentId }) => ({ id: task.id, number, depth, parentId, status: task.status, modelId: task.modelId, reasoningEffort: task.reasoningEffort })),
  events: props.events.filter(event => event.data.runId === props.runId),
}, null, 2) : '')
</script>

<template>
  <Button type="text" size="small" :title="t('traceDebugDetails')" :aria-label="t('traceDebugDetails')" class="trace-debug-trigger" @click="open = true"><BugOutlined /></Button>
  <Modal :open="open" :title="t('traceDebugDetails')" centered :footer="null" :width="840" :style="{ maxWidth: 'calc(100vw - 32px)' }" destroy-on-hidden @cancel="open = false">
    <pre class="trace-debug-body">{{ details }}</pre>
  </Modal>
</template>

<style scoped>
.trace-debug-trigger{margin-left:auto;color:var(--muted)}.trace-debug-body{margin:0;max-height:65dvh;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:15px;line-height:1.6;color:var(--text-secondary)}
</style>
