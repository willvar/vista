<script setup lang="ts">
import { computed, onScopeDispose, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Button, Modal, Select, Tabs, TabPane } from 'antdv-next'
import { CopyOutlined, ReloadOutlined } from '@antdv-next/icons'
import type { StreamEvent } from './client/types'
import type { TraceAgentHierarchyEntry } from './state/runTrace'
import type { ModelDiagnosticRecord, ModelDiagnosticSummary } from './client/modelDiagnostics'
import { mergeDiagnosticSummaries } from './client/modelDiagnostics'
import { useWorkspace } from './composables/useWorkspace'
import { formatDateTime } from './ui/datetime'

const props = defineProps<{ sessionId: string; runId: string; agents: TraceAgentHierarchyEntry[]; events: StreamEvent[] }>()
const { t, locale } = useI18n()
const { client, conversation, stateSync } = useWorkspace()
const selectId = useId()
const open = defineModel<boolean>('open', { default: false })
const tab = ref('events')
const records = ref<ModelDiagnosticSummary[]>([])
const selectedId = ref('')
const record = ref<ModelDiagnosticRecord | null>(null)
const indexLoading = ref(false)
const recordLoading = ref(false)
const loading = computed(() => indexLoading.value || recordLoading.value)
const error = ref('')
let indexController: AbortController | undefined
let recordController: AbortController | undefined
let generation = 0
let recordGeneration = 0
let unsubscribe: (() => void) | undefined
const basePath = computed(() => `/sessions/${encodeURIComponent(props.sessionId)}/runs/${encodeURIComponent(props.runId)}/diagnostics`)
const options = computed(() => records.value.map(item => ({ value: item.id,
  label: `${formatDateTime(item.startedAt, locale.value)} · ${item.purpose || 'conversation'} · ${item.taskId || 'main'}${item.turn ? ` / ${item.turn}` : ''} · ${item.model}${item.error ? ` · ${t('statusFailed')}` : ''}`,
})))
function cancel() {
  generation++; recordGeneration++
  indexController?.abort(); recordController?.abort()
  indexLoading.value = false; recordLoading.value = false
}
function accept(values: ModelDiagnosticSummary[]) {
  records.value = values
  if (!values.some(item => item.id === selectedId.value)) {
    recordGeneration++
    recordController?.abort()
    recordLoading.value = false
    const newest = [...values].reverse()
    selectedId.value = (newest.find(item => item.error) || newest.find(item => item.purpose !== 'title') || newest[0])?.id || ''
    record.value = null
  }
  // Saved bodies are immutable. New index entries do not reload the currently
  // selected body or interrupt the user's inspection of an earlier request.
  if (selectedId.value && record.value?.id !== selectedId.value && !recordLoading.value) void readRecord()
}
async function load(signal?: AbortSignal, propagate = false) {
  if (!open.value || tab.value !== 'http') return
  indexController?.abort()
  const controller = indexController = new AbortController()
  const requestSignal = signal ? AbortSignal.any([signal, controller.signal]) : controller.signal
  const revision = ++generation
  indexLoading.value = true
  error.value = ''
  try {
    const values = await client.request<ModelDiagnosticSummary[]>(basePath.value, { signal: requestSignal })
    if (requestSignal.aborted || revision !== generation) return
    accept(values)
    return true
  } catch (cause) {
    if (!requestSignal.aborted && revision === generation) error.value = String(cause)
    if (propagate) throw cause
  } finally { if (revision === generation) indexLoading.value = false }
}
async function readRecord() {
  recordController?.abort()
  const controller = recordController = new AbortController()
  const revision = ++recordGeneration
  const id = selectedId.value
  if (!id) { record.value = null; recordLoading.value = false; return }
  recordLoading.value = true
  error.value = ''
  try {
    const value = await client.request<ModelDiagnosticRecord>(`${basePath.value}/${encodeURIComponent(id)}`, { signal: controller.signal })
    if (!controller.signal.aborted && revision === recordGeneration) record.value = value
  } catch (cause) {
    if (!controller.signal.aborted && revision === recordGeneration) error.value = String(cause)
  } finally { if (revision === recordGeneration) recordLoading.value = false }
}
function select(value: unknown) { selectedId.value = String(value); record.value = null; void readRecord() }
function pretty(value: unknown) { return JSON.stringify(value, null, 2) }
function requestBody(content: string) { try { return pretty(JSON.parse(content)) } catch { return content } }
function headers(values: Record<string, string[]> = {}) { return Object.entries(values).map(([name, values]) => `${name}: ${values.join(', ')}`).join('\n') }
async function copy() {
  try { await navigator.clipboard.writeText(pretty(record.value)) }
  catch (cause) { error.value = String(cause) }
}
watch([open, tab, () => props.sessionId, () => props.runId], () => {
  unsubscribe?.(); unsubscribe = undefined
  cancel(); records.value = []; selectedId.value = ''; record.value = null; error.value = ''
  if (!open.value || tab.value !== 'http') return
  let initialized = false
  let through = 0
  unsubscribe = stateSync.subscribe(['diagnostics'], async (_, signal, snapshots, revisions) => {
    const changes = snapshots.diagnostics
    if (!initialized || !changes || changes.after !== through) {
      if (!await load(signal, true)) return
    } else {
      const relevant = changes.records.filter(item => item.sessionId === props.sessionId && item.runId === props.runId)
      if (relevant.length) accept(mergeDiagnosticSummaries(records.value, relevant, props.sessionId, props.runId))
    }
    through = revisions.diagnostics
    initialized = true
  })
})
onScopeDispose(() => { unsubscribe?.(); cancel() })
const details = computed(() => open.value ? JSON.stringify({ runId: props.runId,
  synchronization: conversation.syncDiagnostics.value,
  agents: props.agents.map(({ task, number, depth, parentId }) => ({ id: task.id, number, depth, parentId, status: task.status, modelId: task.modelId, reasoningEffort: task.reasoningEffort })),
  events: props.events.filter(event => event.data.runId === props.runId),
}, null, 2) : '')
</script>

