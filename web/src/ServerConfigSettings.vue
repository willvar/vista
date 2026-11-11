<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ControlElement } from '@jsonforms/core'
import { Alert as VAlert, Button as VButton, Modal as VModal } from './ui'
import ConfirmPopover from './ConfirmPopover.vue'
import SchemaForm from './SchemaForm.vue'
import { VistaAPIError } from './client/vista'
import type { ServerConfig } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync, serverStatus } = useWorkspace()
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const settings = ref<ServerConfig>()
const draft = ref<Record<string, unknown>>({}), base = ref<Record<string, unknown>>()
const baseRevision = ref('')
const loading = ref(true), saving = ref(false), error = ref(''), saved = ref(false), discardOpen = ref(false), confirmOpen = ref(false)
const editorValid = ref(false), editorDirty = ref(false), editorEpoch = ref(0)
const phase = ref<'editing' | 'waiting' | 'ready' | 'unconfirmed'>('editing')
const targetAddress = ref('')
const lifetime = new AbortController()
let readGeneration = 0, probeGeneration = 0, probeTimer: ReturnType<typeof setTimeout> | undefined, probeDeadline: ReturnType<typeof setTimeout> | undefined, probeImage: HTMLImageElement | undefined
const uischema = computed<ControlElement>(() => ({ type: 'Control', scope: '#/properties/port', label: t('serverConfigPort') }))
const dirty = computed(() => !!base.value && (editorDirty.value || JSON.stringify(draft.value) !== JSON.stringify(base.value)))
const conflict = computed(() => !!settings.value && !!baseRevision.value && settings.value.revision !== baseRevision.value)
const pending = computed(() => settings.value?.source === 'file' && !!settings.value.runningPort && settings.value.config.port !== settings.value.runningPort)
const portChanges = computed(() => !!settings.value?.runningPort && draft.value.port !== settings.value.runningPort)
const canSave = computed(() => !!settings.value && (dirty.value || pending.value) && editorValid.value && !conflict.value && !saving.value && !loading.value && phase.value === 'editing')
function resetDraft(value: ServerConfig) { base.value = { ...value.config }; baseRevision.value = value.revision; draft.value = { ...value.config }; editorEpoch.value++ }
function receive(value: ServerConfig, reset = false) {
  const [epoch, revision] = value.version.split(':'), [currentEpoch, currentRevision] = settings.value?.version.split(':') ?? []
  if (epoch === currentEpoch && Number(revision) < Number(currentRevision)) return
  readGeneration++
  if (reset || !base.value || !dirty.value) resetDraft(value)
  settings.value = value; loading.value = false
}
async function readConfiguration(signal?: AbortSignal) {
  if (phase.value !== 'editing') return
  const generation = ++readGeneration, combined = signal ? AbortSignal.any([signal, lifetime.signal]) : lifetime.signal
  loading.value = true; error.value = ''
  try { const value = await client.request<ServerConfig>('/server-config', { signal: combined }); if (!combined.aborted && generation === readGeneration) receive(value) }
  catch (cause) { if (!combined.aborted && generation === readGeneration) error.value = cause instanceof Error ? cause.message : String(cause) }
  finally { if (generation === readGeneration) loading.value = false }
}
const unsubscribe = stateSync.subscribe(['server-config'], async (_, signal, point) => { if (phase.value !== 'editing') return; if (point.serverConfig) receive(point.serverConfig); else await readConfiguration(signal) })
function clearProbe() { clearTimeout(probeTimer); clearTimeout(probeDeadline); probeTimer = undefined; probeDeadline = undefined; if (probeImage) { probeImage.onload = null; probeImage.onerror = null; probeImage = undefined } }
onUnmounted(() => { unsubscribe(); lifetime.abort(); readGeneration++; probeGeneration++; clearProbe(); emit('modal-open', false) })
function discard() { if (settings.value && !saving.value) { resetDraft(settings.value); saved.value = false; error.value = ''; discardOpen.value = false } }
function showConfirm() { confirmOpen.value = true; emit('modal-open', true) }
function closeConfirm() { if (saving.value) return; confirmOpen.value = false; emit('modal-open', false) }
function probeTarget() {
  clearProbe(); const generation = ++probeGeneration, deadline = Date.now() + 45_000
  const attempt = () => {
    if (lifetime.signal.aborted || generation !== probeGeneration) return
    clearTimeout(probeDeadline)
    const image = probeImage = new Image()
    image.crossOrigin = 'anonymous'
    image.onload = () => { if (generation === probeGeneration && !lifetime.signal.aborted) { clearProbe(); phase.value = 'ready'; error.value = '' } }
    image.onerror = () => { if (generation !== probeGeneration || lifetime.signal.aborted) return; if (Date.now() >= deadline) { clearProbe(); phase.value = 'unconfirmed' } else probeTimer = setTimeout(attempt, 500) }
    image.src = `${targetAddress.value}/api/port-ready.svg?attempt=${Date.now()}`
    probeDeadline = setTimeout(() => { if (generation === probeGeneration && !lifetime.signal.aborted && phase.value !== 'ready') { clearProbe(); phase.value = 'unconfirmed' } }, Math.max(1, deadline - Date.now()))
  }
  phase.value = 'waiting'; attempt()
}
function localError(cause: unknown) {
  if (cause instanceof VistaAPIError) {
    if (cause.code === 'port_unavailable') return t('portUnavailable', { port: draft.value.port })
    if (cause.code === 'port_restart_start_failed' || cause.code === 'port_restart_unavailable') return t('portChangeStartFailed')
  }
  return cause instanceof Error ? cause.message : String(cause)
}
async function save(confirmed = false) {
  if (!canSave.value) return
  if (portChanges.value && !confirmed) { showConfirm(); return }
  const switching = portChanges.value, port = Number(draft.value.port)
  saving.value = true; error.value = ''; saved.value = false
  if (switching) {
    const target = new URL(window.location.origin)
    // HTTPS reverse proxies retain their public entry when the backend port changes.
    if (target.protocol === 'http:') target.port = String(port)
    targetAddress.value = target.origin
  }
  try {
    const value = await client.request<ServerConfig | { restarting: true; accepted: boolean; address: string; instanceId: string; port: number }>('/server-config', {
      method: 'PUT', body: JSON.stringify({ config: draft.value, revision: baseRevision.value, ...(switching ? { confirmRestart: true } : {}) }),
      signal: AbortSignal.any([lifetime.signal, AbortSignal.timeout(15_000)]),
    })
    if (lifetime.signal.aborted) return
    if ('restarting' in value) {
      const target = new URL(value.address)
      if (!value.accepted || value.port !== port || target.protocol !== 'http:' || Number(target.port || '80') !== port) throw new Error(t('portChangeUnconfirmed'))
      probeTarget()
    } else { receive(value, true); saved.value = !conflict.value }
  } catch (cause) {
    if (lifetime.signal.aborted) return
    error.value = localError(cause)
    if (switching && !(cause instanceof VistaAPIError)) { phase.value = 'unconfirmed'; probeTarget() }
    else if (cause instanceof VistaAPIError && cause.status === 409 && (!cause.code || cause.code === 'configuration_changed')) await readConfiguration()
  } finally { saving.value = false; confirmOpen.value = false; emit('modal-open', false) }
}
</script>
<template>
  <section class="server-config-setting" aria-labelledby="server-config-heading" :aria-busy="loading || saving">
    <h3 id="server-config-heading">{{ t('serverConfig') }}</h3>
    <template v-if="phase !== 'editing'">
      <v-alert :type="phase === 'ready' ? 'success' : phase === 'unconfirmed' ? 'warning' : 'info'" role="status" :message="t(phase === 'ready' ? 'portChangeReady' : phase === 'unconfirmed' ? 'portChangeUnconfirmed' : 'portChangeAccepted')" />
      <a class="port-change-open ui-btn ui-btn-primary" :href="targetAddress + '/'" target="_blank" rel="noopener noreferrer">{{ t('portChangeOpen') }}</a>
      <code class="port-change-address">{{ targetAddress }}</code>
      <v-button v-if="phase === 'unconfirmed'" @click="probeTarget">{{ t('restartVistaReconnect') }}</v-button>
    </template>
    <template v-else>
      <p v-if="loading && !settings" role="status">{{ t('serverConfigLoading') }}</p>
      <form v-if="settings" @submit.prevent="save()">
        <div v-if="settings.runningPort" class="server-config-status">{{ t('serverConfigRunning', { port: settings.runningPort }) }}</div>
        <SchemaForm id="server-config" v-model="draft" :schema="settings.schema" :uischema="uischema" :disabled="saving || loading" :reset-key="editorEpoch" @validity="editorValid = $event" @dirty="editorDirty = $event; $event && (saved = false)" />
        <v-alert v-if="conflict" type="warning" role="alert" :message="t('serverConfigConflict')" />
        <div class="server-config-actions">
          <v-button type="submit" variant="primary" :loading="saving" :disabled="!canSave">{{ t(portChanges ? 'portChangeConfirm' : 'saveChanges') }}</v-button>
          <ConfirmPopover v-if="dirty || conflict" :show="discardOpen" :message="t('serverConfigDiscard')" :cancel-text="t('cancel')" :confirm-text="t('serverConfigReload')" :busy="saving" popover-class="server-config-discard" @update:show="discardOpen = $event" @confirm="discard"><template #trigger><v-button type="button" :disabled="saving">{{ t('serverConfigReload') }}</v-button></template></ConfirmPopover>
        </div>
        <v-alert v-if="saved" type="success" role="status" :message="t('serverConfigSaved')" />
        <p v-if="pending" role="status">{{ t('serverConfigPending', { port: settings.config.port }) }}</p>
        <p class="server-config-startup">{{ t('serverConfigStartupHint') }}</p>
      </form>
    </template>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error && !loading && phase === 'editing'" type="button" @click="readConfiguration()">{{ t('retry') }}</v-button>
  </section>
  <v-modal :open="confirmOpen" :title="t('portChangeConfirm')" :footer="null" :closable="!saving" :keyboard="!saving" :mask-closable="!saving" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))" @cancel="closeConfirm">
    <p>{{ t('portChangeWarning', { port: draft.port }) }}</p>
    <p v-if="serverStatus">{{ t('restartVistaActive', { sessions: serverStatus.sessions.running, processes: serverStatus.managedBackgroundProcesses }) }}</p>
    <p v-else>{{ t('restartVistaCountsUnknown') }}</p>
    <div class="actions"><v-button :disabled="saving" @click="closeConfirm">{{ t('cancel') }}</v-button><v-button variant="primary" :loading="saving" :disabled="!canSave" @click="save(true)">{{ t('portChangeConfirm') }}</v-button></div>
  </v-modal>
</template>
<style scoped>
.server-config-setting { padding: 1rem 0; border-bottom: 1px solid var(--border); min-width: 0; }
.server-config-setting h3 { margin: 0 0 .5rem; font-size: 1rem; }
.server-config-setting p { margin: .5rem 0 .75rem; color: var(--muted); line-height: 1.6; }
.server-config-setting form { display: flex; flex-direction: column; gap: .5rem; min-width: 0; }
.server-config-status { color: var(--muted); font-size: var(--font-size-small); }
.server-config-actions { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; margin: .25rem 0; }
.server-config-startup { font-size: var(--font-size-small); }
.port-change-open { display: inline-flex; margin: .75rem .5rem .5rem 0; text-decoration: none; }
.port-change-address { display: block; overflow-wrap: anywhere; }
</style>
