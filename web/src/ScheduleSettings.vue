<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { FieldInput, FieldLabel, FieldRoot, FieldTextarea } from '@ark-ui/vue/field'
import { IconPlus } from '@tabler/icons-vue'
import type { RouteLocationNormalized } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { VistaAPIError, type ScheduleRule, type ScheduleRun, type ScheduleSnapshot, type ScheduleSummary } from '@vista/client'
import { Alert, Button, Empty, InputNumber, Modal, Select } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import { useStateQuery } from './composables/useStateQuery'
import { useRouteLayer } from './composables/routeLayers'
import { modelPickerOptions } from './composables/usePresentation'
import { profileIdPattern, routeProfileId } from './client/profileScope'

const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync, modelCatalog, modelLabel, settingProviderName, profileId, pathInProfile, router } = useWorkspace()
const inScope = (route: RouteLocationNormalized) => route.name === 'settings-schedules' && routeProfileId(route.params) === profileId
const editorLayer = useRouteLayer('schedule-editor', inScope)
const historyLayer = useRouteLayer('schedule-history', inScope)
const validId = new RegExp(`^${profileIdPattern}$`)
const { data, loading, error } = useStateQuery<ScheduleSnapshot>(client, stateSync, '/schedules', ['schedules'], Infinity, point => point.schedules)
const items = computed(() => data.value?.items ?? [])
const actionError = ref('')
const busy = ref(false)
const lifetime = new AbortController()
onScopeDispose(() => lifetime.abort())

const deviceZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
function format(at: number, timing?: ScheduleRule) {
  const timeZone = timing?.kind === 'interval' ? undefined : timing?.timezone
  const options: Intl.DateTimeFormatOptions = timeZone && timeZone !== deviceZone()
    ? { year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZoneName: 'short', timeZone }
    : { dateStyle: 'medium', timeStyle: 'short', timeZone }
  return new Intl.DateTimeFormat(locale.value, options).format(at)
}
const kindOptions = computed(() => [
  { value: 'daily', label: t('scheduleDaily') }, { value: 'weekly', label: t('scheduleWeekly') },
  { value: 'interval', label: t('scheduleInterval') }, { value: 'once', label: t('scheduleOnce') },
])
const days = computed(() => Array.from({ length: 7 }, (_, day) => ({
  value: day, label: new Intl.DateTimeFormat(locale.value, { weekday: 'long', timeZone: 'UTC' }).format(Date.UTC(2024, 0, 7 + day)),
})))
const models = computed(() => modelCatalog.value.models.filter(model => !model.disabled && model.kind !== 'embedding'
  && (modelCatalog.value.showUnrecognizedModels || model.recognized !== false)))