<template>
  <Modal :open="open" :title="t('traceDebugDetails')" centered :footer="null" :width="840" :style="{ maxWidth: 'calc(100vw - 32px)' }" destroy-on-hidden @cancel="open = false">
    <Tabs v-model:active-key="tab">
      <TabPane key="events" :tab="t('traceDebugEvents')" />
      <TabPane key="http" :tab="t('traceModelRequests')" />
    </Tabs>
    <pre v-if="tab === 'events'" class="trace-debug-body">{{ details }}</pre>
    <div v-else class="trace-model-diagnostics">
      <p class="trace-diagnostic-hint">{{ t('traceModelRequestsHint') }}</p>
      <div class="trace-diagnostic-controls">
        <label :for="selectId" class="visually-hidden">{{ t('traceSelectRequest') }}</label>
        <Select :id="selectId" :value="selectedId || undefined" :options="options" :aria-label="t('traceSelectRequest')" :placeholder="t('traceSelectRequest')" show-search option-filter-prop="label" @change="select" />
        <Button :loading="loading" :title="t('refresh')" :aria-label="t('refresh')" @click="load()"><ReloadOutlined /></Button>
        <Button :disabled="!record || loading" :title="t('traceCopyRecord')" :aria-label="t('traceCopyRecord')" @click="copy"><CopyOutlined /></Button>
      </div>
      <Alert v-if="error" type="error" :message="error" role="alert" />
      <p v-if="!loading && !records.length && !error" role="status">{{ t('traceNoModelRequests') }}</p>
      <div v-if="record" class="trace-model-record" :aria-busy="loading">
        <Alert v-if="record.truncated" type="warning" :message="t('traceCaptureTruncated')" show-icon />
        <Alert v-if="record.error" type="error" :message="record.error" />
        <h4>{{ t('traceParsing') }}</h4>
        <pre class="trace-model-body">{{ pretty({ id: record.id, provider: record.provider, protocol: record.protocol, model: record.model, purpose: record.purpose, taskId: record.taskId, turn: record.turn, durationMs: record.durationMs, finishReason: record.finishReason, parsing: record.parsing }) }}</pre>
        <section v-for="(exchange, index) in record.exchanges" :key="index" class="trace-http-exchange">
          <h4>{{ t('traceRequest') }} {{ index + 1 }}</h4>
          <pre class="trace-model-body">{{ exchange.method }} {{ exchange.url }}
{{ headers(exchange.requestHeaders) }}

{{ requestBody(exchange.request.content) }}</pre>
          <h4>{{ t('traceResponse') }} · HTTP {{ exchange.status || '—' }}</h4>
          <pre class="trace-model-body">{{ headers(exchange.responseHeaders) }}

{{ exchange.response.encoding === 'base64' ? '[base64]\n' : '' }}{{ exchange.response.content }}</pre>
          <details class="trace-http-timings"><summary>{{ t('traceReceiveTimings') }}</summary>
            <pre class="trace-model-body">{{ pretty({ startedMs: exchange.startedMs, headersMs: exchange.headersMs, durationMs: exchange.durationMs, requestBytes: exchange.request.bytes, responseBytes: exchange.response.bytes, requestTruncated: exchange.request.truncated, responseTruncated: exchange.response.truncated, eof: exchange.eof, readError: exchange.readError, error: exchange.error, timingsTruncated: exchange.timingsTruncated, chunks: exchange.chunks }) }}</pre>
          </details>
        </section>
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.trace-debug-body,.trace-model-body{margin:0;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:15px;line-height:1.6;color:var(--text-secondary)}.trace-debug-body,.trace-model-diagnostics{max-height:65dvh;overflow:auto}.trace-diagnostic-hint{color:var(--muted);margin:0 0 12px}.trace-diagnostic-controls{display:flex;gap:8px;margin-bottom:12px}.trace-diagnostic-controls :deep(.ant-select){flex:1;min-width:0}.trace-model-record h4{margin:16px 0 8px}.trace-model-body{padding:12px;background:var(--surface-subtle);border:1px solid var(--border);border-radius:8px;max-height:45dvh}.trace-http-timings{margin-top:12px}.trace-http-timings summary{cursor:pointer;color:var(--text-secondary);padding:4px 0}.trace-model-record :deep(.ant-alert){margin-top:12px}
</style>
