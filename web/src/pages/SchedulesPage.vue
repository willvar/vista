<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { VistaAPIError, type ScheduleRule, type ScheduleRun, type ScheduleSnapshot, type ScheduleSummary } from '@vista/client'
import { Alert, Button, Empty } from '../ui'
import WorkspaceModal from '../WorkspaceModal.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { useStateQuery } from '../composables/useStateQuery'

const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync, modelCatalog, backToWorkspace, pathInProfile } = useWorkspace()
const router = useRouter()
const { data, loading, error } = useStateQuery<ScheduleSnapshot>(client, stateSync, '/schedules', ['schedules'], Infinity, point => point.schedules)
const items = computed(() => data.value?.items ?? [])
const actionError = ref('')
const busy = ref(false)
const lifetime = new AbortController()
onScopeDispose(() => lifetime.abort())

function format(at: number, timeZone?: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short', timeZone }).format(at)
}
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const zones = computed(() => [...new Set([timezone, 'UTC', ...(Intl.supportedValuesOf?.('timeZone') ?? [])])])
const days = computed(() => Array.from({ length: 7 }, (_, day) => new Intl.DateTimeFormat(locale.value, { weekday: 'long', timeZone: 'UTC' }).format(Date.UTC(2024, 0, 7 + day))))
const models = computed(() => modelCatalog.value.models.filter(model => !model.disabled && model.kind !== 'embedding'))
function modelLabel(key: string) {
  const model = models.value.find(item => item.key === key)
  return model ? (model.name || model.modelName || model.id) : key
}
const runLabels: Record<ScheduleRun['state'], string> = {
  queued: 'scheduleQueued', running: 'running', waiting_reply: 'awaitingAnswer', succeeded: 'statusSucceeded',
  failed: 'executionFailed', interrupted: 'statusInterrupted', cancelled: 'scheduleStopped', missed: 'scheduleMissed',
}
function active(run?: ScheduleRun) { return run && ['queued', 'running', 'waiting_reply'].includes(run.state) }
function runLabel(run: ScheduleRun) { return t(run.reason === 'timeout' ? 'scheduleTimeout' : run.reason === 'model' ? 'scheduleModelUnavailable' : runLabels[run.state]) }
function problem(cause: unknown) {
  return cause instanceof VistaAPIError && cause.status === 409 ? t('scheduleConflict') : String(cause)
}

