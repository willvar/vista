<script setup lang="ts">
import { computed, nextTick, onScopeDispose, reactive, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Button } from 'antdv-next'
import { CheckOutlined, ClockCircleOutlined, ExclamationCircleOutlined, LoadingOutlined, MinusOutlined, StopOutlined } from '@antdv-next/icons'
import Think from '@antdv-next/x/dist/think/index'
import type { ThoughtChainItemType } from '@antdv-next/x/dist/thought-chain/interface'
import type { ContextNotice, ModelResultView, StreamEvent } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import { useRunHistory } from './composables/useRunHistory'
import { useScrollbarDrag } from './composables/useScrollbarDrag'
import { buildRunTrace, buildTraceAgentHierarchy, compactTraceText, toolOperationLabelKey, traceEventTime, traceStatus, type RunTraceNode, type RunTraceTask, type TraceAgentHierarchyEntry } from './state/runTrace'
import TraceSteps from './TraceSteps.vue'
import TraceAgentHeader from './TraceAgentHeader.vue'
import TraceDiagnostics from './TraceDiagnostics.vue'
import { createRunTraceView, type RunTraceViewState } from './state/runTraceView'
import { inRunTraceRange } from './state/runTraceRange'

type Node = RunTraceNode & ThoughtChainItemType
const props = defineProps<{
  sessionId: string; runId: string; events: StreamEvent[]; active: boolean; status: string; before: number; first: number
  after?: number; through?: number
  replyTurns?: number[]; calls: number; startedAt?: string; modelName?: string
  modelResults?: ModelResultView[]; contextNotices?: ContextNotice[]
  viewState?: RunTraceViewState
}>()
const emit = defineEmits<{ inspect: [] }>()
const { t } = useI18n()
const { client, statusLabel, localTime, historicalModelLabel, debugMode } = useWorkspace()
const stoppingTasks = ref<Record<string, boolean>>({})
const stopFailures = ref<Record<string, string>>({})
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
// Expansion belongs to the user. Event and status changes never open or close history.
// Kept by the conversation so virtualized messages can unmount without losing
// which sessions or steps the user opened.
const localView = reactive(createRunTraceView())
const view = computed(() => props.viewState || localView)
const expanded = computed({ get: () => view.value.expanded, set: value => { view.value.expanded = value } })
const expandedTasks = computed({ get: () => view.value.tasks, set: value => { view.value.tasks = value } })
const expandedNodes = computed({ get: () => view.value.nodes, set: value => { view.value.nodes = value } })
const root = ref<HTMLElement | null>(null)
const segmentComplete = computed(() => props.through !== undefined)
const segmentActive = computed(() => props.active && !segmentComplete.value)
const segmentResults = computed(() => (props.modelResults || []).filter(result => result.runId === props.runId && inRunTraceRange(result.sequence, props)))
const scrollbarDragging = useScrollbarDrag()
// Completion compacts the overview. Keep the step being read at its current
// viewport offset while that preceding layout changes; no expansion or jump.
watch(segmentActive, (active, wasActive) => {
  if (active || !wasActive || !expanded.value || scrollbarDragging.value) return
  const scroller = root.value?.closest<HTMLElement>('.messages')
  if (!scroller || !root.value) return
  const viewport = scroller.getBoundingClientRect()
  const titles = [...root.value.querySelectorAll<HTMLElement>('.trace-reasoning[id], .antd-thought-chain-node-title [id]')]
  const candidates = titles.filter(element => {
    const bounds = element.getBoundingClientRect()
    return bounds.height > 0 && bounds.bottom > viewport.top && bounds.top < viewport.bottom
  }).map(element => ({ element, top: element.getBoundingClientRect().top }))
  // Prefer an open step as the reading anchor when the overview compacts.
  const inspectedIds = expandedNodes.value.map(nodeId).reverse()
  candidates.sort((a, b) => {
    const priority = (element: HTMLElement) => {
      const index = inspectedIds.indexOf(element.id)
      return index < 0 ? inspectedIds.length : index
    }
    return priority(a.element) - priority(b.element)
  })
  void nextTick(() => {
    const anchor = candidates.find(candidate => candidate.element.isConnected)
    if (anchor && !segmentActive.value && !scrollbarDragging.value && scroller === root.value?.closest('.messages')) {
      scroller.scrollTop += anchor.element.getBoundingClientRect().top - anchor.top
    }
  })
}, { flush: 'sync' })
const { events, loading, failure, load, overview, overviewLoading, overviewFailure, loadOverview, modelCalls, loadNode, nodeLoading, nodeFailure, loadedSequences } = useRunHistory(client, props, expanded, () => view.value.history)
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
  return ''
}
function taskStatus(task: RunTraceTask): string {
  if (task.id === 'main') return runStatus.value
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
const itemsByTask = computed(() => new Map(trace.value.tasks.map(task => [task.id, task.nodes.map(node => ({
    ...node, title: nodeTitle(node), collapsible: node.kind !== 'reasoning' && (node.kind !== 'tool' || !!(node.body || node.error || node.outputRef || node.artifactIds.length || node.jobIds.length || node.search?.matches.length || node.instructions?.length || node.truncated)),
    description: [node.input?.directory,
      !node.turn && node.eventData.status ? statusLabel(node.eventData.status) : '',
      node.resultSummaryKey ? t(node.resultSummaryKey, node.resultSummaryParams || {}) : node.instructions?.length === 0 ? t('traceNoInstructionFiles') : node.search?.matches.length === 0 ? t('traceNoMatches') : '',
    ].filter(Boolean).join(' · '),
  })) as Node[]])))
function taskItems(task: RunTraceTask): Node[] { return itemsByTask.value.get(task.id) || [] }
const runStatusCode = computed(() => trace.value.finished?.data.status || (segmentComplete.value ? 'succeeded' : segmentActive.value ? 'active' : rootTask.value?.status || 'finished'))
const runStatus = computed(() => trace.value.finished ? executionStatus(trace.value.finished.data.status) : segmentComplete.value ? t('traceCompleted') : segmentActive.value ? props.status : rootTask.value?.status ? executionStatus(rootTask.value.status) : t('traceFinished'))
const checklistIcons: Record<string, Component> = {
  done: CheckOutlined, in_progress: LoadingOutlined, blocked: ExclamationCircleOutlined,
  interrupted: StopOutlined, incomplete: MinusOutlined, pending: ClockCircleOutlined,
}
const checklist = computed(() => {
  const items = trace.value.checklist ?? []
  if (segmentActive.value) return items
  const status = trace.value.finished?.data.status || rootTask.value?.status
  const stoppedStatus = ['canceled', 'cancelled', 'interrupted'].includes(status || '') ? 'interrupted' : 'incomplete'
  return items.map(item => item.status === 'in_progress' ? { ...item, status: stoppedStatus } : item)
})
const completedItems = computed(() => checklist.value.filter(item => item.status === 'done').length)
const contextNotices = computed(() => {
  const notices = new Map<number, ContextNotice>()
  for (const notice of props.contextNotices || []) if (inRunTraceRange(notice.sequence, props)) notices.set(notice.sequence, notice)
  for (const event of events.value) if (event.kind === 'context_notice' && !event.data.depth) notices.set(event.sequence, {
    sequence: event.sequence, message: event.data.message, summary: event.data.summary,
  })
  return [...notices.values()].sort((a, b) => a.sequence - b.sequence)
})
const hasDetails = computed(() => trace.value.nodes.length > 0 || agents.value.some(agent => agent.task.id !== 'main') || contextNotices.value.length > 0)
const callCount = computed(() => {
  if (modelCalls.value !== null) return modelCalls.value
  const calls = new Set(segmentResults.value.map(result => result.sequence))
  for (const event of events.value) if (event.kind === 'model_result') calls.add(event.sequence)
  const requests = events.value.filter(event => event.kind === 'model_request' && event.data.purpose !== 'summary').length
  return Math.max(calls.size, requests, props.calls || 0)
})
const rootTask = computed(() => trace.value.tasks.find(task => task.id === 'main'))
const modelName = computed(() => {
  const task = rootTask.value
  if (!task?.modelId) return props.modelName || ''
  return historicalModelLabel({ key: task.modelKey, provider: task.modelProvider, id: task.modelId }) || props.modelName || ''
})
const latestEvent = computed(() => events.value.filter(event => ['tool', 'agent_task', 'model_request', 'model_result', 'model_delta', 'model_reasoning_delta', 'run_finished'].includes(event.kind) && event.data.purpose !== 'summary').at(-1))
const activityText = computed(() => {
  const event = latestEvent.value
  if (!event) return segmentActive.value ? props.status : ''
  if (event.kind === 'tool') {
    const node = [...trace.value.nodes].reverse().find(node => node.kind === 'tool' && node.callId === event.data.tool?.callId && node.taskId === (event.data.taskId || 'main'))
    return node ? nodeTitle(node) : compactTraceText(event.data.tool?.summary || event.data.tool?.name)
  }
  if (event.kind === 'agent_task') return `${compactTraceText(event.data.task?.title || t('traceMainTask'))} · ${executionStatus(event.data.task?.status)}`
  if (event.kind === 'run_finished') return executionStatus(event.data.status)
  return t(({ model_request: 'traceModelRequest', model_result: 'traceModelResult', model_delta: 'modelOutput', model_reasoning_delta: 'modelReasoning' } as Record<string, string>)[event.kind])
})
const lastActivityAt = computed(() => {
  const eventTime = latestEvent.value && traceEventTime(latestEvent.value)
  const snapshotTime = overview.value?.lastActivityAt
  return !eventTime ? snapshotTime : !snapshotTime || Date.parse(eventTime) >= Date.parse(snapshotTime) ? eventTime : snapshotTime
})
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
watch(segmentActive, active => {
  if (clock) clearInterval(clock)
  now.value = Date.now()
  if (active) clock = setInterval(() => { now.value = Date.now() }, 1000)
}, { immediate: true })
onScopeDispose(() => { if (clock) clearInterval(clock) })
function durationText(seconds: number): string {
  return seconds >= 3600 ? `${Math.floor(seconds / 3600)}:${String(Math.floor(seconds / 60) % 60).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
    : `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}
const activityAge = computed(() => lastActivityAt.value ? durationText(Math.max(0, Math.floor((now.value - Date.parse(lastActivityAt.value)) / 1000))) : '')
const elapsed = computed(() => {
  const start = Date.parse(props.startedAt || overview.value?.startedAt || '')
  const finish = trace.value.finished && traceEventTime(trace.value.finished) || overview.value?.finishedAt
  const end = segmentActive.value ? now.value : finish ? Date.parse(finish) : NaN
  if (!Number.isFinite(start) || !Number.isFinite(end)) return ''
  const seconds = Math.max(0, Math.floor((end - start) / 1000))
  return durationText(seconds)
})
function nodeId(key: string) { return `trace-${props.runId}-${props.after ?? 0}-${encodeURIComponent(key)}` }
function hydrateNode(node: RunTraceNode) {
  if (node.kind === 'tool' && !loadedSequences.value.has(node.lastSequence) && (node.truncated || !node.body && !!node.rawBody && !node.search && !node.instructions)) void loadNode(node.lastSequence)
}
watch(() => trace.value.nodes, nodes => {
  const opened = new Set(expandedNodes.value)
  for (const node of nodes) if (opened.has(node.key) && !nodeFailure.value[node.lastSequence]) hydrateNode(node)
})
const expandedKeysByTask = computed(() => {
  const opened = new Set(expandedNodes.value)
  return new Map(trace.value.tasks.map(task => [task.id, task.nodes.filter(node => opened.has(node.key)).map(node => node.key)]))
})
function taskExpandedKeys(task: RunTraceTask): string[] { return expandedKeysByTask.value.get(task.id) || [] }
function expandNodes(task: RunTraceTask, keys: string[]) {
  emit('inspect')
  const opened = keys.filter(key => !expandedNodes.value.includes(key))
  const taskKeys = new Set(task.nodes.map(node => node.key))
  expandedNodes.value = [...expandedNodes.value.filter(key => !taskKeys.has(key)), ...keys.filter(key => taskKeys.has(key))]
  for (const key of opened) {
    const node = trace.value.nodes.find(node => node.key === key)
    if (node) hydrateNode(node)
  }
}
function toggleProcess() { emit('inspect'); expanded.value = !expanded.value }
function retryPage() { emit('inspect'); void load() }
watch(() => [props.sessionId, props.runId, props.after], () => {
  expanded.value = false
  expandedTasks.value = {}
  expandedNodes.value = []
})
</script>

<template>
  <section v-if="hasDetails || overviewFailure" ref="root" class="run-trace" :data-run-id="runId" :data-after-sequence="after ?? 0" :data-through-sequence="through" @click.capture="emit('inspect')">
    <div v-if="!hasDetails" class="trace-overview-error"><small role="alert">{{ t('traceOverviewUnavailable') }}</small><Button size="small" @click="loadOverview">{{ t('retry') }}</Button></div>
    <Think v-else v-model:expanded="expanded" class="trace-overview run-overview" :classes="{ status: 'trace-process-header' }" :loading="segmentActive" :data-run-status="runStatusCode" @expand="emit('inspect')">
      <template #title>
        <div class="trace-summary">
          <div class="trace-overview-heading" role="button" tabindex="0" :aria-expanded="expanded" :aria-label="t('executionProcess')" @keydown.enter.prevent.stop="toggleProcess" @keydown.space.prevent.stop="toggleProcess">
            <strong role="status">{{ runStatus }}</strong><small v-if="elapsed">{{ t('traceElapsed', { elapsed }) }}</small><span v-if="modelName" class="trace-model-name">{{ modelName }}</span><small>{{ t('modelCalls', { count: callCount }) }}</small><small v-if="checklist.length">{{ t('traceChecklistProgress', { done: completedItems, total: checklist.length }) }}</small>
          </div>
          <span v-if="debugMode" class="trace-summary-debug" @click.stop @keydown.stop><TraceDiagnostics :run-id="runId" :agents="agents" :events="events" /></span>
        </div>
        <div v-if="overviewFailure" class="trace-overview-error" @click.stop @keydown.stop><small role="alert">{{ t('traceOverviewUnavailable') }}</small><Button size="small" @click="loadOverview">{{ t('retry') }}</Button></div>
        <small v-else-if="overviewLoading && !events.length">{{ t('traceLoadingOverview') }}</small>
        <ul v-if="checklist.length" class="checklist trace-checklist" :aria-label="t('checklist')">
          <li v-for="(item, index) in checklist" :key="index" :class="item.status" :data-checklist-status="item.status">
            <span class="trace-checklist-icon" aria-hidden="true"><component :is="checklistIcons[item.status] || ClockCircleOutlined" /></span>
            <span>{{ item.text }}</span><small>{{ statusLabel(item.status) }}</small>
          </li>
        </ul>
        <p v-if="segmentActive && activityText" class="trace-last-activity"><span>{{ t('traceLastActivity', { activity: activityText }) }}</span><time v-if="lastActivityAt" :datetime="lastActivityAt" :title="localTime(lastActivityAt)">{{ localTime(lastActivityAt) }}</time><small v-if="activityAge">{{ t('traceActivityAge', { elapsed: activityAge }) }}</small></p>
      </template>
      <Alert v-if="failure" type="error" :message="failure" show-icon />
      <Button v-if="failure" size="small" :loading="loading" @click="retryPage">{{ t('retry') }}</Button>
      <p v-if="loading" class="trace-loading" role="status">{{ t('loadingProcess') }}</p>
      <div v-for="agent in agents" :key="agent.task.id" class="trace-task trace-task-group" :class="{ 'trace-agent-group': agent.task.id !== 'main' }" :id="nodeId(`task/${agent.task.id}`)" :data-task-id="agent.task.id" :data-parent-task-id="agent.parentId" :data-task-status="agent.task.status" :data-agent-role="agent.task.id === 'main' ? 'main' : 'subagent'" :data-agent-number="agent.number" :data-agent-depth="agent.depth" :style="{ '--agent-indent': `${Math.min(agent.depth, 2) * 12}px` }">
        <template v-if="agent.task.id === 'main'">
          <TraceSteps :session-id="sessionId" :task="agent.task" :show-report="false" :items="taskItems(agent.task)" :expanded-keys="taskExpandedKeys(agent.task)" :node-loading="nodeLoading" :node-failure="nodeFailure" :node-id="nodeId" @expand="keys => expandNodes(agent.task, keys)" @retry="node => loadNode(node.lastSequence)">
            <template #tool-output="{ node, expanded: nodeExpanded }"><slot name="tool-output" :node="node" :expanded="nodeExpanded" /></template>
          </TraceSteps>
        </template>
        <Think v-else v-model:expanded="expandedTasks[agent.task.id]" :default-expanded="false" :loading="traceStatus(agent.task.status) === 'loading'" :title="agentLabel(agent)" @expand="emit('inspect')">
          <template #title><TraceAgentHeader :label="agentLabel(agent)" :status="agent.task.background && traceStatus(agent.task.status) === 'loading' ? t('traceAgentBackground') : taskStatus(agent.task)" :model-name="taskModelName(agent.task)" :delegated-by="delegatorLabel(agent)" :purpose="agent.task.title" :purpose-expanded="expandedTasks[agent.task.id]" /></template>
          <Button v-if="traceStatus(agent.task.status) === 'loading'" size="small" :loading="stoppingTasks[agent.task.id]" @click.stop="stopTask(agent.task)">{{ t('stopAgent') }}</Button>
          <Alert v-if="stopFailures[agent.task.id]" type="error" :message="stopFailures[agent.task.id]" />
          <TraceSteps :session-id="sessionId" :task="agent.task" show-report :items="taskItems(agent.task)" :expanded-keys="taskExpandedKeys(agent.task)" :node-loading="nodeLoading" :node-failure="nodeFailure" :node-id="nodeId" @expand="keys => expandNodes(agent.task, keys)" @retry="node => loadNode(node.lastSequence)">
            <template #tool-output="{ node, expanded: nodeExpanded }"><slot name="tool-output" :node="node" :expanded="nodeExpanded" /></template>
          </TraceSteps>
        </Think>
      </div>
      <div v-for="notice in contextNotices" :key="notice.sequence" class="activity context-notice"><p>{{ t('contextCompacted') }}</p><details v-if="notice.summary"><summary>{{ t('contextSummaryPreview') }}</summary><pre>{{ notice.summary }}</pre></details></div>
      <slot />
    </Think>
  </section>
</template>

<style scoped>
.run-trace :deep(.antd-think-content){border-inline-start:0}
.trace-overview{--trace-summary-line-height:26px;min-width:0}
.trace-overview :deep(.trace-process-header){position:relative;width:100%;align-items:flex-start;padding:.55rem 0;border:0;border-radius:8px}
.trace-overview :deep(.trace-process-header:hover){border-color:var(--border-strong);background:var(--surface-subtle)}
.trace-overview :deep(.trace-process-header>.antd-think-status-text){display:block;min-width:0}
.trace-overview :deep(.trace-process-header>.antd-think-status-icon){height:var(--trace-summary-line-height);margin-top:0;align-items:center;justify-content:center}
/* Leave room for the arrow's corners throughout its rotation. */
.trace-overview :deep(.trace-process-header>.antd-think-status-down-icon){width:12px;height:12px;margin:7px;flex:none;align-items:center;justify-content:center}
.trace-summary{display:flex;align-items:flex-start;gap:.5rem;min-width:0;width:100%}
.trace-overview-heading{display:flex;align-items:baseline;flex-wrap:wrap;gap:.35rem .65rem;flex:1;min-width:0;font-size:15px;line-height:var(--trace-summary-line-height);color:var(--text-secondary)}
.trace-overview-heading:focus-visible{outline:2px solid var(--accent);outline-offset:3px;border-radius:3px}
.trace-overview-heading strong{font-size:14px;font-weight:500;color:var(--text)}.trace-overview-heading small{font-size:14px}.trace-model-name{overflow-wrap:anywhere}
.trace-summary-debug{display:inline-flex;align-items:center;flex:none;height:var(--trace-summary-line-height)}.trace-summary-debug :deep(.trace-debug-trigger){margin:0}
.trace-last-activity{display:flex;flex-wrap:wrap;gap:.4rem;margin:.25rem 0 0;font-size:15px;color:var(--muted);overflow-wrap:anywhere}
.trace-last-activity time{font-size:14px}.trace-checklist{font-size:16px;line-height:1.65;margin:.55rem 0 0;overflow-wrap:anywhere}.trace-checklist small{flex:none;font-size:14px}
.trace-checklist-icon{display:inline-flex;align-items:center;justify-content:center;align-self:flex-start;flex:none;width:1em;height:1.65em}
.trace-overview-error{display:flex;align-items:center;gap:.5rem;margin-top:.35rem}
.trace-loading{font-size:15px;color:var(--muted)}.context-notice{font-size:15px;margin:.75rem 0}.context-notice pre{white-space:pre-wrap;overflow-wrap:anywhere}.context-notice summary{cursor:pointer}
.trace-task{margin:.65rem 0;min-width:0}.trace-agent-group{margin-left:var(--agent-indent);padding:.65rem .8rem;border-left:2px solid var(--border);border-radius:0 8px 8px 0;background:var(--surface-subtle)}.trace-agent-group[data-agent-depth="2"],.trace-agent-group[data-agent-depth="3"]{border-left-color:var(--accent)}.trace-agent-group :deep(.antd-think-status-wrapper){align-items:flex-start;width:100%}.trace-agent-group :deep(.antd-think-status-text){flex:1}.trace-task-group :deep(.antd-thought-chain-node-title){font-size:15px;line-height:1.5;color:var(--text-secondary)}.trace-task-group :deep(.antd-thought-chain-node-description){font-size:15px;line-height:1.6;color:var(--muted)}.trace-details{margin-top:.5rem;font-size:15px;color:var(--muted)}
.trace-details summary{cursor:pointer}.trace-details dl{display:grid;grid-template-columns:auto minmax(0,1fr);gap:.2rem .7rem}.trace-details dd{margin:0;overflow-wrap:anywhere}
@media(max-width:600px){.trace-overview :deep(.trace-process-header){padding:.4rem .5rem}.trace-checklist li{flex-wrap:wrap}.trace-checklist small{margin-left:1rem}.trace-last-activity time{display:block}}
</style>
