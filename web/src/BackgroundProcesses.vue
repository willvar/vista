<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { Button, Modal, Select } from 'antdv-next'
import { CodeOutlined, StopOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import { useWorkspace } from './composables/useWorkspace'
import { useEscapeDismiss } from './composables/useEscape'
import type { Job } from './client/types'
import { jobActive, jobStatusKey } from './state/jobs'
import BackgroundProcessOutput from './BackgroundProcessOutput.vue'
import { formatDateTime } from './ui/datetime.ts'

const props = defineProps<{ sessionId: string; jobs: Job[] }>()
const { client } = useWorkspace()
const { t, locale } = useI18n()
const visible = ref(false)
const selectedId = ref('')
const detail = ref<Job | null>(null)
const error = ref('')
const loading = ref(false)
const stopping = ref(new Set<string>())
const actions = new Set<AbortController>()
const now = ref(Date.now())
let controller: AbortController | undefined
let clock: ReturnType<typeof setInterval> | undefined
let generation = 0
useEscapeDismiss(visible)
const ordered = computed(() => {
  const items = [...props.jobs]
  if (detail.value && !items.some(item => item.id === detail.value!.id)) items.push(detail.value)
  return items.sort((a, b) => Number(jobActive(b)) - Number(jobActive(a)) || (b.createdAt || '').localeCompare(a.createdAt || ''))
})
const job = computed(() => {
  const current = props.jobs.find(item => item.id === selectedId.value)
  if (current && !jobActive(current)) return current
  return detail.value?.id === selectedId.value ? detail.value : current
})
const options = computed(() => ordered.value.map(item => ({ value: item.id, label: `${t(jobStatusKey(item))} · ${item.command}` })))
function time(value?: string) { return value ? formatDateTime(value, locale.value) : '—' }
function duration(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(seconds / 3600).toString().padStart(2, '0')}:${Math.floor(seconds / 60 % 60).toString().padStart(2, '0')}:${(seconds % 60).toString().padStart(2, '0')}`
}
const elapsed = computed(() => job.value ? duration((job.value.finishedAt ? Date.parse(job.value.finishedAt) : now.value) - Date.parse(job.value.startedAt || job.value.createdAt || new Date(now.value).toISOString())) : '—')
const remaining = computed(() => job.value?.expiresAt ? duration(Date.parse(job.value.expiresAt) - now.value) : '—')
const reason = computed(() => {
  const value = job.value?.terminationReason
  if (value === 'worker_closed') return t('processWorkerClosed')
  if (value === 'output_error') return t('processOutputError')
  return job.value ? t(jobStatusKey(job.value)) : ''
})
function cancelRequests() { generation++; controller?.abort() }
async function refresh() {
  if (!visible.value || !selectedId.value) return
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  const revision = ++generation
  const id = selectedId.value
  loading.value = !job.value
  try {
    const value = await client.request<Job>(`/sessions/${encodeURIComponent(props.sessionId)}/jobs/${encodeURIComponent(id)}`, { signal })
    if (signal.aborted || revision !== generation) return
    detail.value = value
    if (!jobActive(value)) stopping.value.delete(id)
  } catch (cause) {
    if (!signal.aborted && revision === generation) error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    if (revision === generation) {
      loading.value = false
    }
  }
}
function open(id?: string) {
  selectedId.value = id || ordered.value[0]?.id || ''
  visible.value = true
}
defineExpose({ open })
watch([visible, selectedId], () => {
  cancelRequests()
  error.value = ''
  if (detail.value?.id !== selectedId.value) detail.value = null
  clearInterval(clock)
  if (!visible.value) {
    for (const action of actions) action.abort()
    actions.clear()
    stopping.value.clear()
  }
  if (visible.value) {
    now.value = Date.now()
    clock = setInterval(() => { now.value = Date.now() }, 1000)
    void refresh()
  }
})
watch(() => props.jobs, jobs => {
  const latest = jobs.find(item => item.id === selectedId.value)
  if (visible.value && latest) detail.value = latest
})
watch(() => [props.jobs, job.value], () => { for (const item of [...props.jobs, ...(job.value ? [job.value] : [])]) if (!jobActive(item)) stopping.value.delete(item.id) })
async function stop() {
  const current = job.value
  if (!current || !jobActive(current) || stopping.value.has(current.id)) return
  stopping.value.add(current.id)
  error.value = ''
  const action = new AbortController()
  actions.add(action)
  try {
    await client.request(`/sessions/${encodeURIComponent(props.sessionId)}/jobs/${encodeURIComponent(current.id)}/stop`, { method: 'POST', signal: action.signal })
    if (!action.signal.aborted && selectedId.value === current.id) void refresh()
  } catch (cause) {
    stopping.value.delete(current.id)
    if (!action.signal.aborted && selectedId.value === current.id) error.value = cause instanceof Error ? cause.message : String(cause)
  } finally { actions.delete(action) }
}
onUnmounted(() => { cancelRequests(); clearInterval(clock); for (const action of actions) action.abort() })
</script>

<template>
  <Modal v-model:open="visible" centered width="min(960px, calc(100vw - 2rem))" :footer="null" :title="t('backgroundProcesses')" root-class="background-process-modal" :destroy-on-hidden="true">
    <div v-if="visible" class="background-process-monitor">
      <Select v-if="ordered.length > 1" v-model:value="selectedId" class="process-select" :options="options" :aria-label="t('selectProcess')" :show-search="true" option-filter-prop="label" />
      <p v-if="error" class="process-error" role="alert">{{ error }} <Button type="link" size="small" @click="refresh">{{ t('retry') }}</Button></p>
      <template v-if="job">
        <div class="process-heading"><span class="process-state" :class="{ active: jobActive(job), failed: job.status === 'failed' }"><CodeOutlined aria-hidden="true" />{{ t(jobStatusKey(job)) }}</span><Button v-if="jobActive(job)" size="small" danger :aria-label="t(stopping.has(job.id) ? 'processStopping' : 'stopProcess')" :loading="stopping.has(job.id)" :disabled="stopping.has(job.id)" @click="stop"><StopOutlined v-if="!stopping.has(job.id)" aria-hidden="true" />{{ t(stopping.has(job.id) ? 'processStopping' : 'stopProcess') }}</Button></div>
        <div class="process-info">
        <pre class="process-command">{{ job.command }}</pre>
        <dl class="process-details">
          <div class="process-directory"><dt>{{ t('processDirectory') }}</dt><dd>{{ job.directory }}</dd></div>
          <div><dt>{{ t('processStartedAt') }}</dt><dd>{{ time(job.startedAt || job.createdAt) }}</dd></div>
          <div><dt>{{ t('processElapsed') }}</dt><dd>{{ elapsed }}</dd></div>
          <div v-if="job.expiresAt"><dt>{{ t('processExpiresAt') }}</dt><dd>{{ time(job.expiresAt) }}</dd></div>
          <div v-if="jobActive(job) && job.expiresAt"><dt>{{ t('processRemaining') }}</dt><dd>{{ remaining }}</dd></div>
          <template v-if="!jobActive(job)"><div><dt>{{ t('processFinishedAt') }}</dt><dd>{{ time(job.finishedAt) }}</dd></div><div><dt>{{ t('processExitReason') }}</dt><dd>{{ reason }}<template v-if="job.signal"> · {{ job.signal }}</template><template v-else-if="job.complete"> · {{ t('processExitCode', { code: job.exitCode }) }}</template></dd></div></template>
        </dl>
        <p v-if="job.error && !['stopped', 'timeout', 'worker_closed'].includes(job.terminationReason || '')" class="process-error">{{ job.error }}</p>
        </div>
        <BackgroundProcessOutput :key="job.id" :session-id="sessionId" :job="job" />
      </template>
      <p v-else class="process-empty">{{ t(loading ? 'outputLoading' : 'noBackgroundProcesses') }}</p>
    </div>
  </Modal>
</template>

<style scoped>
.background-process-monitor{display:flex;flex-direction:column;gap:.75rem;height:min(720px,calc(100dvh - 10rem));min-height:0;color:var(--text)}
.process-select{width:100%;flex:none}
.process-heading{display:flex;align-items:center;justify-content:space-between;gap:.75rem;flex:none}
.process-state{display:flex;align-items:center;gap:.45rem;font-size:13px;color:var(--muted)}
.process-state.active{color:var(--accent)}.process-state.failed{color:var(--danger)}
.process-info{display:flex;flex-direction:column;gap:.6rem;flex:0 1 auto;min-height:2rem;max-height:17rem;overflow:auto}
.process-command{margin:0;padding:.6rem .75rem;background:var(--surface-subtle);border-radius:8px;white-space:pre-wrap;overflow:auto;overflow-wrap:anywhere;max-height:5rem;flex:none;font:13px/1.6 var(--font-mono,monospace)}
.process-details{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.4rem 1.5rem;margin:0;flex:none;font-size:12px;line-height:1.6;max-height:10rem;overflow:auto}
.process-details>div{display:flex;gap:.6rem;min-width:0}
.process-details dt{color:var(--muted);flex:none}.process-details dd{margin:0;min-width:0;overflow-wrap:anywhere;font-variant-numeric:tabular-nums}
.process-directory{grid-column:1/-1}
.process-error{margin:0;color:var(--danger);font-size:12px;white-space:pre-wrap;overflow-wrap:anywhere;max-height:4.5rem;overflow:auto;flex:none}
.process-empty{color:var(--muted)}
@media(max-width:600px){.background-process-monitor{height:calc(100dvh - 8rem);gap:.5rem}.process-details{grid-template-columns:1fr;gap:.2rem;max-height:11rem}.process-command{max-height:4rem}.process-heading .ant-btn{min-height:36px}}
@media(max-height:520px){.background-process-monitor{height:calc(100dvh - 6rem);gap:.35rem}.process-info{min-height:1rem;max-height:5rem;gap:.35rem}.process-details{grid-template-columns:repeat(2,minmax(0,1fr));max-height:none;gap:0 .75rem}.process-command{max-height:2.5rem;padding:.2rem .5rem}.process-heading .ant-btn{height:28px;min-height:28px}}
</style>