type Editor = { id: string; revision: number; name: string; prompt: string; modelKey: string; rule: ScheduleRule }
const editor = ref<Editor>()
const editorError = ref('')
const preview = ref<number>()
const previewInvalid = ref(false)
const rule = computed<ScheduleRule | undefined>(() => {
  const timing = editor.value?.rule
  if (!timing) return
  const { kind, timezone } = timing
  switch (kind) {
    case 'once': return { kind, timezone, at: timing.at || '' }
    case 'interval': return { kind, timezone, minutes: Number(timing.minutes) }
    case 'daily': return { kind, timezone, time: timing.time || '' }
    case 'weekly': return { kind, timezone, time: timing.time || '', weekday: Number(timing.weekday ?? 1) }
  }
})
watch(() => JSON.stringify(rule.value), (_, __, cleanup) => {
  preview.value = undefined; previewInvalid.value = false
  const timing = rule.value, id = editor.value?.id
  if (!timing || (timing.kind === 'once' && !timing.at)) return
  const owner = new AbortController()
  const timer = setTimeout(async () => {
    try {
      const response = await client.request<{ nextAt: number }>('/schedules/preview', {
        method: 'POST', body: JSON.stringify({ rule: timing, id }), signal: owner.signal,
      })
      if (!owner.signal.aborted) preview.value = response.nextAt
    } catch { if (!owner.signal.aborted) previewInvalid.value = true }
  }, 240)
  cleanup(() => { owner.abort(); clearTimeout(timer) })
})
const editorStale = computed(() => editor.value?.id && items.value.some(item => item.id === editor.value!.id && item.revision !== editor.value!.revision))
function create() {
  editorError.value = ''; actionError.value = ''
  editor.value = { id: '', revision: 0, name: '', prompt: '', modelKey: '', rule: { kind: 'daily', timezone, time: '09:00', minutes: 60, weekday: 1 } }
}
async function edit(item: ScheduleSummary) {
  busy.value = true; actionError.value = ''
  try {
    const current = await client.request<Editor>(`/schedules/${item.id}`, { signal: lifetime.signal })
    editorError.value = ''
    editor.value = { ...current, rule: { time: '09:00', minutes: 60, weekday: 1, ...current.rule, ...(current.rule.kind === 'weekly' ? { weekday: current.rule.weekday ?? 0 } : {}) } }
  } catch (cause) { if (!lifetime.signal.aborted) actionError.value = problem(cause) }
  finally { busy.value = false }
}
async function save() {
  const form = editor.value, timing = rule.value
  if (!form || !timing || busy.value || preview.value === undefined) return
  busy.value = true; editorError.value = ''
  try {
    const { id, name, prompt, modelKey, revision } = form
    await client.request(id ? `/schedules/${id}` : '/schedules', {
      method: id ? 'PUT' : 'POST', body: JSON.stringify({ name, prompt, modelKey, rule: timing, revision }), signal: lifetime.signal,
    })
    editor.value = undefined
  } catch (cause) { if (!lifetime.signal.aborted) editorError.value = problem(cause) }
  finally { busy.value = false }
}
async function act(item: ScheduleSummary, action: 'pause' | 'resume' | 'run-now' | 'stop') {
  busy.value = true; actionError.value = ''
  try {
    await client.request(`/schedules/${item.id}/${action}`, {
      method: 'POST', body: JSON.stringify(action === 'run-now' ? { requestId: Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('') } : action === 'stop' ? { occurrenceId: item.latest?.id } : { revision: item.revision }), signal: lifetime.signal,
    })
  } catch (cause) { if (!lifetime.signal.aborted) actionError.value = problem(cause) }
  finally { busy.value = false }
}

const historyId = ref('')
const historyItem = computed(() => items.value.find(item => item.id === historyId.value))
const history = shallowRef<ScheduleRun[]>([])
const before = ref(0), historyLoading = ref(false), historyError = ref('')
let historyOwner: AbortController | undefined
onScopeDispose(() => historyOwner?.abort())
function mergeLatest(rows: ScheduleRun[], latest?: ScheduleRun) {
  if (!latest) return rows
  if (rows.some(run => run.id === latest.id)) return rows.map(run => run.id === latest.id ? latest : run)
  return [latest, ...rows]
}
watch(() => historyItem.value?.latest, latest => { history.value = mergeLatest(history.value, latest) })
async function readHistory(older = false) {
  historyOwner?.abort()
  const owner = historyOwner = new AbortController()
  const id = historyId.value
  historyLoading.value = true; historyError.value = ''
  try {
    const result = await client.request<{ items: ScheduleRun[]; nextBefore: number }>(`/schedules/${id}/history${older ? `?before=${before.value}` : ''}`, { signal: owner.signal })
    if (!owner.signal.aborted && id === historyId.value) {
      const rows = older ? [...history.value, ...result.items.filter(run => !history.value.some(saved => saved.id === run.id))] : result.items
      history.value = mergeLatest(rows, historyItem.value?.latest)
      before.value = result.nextBefore
    }
  } catch (cause) { if (!owner.signal.aborted) historyError.value = problem(cause) }
  finally { if (historyOwner === owner) historyLoading.value = false }
}
function openHistory(item: ScheduleSummary) {
  historyId.value = item.id; history.value = []; before.value = 0; actionError.value = ''
  void readHistory()
}
function goBack() {
  if (editor.value) { editor.value = undefined; return }
  if (historyId.value) { historyOwner?.abort(); historyId.value = ''; return }
  backToWorkspace()
}
</script>

