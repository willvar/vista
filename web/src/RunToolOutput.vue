<script setup lang="ts">
import { computed } from 'vue'
import { Button } from 'antdv-next'
import { useI18n } from 'vue-i18n'
import type { Artifact, Job } from './client/types'
import { projectToolOutput, toolOperationLabelKey, type RunTraceNode } from './state/runTrace'
import { artifactsForTool, jobsForTool, toolOutputErrors } from './state/toolOutputs'
import FullTraceOutput from './FullTraceOutput.vue'
import TraceToolResult from './TraceToolResult.vue'

const props = withDefaults(defineProps<{ sessionId: string; runId: string; node: RunTraceNode; allArtifacts: Artifact[]; allJobs: Job[]; expanded?: boolean }>(), { expanded: true })
const emit = defineEmits<{ openJob: [id: string] }>()
const { t } = useI18n({ useScope: 'global' })
const artifacts = computed(() => artifactsForTool(props.node, props.runId, props.allArtifacts))
const jobs = computed(() => jobsForTool(props.node, props.runId, props.allJobs))
const errors = computed(() => toolOutputErrors(props.node, artifacts.value, []))
function outputPath(sessionId: string, kind: 'artifacts' | 'jobs', id: string) {
  return `/sessions/${encodeURIComponent(sessionId)}/${kind}/${encodeURIComponent(id)}/output?full=1`
}
function snapshot(item: Artifact | Job) { return `${item.bytes}/${item.complete}/${item.status}` }
function artifactResult(content: string) {
  if (['traceOpShell', 'traceOpFetch'].includes(toolOperationLabelKey(props.node.toolName))) return { body: content }
  return projectToolOutput(content, props.node.toolName)
}
</script>

<template>
  <div class="tool-outputs">
    <p v-for="error in errors" :key="error" class="tool-output-error error">{{ error }}</p>
    <TraceToolResult v-if="!artifacts.length && !jobs.length" :node="node" :show-error="false" />
    <section v-for="(item, index) in jobs" :key="item.id" class="tool-output-entry" :data-output-id="item.id">
      <TraceToolResult v-if="index === 0" :node="node" :show-error="false" />
      <Button type="link" size="small" @click="emit('openJob', item.id)">{{ t('viewProcess') }}</Button>
    </section>
    <Button v-for="id in node.jobIds.filter(id => !jobs.some(job => job.id === id))" :key="id" type="link" size="small" @click="emit('openJob', id)">{{ t('viewProcess') }}</Button>
    <section v-for="(item, index) in artifacts" :key="item.id" class="tool-output-entry" :data-output-id="item.id">
      <FullTraceOutput :path="outputPath(sessionId, 'artifacts', item.id)" :snapshot="snapshot(item)" :expanded="expanded">
        <template #default="{ content }"><TraceToolResult :node="artifactResult(content)" :show-error="!errors.length" /></template>
        <template v-if="!jobs.length && index === 0" #fallback><TraceToolResult :node="node" :show-error="false" /></template>
      </FullTraceOutput>
    </section>
  </div>
</template>

<style scoped>
.tool-outputs{min-width:0;margin-top:.6rem}
.tool-output-entry{min-width:0;padding:.4rem 0}
.tool-output-entry+.tool-output-entry{border-top:1px solid var(--border)}
.tool-output-error{color:var(--danger);overflow-wrap:anywhere}
.tool-outputs .actions{flex-wrap:wrap}
</style>
