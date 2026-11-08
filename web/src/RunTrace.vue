<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Button } from './ui'
import { BugOutlined } from '@antdv-next/icons'
import type { TraceNodeView as Node } from './ui/traceTypes'
import type { ModelResultView, StreamEvent } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import { useRunHistory } from './composables/useRunHistory'
import { buildRunTrace, buildTraceAgentHierarchy, compactTraceText, toolOperationLabelKey, traceStatus, type RunTraceNode, type RunTraceTask, type TraceAgentHierarchyEntry } from './state/runTrace'
import TraceSteps from './TraceSteps.vue'
import TraceAgentHeader from './TraceAgentHeader.vue'
import TraceDiagnostics from './TraceDiagnostics.vue'
import TraceReport from './TraceReport.vue'
import { createRunTraceView, type RunTraceViewState } from './state/runTraceView'
import { inRunTraceRange } from './state/runTraceRange'

const props = defineProps<{
  sessionId: string; runId: string; events: StreamEvent[]; active: boolean; status: string; before: number; first: number
  after?: number; through?: number
  replyTurns?: number[]; startedAt?: string; modelName?: string
  modelResults?: ModelResultView[]
  viewState?: RunTraceViewState
}>()
const emit = defineEmits<{ inspect: [] }>()
const { t } = useI18n()
const { client, statusLabel, historicalModelLabel, debugMode } = useWorkspace()
const stoppingTasks = ref<Record<string, boolean>>({})
const stopFailures = ref<Record<string, string>>({})
const diagnosticsOpen = ref(false)
async function stopTask(task: RunTraceTask) {
  const sessionId = props.sessionId, runId = props.runId
  stoppingTasks.value[task.id] = true
  stopFailures.value[task.id] = ''
  try {
    await client.request(`/sessions/${encodeURIComponent(sessionId)}/runs/${encodeURIComponent(runId)}/stop-agent`, { method: 'POST', body: JSON.stringify({ taskId: task.id }) })
  } catch (error) {
    if (sessionId === props.sessionId && runId === props.runId) stopFailures.value[task.id] = String(error)
  } finally {
    if (sessionId === props.sessionId && runId === props.runId) stoppingTasks.value[task.id] = false
  }
}
// Only full tool results have a user-controlled expansion state. Process
// messages themselves stay visible, including after virtualized remounts.
const localView = reactive(createRunTraceView())
const view = computed(() => props.viewState || localView)
const expanded = computed({ get: () => view.value.expanded, set: value => { view.value.expanded = value } })
const expandedNodes = computed({ get: () => view.value.nodes, set: value => { view.value.nodes = value } })
const segmentComplete = computed(() => props.through !== undefined)
const segmentActive = computed(() => props.active && !segmentComplete.value)
const segmentResults = computed(() => (props.modelResults || []).filter(result => result.runId === props.runId && inRunTraceRange(result.sequence, props)))
const { events, loading, failure, load, overviewFailure, loadOverview, loadNode, nodeLoading, nodeFailure, loadedSequences } = useRunHistory(client, props, expanded, () => view.value.history)
const trace = computed(() => buildRunTrace(events.value, { ...props, active: segmentActive.value, modelResults: segmentResults.value }))
const agents = computed(() => buildTraceAgentHierarchy(trace.value.tasks))
const agentsById = computed(() => new Map(agents.value.map(agent => [agent.task.id, agent])))
function safeStatus(status?: string): string { return status ? statusLabel(status) : t('traceStatusUnreported') }
function executionStatus(status?: string): string {
  if (status === 'succeeded') return t('traceCompleted')
  if (status === 'failed') return t('traceExecutionError')
  if (status === 'canceled' || status === 'cancelled') return t('statusInterrupted')
  return safeStatus(status)
}
function nodeTitle(node: RunTraceNode): string {
  if (node.kind === 'tool') {
    const name = t(toolOperationLabelKey(node.toolName))
    const summary = compactTraceText(node.input?.command || node.summary)
    return summary ? `${name} · ${summary}` : name
  }
  if (node.kind === 'reasoning') return t('modelReasoning')
  if (node.kind === 'text') return t('modelOutput')
  if (node.kind === 'source') return t('sources')
  if (node.kind === 'retry') return t('modelRequestRecovery')
  return ''
}
function taskStatus(task: RunTraceTask): string {
  if (task.id === 'main') return runStatus.value
  if (task.background && traceStatus(task.status) === 'loading') return t('traceAgentBackground')
  return task.status ? executionStatus(task.status) : t('traceStatusUnreported')
}
function agentLabel(agent?: TraceAgentHierarchyEntry): string {
  return !agent || agent.task.id === 'main' ? t('traceMainAgent') : t('traceSubagent', { number: agent.number })
}
function delegatorLabel(agent: TraceAgentHierarchyEntry): string {
  if (agent.task.id === 'main') return ''
  const parent = agent.parentId && agentsById.value.get(agent.parentId)
  return parent ? t('traceAgentDelegatedBy', { agent: agentLabel(parent) }) : t('traceAgentOriginUnknown')
}
function taskModelName(task: RunTraceTask): string {
  if (task.id === 'main') return modelName.value
  return historicalModelLabel({ key: task.modelKey, provider: task.modelProvider, id: task.modelId })
}
const items = computed(() => trace.value.nodes.map(node => ({
  ...node, title: nodeTitle(node),
  description: [node.input?.directory,
    !node.turn && node.eventData.status ? statusLabel(node.eventData.status) : '',
    node.resultSummaryKey ? t(node.resultSummaryKey, node.resultSummaryParams || {}) : node.instructions?.length === 0 ? t('traceNoInstructionFiles') : node.search?.matches.length === 0 ? t('traceNoMatches') : '',
  ].filter(Boolean).join(' · '),
})) as Node[])
const lastNodeByTask = computed(() => new Map(items.value.map(node => [node.taskId, node.key])))
function agentFor(node: RunTraceNode) { return agentsById.value.get(node.taskId)! }
const runStatusCode = computed(() => trace.value.finished?.data.status || (segmentComplete.value ? 'succeeded' : segmentActive.value ? 'active' : rootTask.value?.status || 'finished'))
const emptyReply = computed(() => trace.value.finished?.data.status === 'succeeded' && trace.value.finished.data.emptyReply === true)
const runStatus = computed(() => emptyReply.value ? t('traceEmptyReply') : trace.value.finished ? executionStatus(trace.value.finished.data.status) : segmentComplete.value ? t('traceCompleted') : segmentActive.value ? props.status : rootTask.value?.status ? executionStatus(rootTask.value.status) : t('traceFinished'))
const hasDetails = computed(() => emptyReply.value || trace.value.nodes.length > 0 || agents.value.some(agent => agent.task.id !== 'main'))
const rootTask = computed(() => trace.value.tasks.find(task => task.id === 'main'))
const modelName = computed(() => {
  const task = rootTask.value
  if (!task?.modelId) return props.modelName || ''
  return historicalModelLabel({ key: task.modelKey, provider: task.modelProvider, id: task.modelId }) || props.modelName || ''
})
function nodeId(key: string) { return `trace-${props.runId}-${props.after ?? 0}-${encodeURIComponent(key)}` }
function hydrateNode(node: RunTraceNode) {
  if (node.kind === 'tool' && !node.artifactIds.length && !node.outputRef?.artifactId && !loadedSequences.value.has(node.lastSequence) && (node.truncated || !node.body && !!node.rawBody && !node.search && !node.instructions)) void loadNode(node.lastSequence)
}
watch(() => trace.value.nodes, nodes => {
  const opened = new Set(expandedNodes.value)
  for (const node of nodes) if (opened.has(node.key) && !nodeFailure.value[node.lastSequence]) hydrateNode(node)
})
function expandNodes(keys: string[]) {
  emit('inspect')
  const opened = keys.filter(key => !expandedNodes.value.includes(key))
  expandedNodes.value = keys
  for (const key of opened) {
    const node = trace.value.nodes.find(node => node.key === key)
    if (node) hydrateNode(node)
  }
}
watch(hasDetails, details => { if (details) expanded.value = true }, { immediate: true })
function retryPage() { emit('inspect'); void load() }
watch(() => [props.sessionId, props.runId, props.after], () => {
  expanded.value = hasDetails.value
  expandedNodes.value = []
  diagnosticsOpen.value = false
})
</script>