<template>
  <WorkspaceModal :open="true" :title="t('scheduledTasks')" page-class="schedules-page"
    :close-label="t('close')" :back-label="t('backToConversation')" width="min(820px, calc(100vw - 2rem))"
    @cancel="backToWorkspace" @back="backToWorkspace">
    <div class="schedules-content">
      <form v-if="editor" class="schedule-editor" @submit.prevent="save">
        <h2>{{ t(editor.id ? 'edit' : 'scheduleNew') }}</h2>
        <Alert v-if="editorError || editorStale" type="error" :message="editorError || t('scheduleConflict')" />
        <label>{{ t('scheduleName') }}<input v-model="editor.name" maxlength="200" required /></label>
        <label>{{ t('scheduleMessage') }}<textarea v-model="editor.prompt" rows="5" required /></label>
        <div class="schedule-fields">
          <label>{{ t('scheduleTiming') }}
            <select v-model="editor.rule.kind">
              <option value="once">{{ t('scheduleOnce') }}</option><option value="interval">{{ t('scheduleInterval') }}</option>
              <option value="daily">{{ t('scheduleDaily') }}</option><option value="weekly">{{ t('scheduleWeekly') }}</option>
            </select>
          </label>
          <label>{{ t('scheduleTimezone') }}<input v-model="editor.rule.timezone" list="schedule-zones" required />
            <datalist id="schedule-zones"><option v-for="zone in zones" :key="zone" :value="zone" /></datalist>
          </label>
          <label v-if="editor.rule.kind === 'once'">{{ t('scheduleTime') }}<input v-model="editor.rule.at" type="datetime-local" required /></label>
          <label v-else-if="editor.rule.kind === 'interval'">{{ t('scheduleMinutes') }}<input v-model.number="editor.rule.minutes" type="number" min="1" max="525600" required /></label>
          <template v-else>
            <label v-if="editor.rule.kind === 'weekly'">{{ t('scheduleWeekday') }}<select v-model.number="editor.rule.weekday">
              <option v-for="(day, index) in days" :key="index" :value="index">{{ day }}</option>
            </select></label>
            <label>{{ t('scheduleTime') }}<input v-model="editor.rule.time" type="time" required /></label>
          </template>
          <label>{{ t('model') }}<select v-model="editor.modelKey">
            <option value="">{{ t('scheduleDefaultModel') }}</option>
            <option v-if="editor.modelKey && !models.some(item => item.key === editor!.modelKey)" :value="editor.modelKey">{{ editor.modelKey }}</option>
            <option v-for="item in models" :key="item.key" :value="item.key">{{ modelLabel(item.key) }}</option>
          </select></label>
        </div>
        <p role="status" class="schedule-summary">
          {{ previewInvalid ? t('scheduleInvalidTiming') : preview ? t('scheduleNext', { time: format(preview, rule?.timezone) }) : '' }}
        </p>
        <div class="schedule-actions">
          <Button @click="goBack">{{ t('cancel') }}</Button>
          <Button variant="primary" type="submit" :disabled="busy || !!editorStale || preview === undefined || !editor.name.trim() || !editor.prompt.trim()">{{ t(editor.id ? 'saveChanges' : 'scheduleEnable') }}</Button>
        </div>
      </form>
      <template v-else-if="historyId">
        <div class="schedule-heading">
          <h2>{{ historyItem?.name }} · {{ t('scheduleHistory') }}</h2>
          <Button @click="goBack">{{ t('scheduledTasks') }}</Button>
        </div>
        <Alert v-if="historyError" type="error" :message="historyError" />
        <Empty v-if="!historyLoading && !history.length" :description="t('scheduleNoHistory')" />
        <ul class="schedule-list">
          <li v-for="run in history" :key="run.id" class="schedule-row">
            <div><strong>{{ format(run.dueAt, historyItem?.rule.timezone) }}</strong><p>{{ runLabel(run) }}</p></div>
            <Button v-if="run.sessionId" @click="router.push(pathInProfile(`/sessions/${run.sessionId}`))">{{ t('open') }}</Button>
          </li>
        </ul>
        <Button v-if="before" :disabled="historyLoading" @click="readHistory(true)">{{ t('scheduleOlder') }}</Button>
      </template>
      <template v-else>
        <div class="schedule-heading">
          <p>{{ t('scheduleServiceHint') }}</p>
          <Button variant="primary" :disabled="busy" @click="create">{{ t('scheduleNew') }}</Button>
        </div>
        <Alert v-if="error || actionError" type="error" :message="error || actionError" />
        <Empty v-if="!loading && !items.length" :description="t('scheduleEmpty')" />
        <ul class="schedule-list">
          <li v-for="item in items" :key="item.id" :data-schedule-id="item.id" class="schedule-row">
            <div>
              <h2>{{ item.name }}</h2>
              <p class="schedule-summary">{{ !item.enabled ? t(item.blocked === 'model' ? 'scheduleModelUnavailable' : 'schedulePaused') : item.nextAt ? `${t('scheduleNext', { time: format(item.nextAt, item.rule.timezone) })} · ${item.rule.timezone}` : t('scheduleNoNext') }}</p>
              <p v-if="item.latest">{{ runLabel(item.latest) }}</p>
            </div>
            <div class="schedule-actions">
              <Button :disabled="busy" @click="edit(item)">{{ t('edit') }}</Button>
              <Button :disabled="busy" @click="openHistory(item)">{{ t('scheduleHistory') }}</Button>
              <Button v-if="item.enabled" :disabled="busy" @click="act(item, 'pause')">{{ t('schedulePause') }}</Button>
              <Button v-else :disabled="busy || !!active(item.latest)" @click="act(item, 'resume')">{{ t('resumePending') }}</Button>
              <Button v-if="active(item.latest)" :disabled="busy" @click="act(item, 'stop')">{{ t('scheduleStop') }}</Button>
              <Button v-else :disabled="busy" @click="act(item, 'run-now')">{{ t('scheduleRunNow') }}</Button>
            </div>
          </li>
        </ul>
      </template>
    </div>
  </WorkspaceModal>