const modelOptions = computed(() => {
  const value = editor.value?.modelKey
  const unavailable = value && !models.value.some(model => model.key === value) ? [{ value, label: modelCatalog.value.models.find(model => model.key === value)?.modelName || value }] : []
  return [{ value: '', label: t('scheduleDefaultModel') }, ...unavailable,
    ...modelPickerOptions(models.value, locale.value, settingProviderName, model => modelLabel(model))]
})
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
const editorTitle = ref('scheduleNew')
const editorError = ref('')
const editorLoading = ref(false)
const saving = ref(false)
const preview = ref<number>()
const previewError = ref('')
const rule = computed<ScheduleRule | undefined>(() => {
  if (editorLayer.current.value === undefined) return
  const timing = editor.value?.rule
  if (!timing) return
  const { kind, timezone } = timing
  switch (kind) {
    case 'once': return { kind, timezone, at: timing.at || '' }
    case 'interval': return { kind, timezone, minutes: timing.minutes }
    case 'daily': return { kind, timezone, time: timing.time || '' }
    case 'weekly': return { kind, timezone, time: timing.time || '', weekday: timing.weekday ?? 1 }
  }
})
const editorStale = computed(() => !!editor.value?.id && items.value.some(item => item.id === editor.value?.id && item.revision !== editor.value?.revision))
const canSave = computed(() => !saving.value && !editorStale.value && !!editor.value?.name.trim() && !!editor.value.prompt.trim() && preview.value !== undefined)
watch(editorLayer.current, async (id, _, cleanup) => {
  if (id === undefined) return // Keep content stable during the closing animation.
  editor.value = undefined
  editorTitle.value = id ? 'edit' : 'scheduleNew'
  editorError.value = ''
  editorLoading.value = false
  actionError.value = ''
  const defaults = { time: '09:00', minutes: 60, weekday: 1 }
  if (!id) {
    editor.value = { id, revision: 0, name: '', prompt: '', modelKey: '', rule: { kind: 'daily', timezone: deviceZone(), ...defaults } }
    return
  }
  if (!validId.test(id)) { editorLayer.drop(); return }
  const owner = new AbortController()
  cleanup(() => owner.abort())
  editorLoading.value = true
  try {
    // Editing preserves the original timezone even on a device in another zone.
    const saved = await client.request<Editor>(`/schedules/${id}`, { signal: owner.signal })
    if (!owner.signal.aborted) editor.value = { ...saved, rule: { ...defaults, ...saved.rule, weekday: saved.rule.kind === 'weekly' ? saved.rule.weekday ?? 0 : 1 } }
  } catch (cause) { if (!owner.signal.aborted) editorError.value = problem(cause) }
  finally { if (!owner.signal.aborted) editorLoading.value = false }
}, { immediate: true })
watch(() => JSON.stringify(rule.value), (_, __, cleanup) => {
  if (editorLayer.current.value === undefined) return
  preview.value = undefined; previewError.value = ''
  const timing = rule.value, id = editor.value?.id
  if (!timing || (timing.kind === 'once' ? !timing.at : timing.kind === 'interval' ? !timing.minutes : !timing.time)) return
  const owner = new AbortController()
  const timer = setTimeout(async () => {
    try {
      const response = await client.request<{ nextAt: number }>('/schedules/preview', {
        method: 'POST', body: JSON.stringify({ rule: timing, id }), signal: owner.signal,
      })
      if (!owner.signal.aborted) preview.value = response.nextAt
    } catch (cause) {
      if (!owner.signal.aborted) previewError.value = cause instanceof VistaAPIError && cause.status === 400 ? t('scheduleInvalidTiming') : problem(cause)
    }
  }, 240)
  cleanup(() => { owner.abort(); clearTimeout(timer) })
})
editorLayer.onLeave(() => !saving.value)
async function save() {
  const form = editor.value, timing = rule.value
  if (!form || !timing || !canSave.value) return
  const close = editorLayer.captureClose()
  saving.value = true; editorError.value = ''
  try {
    const { id, name, prompt, modelKey, revision } = form
    await client.request(id ? `/schedules/${id}` : '/schedules', {
      method: id ? 'PUT' : 'POST', body: JSON.stringify({ name, prompt, modelKey, rule: timing, revision }), signal: lifetime.signal,
    })
  } catch (cause) {
    if (!lifetime.signal.aborted) editorError.value = problem(cause)
    return
  } finally { saving.value = false }
  close()
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

const historyId = historyLayer.current
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
  if (!id) return
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
watch(historyId, id => {
  historyOwner?.abort()
  if (id === undefined) return
  if (!validId.test(id)) { historyLayer.drop(); return }
  history.value = []; before.value = 0; actionError.value = ''
  void readHistory()
}, { immediate: true })
</script>

<template>
  <section class="settings-content schedules-content">
    <template v-if="historyId">
      <div class="schedule-heading">
        <h2>{{ historyItem?.name }} · {{ t('scheduleHistory') }}</h2>
        <Button @click="historyLayer.close">{{ t('scheduledTasks') }}</Button>
      </div>
      <Alert v-if="historyError" type="error" :message="historyError" />
      <Empty v-if="!historyLoading && !history.length" :description="t('scheduleNoHistory')" />
      <ul class="schedule-list">
        <li v-for="run in history" :key="run.id" class="schedule-row">
          <div><strong>{{ format(run.dueAt, historyItem?.rule) }}</strong><p>{{ runLabel(run) }}</p></div>
          <Button v-if="run.sessionId" @click="router.push(pathInProfile(`/sessions/${run.sessionId}`))">{{ t('open') }}</Button>
        </li>
      </ul>
      <Button v-if="before" :disabled="historyLoading" @click="readHistory(true)">{{ t('scheduleOlder') }}</Button>
    </template>
    <template v-else>
      <div class="settings-toolbar">
        <p>{{ t('scheduleServiceHint') }}</p>
        <Button variant="primary" :disabled="busy" @click="editorLayer.open()">
          <template #icon><IconPlus aria-hidden="true" /></template>{{ t('scheduleNew') }}
        </Button>
      </div>
      <Alert v-if="error || actionError" type="error" :message="error || actionError" />
      <Empty v-if="!loading && !items.length" :description="t('scheduleEmpty')" />
      <ul class="schedule-list">
        <li v-for="item in items" :key="item.id" :data-schedule-id="item.id" class="schedule-row">
          <div>
            <h2>{{ item.name }}</h2>
            <p class="schedule-summary">{{ !item.enabled ? t(item.blocked === 'model' ? 'scheduleModelUnavailable' : 'schedulePaused') : item.nextAt ? t('scheduleNext', { time: format(item.nextAt, item.rule) }) : t('scheduleNoNext') }}</p>
            <p v-if="item.latest">{{ runLabel(item.latest) }}</p>
          </div>
          <div class="schedule-actions">
            <Button :disabled="busy" @click="editorLayer.open(item.id)">{{ t('edit') }}</Button>
            <Button :disabled="busy" @click="historyLayer.open(item.id)">{{ t('scheduleHistory') }}</Button>
            <Button v-if="item.enabled" :disabled="busy" @click="act(item, 'pause')">{{ t('schedulePause') }}</Button>
            <Button v-else :disabled="busy || !!active(item.latest)" @click="act(item, 'resume')">{{ t('resumePending') }}</Button>
            <Button v-if="active(item.latest)" :disabled="busy" @click="act(item, 'stop')">{{ t('scheduleStop') }}</Button>
            <Button v-else :disabled="busy" @click="act(item, 'run-now')">{{ t('scheduleRunNow') }}</Button>
          </div>
        </li>
      </ul>
    </template>
  </section>
  <Modal :open="editorLayer.current.value !== undefined" class="schedule-editor-modal" :z-index="1100" :title="t(editorTitle)"
    :mask-closable="false" :keyboard="!saving" :close-disabled="saving" destroy-on-hidden @cancel="editorLayer.close">
    <Alert v-if="editorError || editorStale" type="error" :message="editorError || t('scheduleConflict')" />
    <p v-if="editorLoading" role="status">{{ t('uiLoading') }}</p>
    <form v-if="editor" id="schedule-form" class="schedule-editor" @submit.prevent="save">
      <FieldRoot id="schedule-name" class="schedule-field" required :disabled="saving">
        <FieldLabel>{{ t('scheduleName') }}</FieldLabel>
        <FieldInput v-model="editor.name" class="ui-input" maxlength="200" />
      </FieldRoot>
      <FieldRoot id="schedule-message" class="schedule-field" required :disabled="saving">
        <FieldLabel>{{ t('scheduleMessage') }}</FieldLabel>
        <FieldTextarea v-model="editor.prompt" class="ui-input ui-textarea" :rows="3" />
      </FieldRoot>
      <div class="schedule-fields">
        <FieldRoot id="schedule-kind" class="schedule-field" :disabled="saving">
          <FieldLabel>{{ t('scheduleTiming') }}</FieldLabel>
          <Select id="schedule-kind" :value="editor.rule.kind" :options="kindOptions" :disabled="saving"
            @update:value="value => { if (editor && value) editor.rule.kind = value as ScheduleRule['kind'] }" />
        </FieldRoot>
        <FieldRoot v-if="editor.rule.kind === 'interval'" id="schedule-minutes" class="schedule-field" :disabled="saving">
          <FieldLabel>{{ t('scheduleMinutes') }}</FieldLabel>
          <InputNumber id="schedule-minutes" :value="editor.rule.minutes" :min="1" :max="525600" :precision="0" :disabled="saving"
            @update:value="value => { if (editor) editor.rule.minutes = value ?? undefined }" />
        </FieldRoot>
        <template v-else>
          <FieldRoot v-if="editor.rule.kind === 'weekly'" id="schedule-day" class="schedule-field" :disabled="saving">
            <FieldLabel>{{ t('scheduleWeekday') }}</FieldLabel>
            <Select id="schedule-day" :value="editor.rule.weekday" :options="days" :disabled="saving"
              @update:value="value => { if (editor && value !== undefined) editor.rule.weekday = Number(value) }" />
          </FieldRoot>
          <FieldRoot id="schedule-time" class="schedule-field" required :disabled="saving">
            <FieldLabel>{{ t('scheduleTime') }}</FieldLabel>
            <FieldInput v-if="editor.rule.kind === 'once'" v-model="editor.rule.at" type="datetime-local" class="ui-input" />
            <FieldInput v-else v-model="editor.rule.time" type="time" class="ui-input" />
          </FieldRoot>
        </template>
      </div>
      <FieldRoot id="schedule-model" class="schedule-field" :disabled="saving">
        <FieldLabel>{{ t('model') }}</FieldLabel>
        <Select id="schedule-model" :value="editor.modelKey" :options="modelOptions" show-search option-filter-prop="searchText" option-label-prop="fullLabel"
          :disabled="saving" @update:value="value => { if (editor) editor.modelKey = typeof value === 'string' ? value : '' }" />
      </FieldRoot>
      <p role="status" class="schedule-preview" :class="{ 'schedule-preview-error': previewError }">
        {{ previewError || (preview ? t('scheduleNext', { time: format(preview, editor.rule) }) : preview === 0 ? t('scheduleNoNext') : '') }}
      </p>
    </form>
    <template #footer>
      <Button :disabled="saving" @click="editorLayer.close">{{ t('cancel') }}</Button>
      <Button variant="primary" type="submit" form="schedule-form" :loading="saving" :disabled="!canSave">
        {{ t(editorTitle === 'edit' ? 'saveChanges' : 'scheduleEnable') }}
      </Button>
    </template>
  </Modal>
</template>

<style scoped>
.schedule-heading, .schedule-row { display: flex; align-items: flex-start; justify-content: space-between; flex-wrap: wrap; gap: .75rem; }
.schedule-row { padding-block: .85rem; border-block-end: 1px solid var(--border-subtle); }
.schedule-heading h2, .schedule-row h2 { margin: 0; font-size: 1.06rem; overflow-wrap: anywhere; }
.schedule-summary { color: var(--muted); }
.schedule-row > div { min-width: 0; }
.schedule-row p { margin: .25rem 0; overflow-wrap: anywhere; }
.schedule-list { padding: 0; margin: .75rem 0; list-style: none; }
.schedule-actions { display: flex; flex-wrap: wrap; gap: .4rem; }
.schedule-editor { display: grid; gap: .85rem; }
.schedule-field { display: grid; gap: .35rem; min-width: 0; }
.schedule-fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 9rem), 1fr)); gap: .65rem; }
.schedule-field :deep(.ui-input-number), .schedule-field :deep(.ui-select) { width: 100%; }
.schedule-preview { min-height: 1.4em; margin: 0; color: var(--muted); }
.schedule-preview-error { color: var(--danger); }
:global(.schedule-editor-modal .ui-modal-container) { display: flex; flex-direction: column; max-height: calc(100dvh - 32px); }
:global(.schedule-editor-modal .ui-modal-body) { min-height: 0; overflow: auto; overscroll-behavior: contain; }
:global(.schedule-editor-modal .ui-modal-footer) { flex: none; }
@media (pointer: coarse) {
  .schedule-actions :deep(button), .schedule-heading :deep(button), .settings-toolbar :deep(button),
  .schedule-field .ui-input, .schedule-field :deep(.ui-select-selector), .schedule-field :deep(.ui-input-number) { min-height: 44px; }
}
</style>