<template>
  <section v-if="hasDetails || overviewFailure || debugMode" class="run-trace trace-messages" :data-run-id="runId" :data-run-status="runStatusCode" :data-after-sequence="after ?? 0" :data-through-sequence="through" @click.capture="emit('inspect')">
    <TraceDiagnostics v-if="debugMode" v-model:open="diagnosticsOpen" :session-id="sessionId" :run-id="runId" :agents="agents" :events="events" />
    <div v-if="overviewFailure || debugMode" class="trace-overview-error">
      <template v-if="overviewFailure"><small role="alert">{{ t('traceOverviewUnavailable') }}</small><Button @click="loadOverview">{{ t('retry') }}</Button></template>
      <Button v-if="debugMode" variant="text" :title="t('traceDebugDetails')" :aria-label="t('traceDebugDetails')" class="icon-button trace-debug-trigger" @click="diagnosticsOpen = true"><BugOutlined /></Button>
    </div>
    <Alert v-if="failure" type="error" :message="failure" />
    <Button v-if="failure" @click="retryPage">{{ t('retry') }}</Button>
    <p v-if="loading && !items.length" class="trace-loading" role="status">{{ t('loadingProcess') }}</p>
    <p v-if="emptyReply" class="trace-empty-reply">{{ t('traceEmptyReply') }}</p>
    <TraceSteps :items="items" :expanded-keys="expandedNodes" :node-loading="nodeLoading" :node-failure="nodeFailure" :node-id="nodeId" @expand="expandNodes" @retry="node => loadNode(node.lastSequence)">
      <template #before-step="{ node, index }">
        <div v-if="node.taskId !== 'main' && (!index || items[index - 1].taskId !== node.taskId)" class="trace-agent-message-heading" :data-task-id="node.taskId" :data-task-status="agentFor(node).task.status" :data-parent-task-id="agentFor(node).parentId" :data-agent-number="agentFor(node).number" :data-agent-depth="agentFor(node).depth">
          <TraceAgentHeader :label="agentLabel(agentFor(node))" :status="taskStatus(agentFor(node).task)" :model-name="taskModelName(agentFor(node).task)" :delegated-by="delegatorLabel(agentFor(node))" :purpose="agentFor(node).task.title" purpose-expanded />
          <Button v-if="traceStatus(agentFor(node).task.status) === 'loading'" variant="link" :loading="stoppingTasks[node.taskId]" @click="stopTask(agentFor(node).task)">{{ t('stopAgent') }}</Button>
          <small v-if="stopFailures[node.taskId]" role="alert">{{ stopFailures[node.taskId] }}</small>
        </div>
      </template>
      <template #tool-output="{ node, expanded }"><slot name="tool-output" :node="node" :expanded="expanded" /></template>
      <template #after-step="{ node }"><TraceReport v-if="node.taskId !== 'main' && lastNodeByTask.get(node.taskId) === node.key && agentFor(node).task.report" :session-id="sessionId" :report="agentFor(node).task.report!" manual /></template>
    </TraceSteps>
    <section v-for="agent in agents.filter(entry => entry.task.id !== 'main' && !entry.task.nodes.length)" :key="agent.task.id" class="trace-task trace-task-group trace-agent-message-heading" :data-task-id="agent.task.id" :data-parent-task-id="agent.parentId" :data-task-status="agent.task.status" data-agent-role="subagent" :data-agent-number="agent.number" :data-agent-depth="agent.depth">
      <TraceAgentHeader :label="agentLabel(agent)" :status="taskStatus(agent.task)" :model-name="taskModelName(agent.task)" :delegated-by="delegatorLabel(agent)" :purpose="agent.task.title" purpose-expanded />
      <Button v-if="traceStatus(agent.task.status) === 'loading'" variant="link" :loading="stoppingTasks[agent.task.id]" @click="stopTask(agent.task)">{{ t('stopAgent') }}</Button>
      <small v-if="stopFailures[agent.task.id]" role="alert">{{ stopFailures[agent.task.id] }}</small>
      <TraceReport v-if="agent.task.report" :session-id="sessionId" :report="agent.task.report" manual />
    </section>
    <slot />
  </section>
</template>

<style scoped>
.trace-messages{min-width:0;color:var(--muted);font-size:var(--font-size-body);line-height:1.65}
.trace-messages :deep(.trace-step-row){padding:.35rem 0 .8rem}
.trace-agent-message-heading{display:flex;flex-wrap:wrap;align-items:flex-start;justify-content:space-between;gap:.4rem .8rem;margin:.8rem 0 .4rem;padding:.5rem .7rem;border-inline-start:2px solid var(--border-subtle);border-radius:0 8px 8px 0;background:var(--surface-subtle)}
.trace-agent-message-heading :deep(.trace-agent-heading){flex:1;min-width:0}
.trace-overview-error{display:flex;align-items:center;gap:.5rem}.trace-debug-trigger{margin-inline-start:auto}
.trace-loading,.trace-empty-reply{color:var(--muted)}
</style>
