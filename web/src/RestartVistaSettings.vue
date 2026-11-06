<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Modal as VModal } from './ui'
import { useWorkspace } from './composables/useWorkspace'

const { t } = useI18n({ useScope: 'global' })
const { client, serverStatus, stateConnection, stateSync } = useWorkspace()
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const open = ref(false)
const phase = ref<'confirm' | 'waiting' | 'unconfirmed' | 'success'>('confirm')
const error = ref('')
const busy = computed(() => phase.value === 'waiting')
const succeeded = computed(() => phase.value === 'success')
const connected = computed(() => stateConnection.value.phase === 'ready' && !!serverStatus.value?.instanceId)
let previousInstance = ''
let attempt = 0
let request: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined
function clearTimer() { clearTimeout(timer); timer = undefined }
function checkConnection() {
  if (!open.value || phase.value === 'confirm' || !connected.value || !previousInstance) return
  if (serverStatus.value!.instanceId === previousInstance) return
  clearTimer()
  error.value = ''
  phase.value = 'success'
}
watch([stateConnection, () => serverStatus.value?.instanceId], checkConnection, { flush: 'post' })
watch(open, value => emit('modal-open', value), { flush: 'sync' })
onUnmounted(() => {
  attempt++
  clearTimer()
  request?.abort()
  emit('modal-open', false)
})
function confirm() {
  attempt++
  clearTimer()
  request?.abort()
  previousInstance = ''
  error.value = ''
  phase.value = 'confirm'
  open.value = true
}
function cancel() {
  if (busy.value) return
  attempt++
  request?.abort()
  clearTimer()
  open.value = false
}
function waitForConnection() {
  phase.value = 'waiting'
  clearTimer()
  timer = setTimeout(() => { phase.value = 'unconfirmed' }, 45_000)
  checkConnection()
}
function retryConnection() {
  error.value = ''
  waitForConnection()
  stateSync.reconnect()
}
async function restart() {
  if (phase.value !== 'confirm' || !connected.value) return
  previousInstance = serverStatus.value!.instanceId!
  const current = attempt
  const controller = request = new AbortController()
  error.value = ''
  waitForConnection()
  try {
    const result = await client.request<{ accepted: boolean; instanceId: string }>('/restart', {
      method: 'POST', body: JSON.stringify({ confirm: 'restart' }),
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(45_000)]),
    })
    if (current !== attempt) return
    if (result.accepted !== true || result.instanceId !== previousInstance) throw new Error(t('restartVistaUnconfirmed'))
    // Acceptance identifies the old process, not a completed restart.
    checkConnection()
    if (!succeeded.value) stateSync.reconnect()
  } catch (failure) {
    if (current !== attempt || succeeded.value) return
    clearTimer()
    error.value = String(failure)
    phase.value = 'unconfirmed'
    // A lost response is not evidence that the restart did not happen.
    // Reconnect the shared stream; never repeat a lifecycle mutation here.
    stateSync.reconnect()
  }
}
</script>

<template>
  <div class="restart-vista-setting general-setting">
    <div><h4>{{ t('restartVista') }}</h4><p>{{ t('restartVistaHint') }}</p></div>
    <v-button @click="confirm">{{ t('restartVista') }}</v-button>
  </div>
  <v-modal :open="open" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))"
    class="restart-vista-modal" :title="t('restartVistaTitle')" :footer="null"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
    :styles="{ body: { maxHeight: 'calc(100dvh - 12rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }"
    @cancel="cancel">
    <div class="restart-vista-confirmation" :aria-busy="busy">
      <p>{{ t('restartVistaWarning') }}</p>
      <p v-if="connected">{{ t('restartVistaActive', { sessions: serverStatus!.sessions.running, processes: serverStatus!.managedBackgroundProcesses }) }}</p>
      <p v-else-if="phase === 'confirm'">{{ t('restartVistaCountsUnknown') }}</p>
      <v-alert v-if="busy" type="info" role="status" :message="t('restartVistaProgress')" />
      <v-alert v-else-if="phase === 'success'" type="success" role="status" :message="t('restartVistaSuccess')" />
      <v-alert v-else-if="phase === 'unconfirmed'" type="warning" role="alert" :message="t('restartVistaUnconfirmed')" />
      <p v-if="error" class="restart-vista-error">{{ error }}</p>
      <div class="actions">
        <v-button :disabled="busy" @click="cancel">{{ t(phase === 'success' ? 'close' : 'cancel') }}</v-button>
        <v-button v-if="phase === 'confirm'" :disabled="!connected" @click="restart">{{ t('restartVista') }}</v-button>
        <v-button v-else-if="phase === 'unconfirmed'" @click="retryConnection">{{ t('restartVistaReconnect') }}</v-button>
      </div>
    </div>
  </v-modal>
</template>

<style>
.about-settings-content > .restart-vista-setting.general-setting {
  flex-direction: row;
  align-items: center;
  gap: 16px;
  padding: 16px 0;
  border-top: 0;
  border-bottom: 1px solid var(--border);
}
.restart-vista-setting > div { flex: 1; min-width: 0; }
.restart-vista-setting h4 { margin: 0; font-size: var(--font-size-body); font-weight: 550; }
.restart-vista-setting > .ui-btn { flex: none; }
.restart-vista-confirmation { line-height: 1.7; overflow-wrap: anywhere; }
.restart-vista-confirmation > p { margin: 0 0 12px; }
.restart-vista-confirmation .actions { justify-content: flex-end; margin-top: 16px; }
.restart-vista-confirmation .restart-vista-error { color: var(--muted); white-space: pre-wrap; margin-top: 12px; }
</style>
