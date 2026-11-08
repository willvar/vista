<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AgentTaskView } from './client/types'
import FullTraceOutput from './FullTraceOutput.vue'
import { Button } from './ui'

const props = withDefaults(defineProps<{ sessionId: string; report: NonNullable<AgentTaskView['report']>; expanded?: boolean; manual?: boolean }>(), { expanded: true })
const opened = ref(false)
const { t } = useI18n()
const path = computed(() => `/sessions/${encodeURIComponent(props.sessionId)}/runs/${encodeURIComponent(props.report.runId)}/model-output?turn=${props.report.turn}&full=1`)
</script>

<template>
  <div class="trace-report">
    <Button v-if="manual" variant="link" :aria-expanded="opened" @click="opened = !opened">{{ t('traceTaskReport') }}</Button>
    <small v-else>{{ t('traceTaskReport') }}</small>
    <FullTraceOutput v-if="!manual || opened" :path="path" :snapshot="String(report.bytes)" :expanded="expanded" markdown />
  </div>
</template>

<style scoped>
.trace-report{margin-top:.6rem}.trace-report>small{display:block;margin-bottom:.3rem;font-size:15px}
</style>
