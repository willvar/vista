<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AgentTaskView } from './client/types'
import FullTraceOutput from './FullTraceOutput.vue'

const props = withDefaults(defineProps<{ sessionId: string; report: NonNullable<AgentTaskView['report']>; expanded?: boolean }>(), { expanded: true })
const { t } = useI18n()
const path = computed(() => `/sessions/${encodeURIComponent(props.sessionId)}/runs/${encodeURIComponent(props.report.runId)}/model-output?turn=${props.report.turn}&full=1`)
</script>

<template>
  <div class="trace-report">
    <small>{{ t('traceTaskReport') }}</small>
    <FullTraceOutput :path="path" :snapshot="String(report.bytes)" :expanded="expanded" markdown />
  </div>
</template>

<style scoped>
.trace-report{margin-top:.6rem}.trace-report>small{display:block;margin-bottom:.3rem;font-size:15px}
</style>