</template>

<style scoped>
.schedules-content { overflow: auto; min-height: 0; max-height: calc(100% - 72px); padding: 1rem clamp(.75rem, 3vw, 1.5rem); }
.schedule-heading, .schedule-row { display: flex; align-items: flex-start; justify-content: space-between; flex-wrap: wrap; gap: .75rem; margin-block-end: .75rem; }
.schedule-heading > p { flex: 1 1 17rem; color: var(--muted); margin: 0; }
.schedule-row { padding: .9rem 0; border-block-end: 1px solid var(--border-subtle); }
.schedule-editor h2, .schedule-heading h2, .schedule-row h2 { margin: 0; font-size: 1.06rem; overflow-wrap: anywhere; }
.schedule-summary { color: var(--muted); }
.schedule-row > div { min-width: 0; }
.schedule-row p { margin: .25rem 0; overflow-wrap: anywhere; }
.schedule-list { padding: 0; margin: 0; list-style: none; }
.schedule-editor, .schedule-editor label { display: grid; gap: .55rem; min-width: 0; }
.schedule-fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 12rem), 1fr)); gap: .75rem; }
.schedule-editor input, .schedule-editor select, .schedule-editor textarea { width: 100%; min-width: 0; padding: .55rem .65rem; min-height: 2.5rem; border: 1px solid var(--border); border-radius: .45rem; background: var(--surface); color: var(--text); font: inherit; }
.schedule-editor textarea { resize: vertical; }
.schedule-editor :is(input, select, textarea):focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.schedule-actions { display: flex; flex-wrap: wrap; gap: .4rem; }
@media (pointer: coarse) { .schedule-actions :deep(button), .schedule-heading :deep(button) { min-height: 44px; } }
</style>
