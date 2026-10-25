<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Spin as ASpin } from 'antdv-next'
import { CopyOutlined } from '@antdv-next/icons'
import { VistaAPIError, type VistaClient } from './client/vista'
import { useWorkspace } from './composables/useWorkspace'
import type { CopilotAuthOptions, LoginUpdate } from './client/types'

type Login = { id: string; verificationUrl: string; userCode: string; status: 'pending' | 'connected' | 'failed' | 'expired' | 'canceled'; error?: string }
const props = defineProps<{ kind: 'codex' | 'copilot'; client: VistaClient; name: string; connectionKey?: string; copilotAuth?: CopilotAuthOptions }>()
const emit = defineEmits<{ connected: []; busy: [value: boolean] }>()
const { stateSync } = useWorkspace()
const lifetime = new AbortController()
const { t } = useI18n({ useScope: 'global' })
const login = ref<Login | null>(null)
const starting = ref(false)
const error = ref('')
const copied = ref(false)
let generation = 0
let readRevision = 0
let disposed = false

function accept(result: Login) {
  const previous = login.value?.status
  login.value = result
  error.value = ''
  if (result.status === 'connected') { emit('busy', false); if (previous !== 'connected') emit('connected'); return }
  if (result.status !== 'pending') {
    error.value = result.error || (result.status === 'expired' ? t('providerLoginExpired') : '')
    emit('busy', false)
  }
}
function receive(update: LoginUpdate) {
  if (!login.value || update.id !== login.value.id) return
  readRevision++
  accept({ ...login.value, ...update } as Login)
}

async function readLogin(id: string, ticket: number, signal = lifetime.signal, propagate = false) {
  const revision = ++readRevision
  try {
    const result = await props.client.request<Login>(`/provider-auth/${props.kind}/${id}`, { signal })
    if (disposed || signal.aborted || ticket !== generation || revision !== readRevision) return
    accept(result)
  } catch (cause) {
    if (disposed || signal.aborted || ticket !== generation || revision !== readRevision) return
    if (cause instanceof VistaAPIError && cause.status === 404) {
      login.value = null
      error.value = t('providerLoginExpired')
      emit('busy', false)
      return
    }
    error.value = String(cause)
    if (propagate) throw cause
  }
}

async function start() {
  if (starting.value || login.value?.status === 'pending') return
  const ticket = ++generation
  starting.value = true
  emit('busy', true)
  error.value = ''
  copied.value = false
  try {
    const result = await props.client.request<Login>(`/provider-auth/${props.kind}`, { method: 'POST', body: JSON.stringify({ name: props.name.trim(), key: props.connectionKey || '',
      copilotAuth: props.kind === 'copilot' ? { enterpriseUrl: props.copilotAuth?.enterpriseUrl?.trim() || '', clientId: props.copilotAuth?.clientId?.trim() || '' } : undefined,
    }) })
    if (disposed || ticket !== generation) {
      await props.client.request(`/provider-auth/${props.kind}/${result.id}`, { method: 'DELETE' })
      return
    }
    accept(result)
    const update = stateSync.snapshot()?.providerLogins?.find(item => item.id === result.id)
    if (update) receive(update)
  } catch (cause) {
    if (!disposed && ticket === generation) { error.value = String(cause); emit('busy', false) }
  } finally { if (!disposed && ticket === generation) starting.value = false }
}

async function cancel() {
  const pending = login.value
  try {
    if (pending?.status === 'pending') {
      const result = await props.client.request<Login>(`/provider-auth/${props.kind}/${pending.id}`, { method: 'DELETE' })
      if (disposed) return
      if (result.status === 'connected') { emit('connected'); return }
    }
    generation++
    login.value = null
    error.value = ''
    emit('busy', false)
  } catch (cause) { error.value = String(cause) }
}

async function copyCode() {
  try { await navigator.clipboard.writeText(login.value!.userCode); copied.value = true }
  catch { error.value = t('providerCopyManually') }
}

const unsubscribe = stateSync.subscribe(['provider-login'], async (_, signal, snapshots) => {
  if (login.value?.status !== 'pending') return
  if (snapshots.providerLogins) {
    const update = snapshots.providerLogins.find(item => item.id === login.value?.id)
    if (update) receive(update)
    else await readLogin(login.value.id, generation, signal, true)
  } else await readLogin(login.value.id, generation, signal, true)
})

onBeforeUnmount(() => {
  disposed = true
  emit('busy', false)
  generation++
  lifetime.abort()
  unsubscribe()
  if (login.value?.status === 'pending') void props.client.request(`/provider-auth/${props.kind}/${login.value.id}`, { method: 'DELETE' }).catch(() => {})
})
</script>

<template>
  <div class="provider-login">
    <p class="provider-help">{{ t(`${kind}LoginHint`) }}</p>
    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <template v-if="login?.status === 'pending'">
      <div class="provider-device-code"><code>{{ login.userCode }}</code><a-button type="text" :aria-label="t(copied ? 'providerCodeCopied' : 'providerCopyCode')" @click="copyCode"><CopyOutlined /></a-button></div>
      <p class="provider-help">{{ t(`${kind}DeviceHint`) }}</p>
      <div class="actions"><a-button type="primary" :href="login.verificationUrl" target="_blank" rel="noopener noreferrer">{{ t(`${kind}OpenLogin`) }}</a-button><a-button @click="cancel">{{ t('providerCancelLogin') }}</a-button></div>
      <div class="provider-login-wait" role="status"><a-spin size="small" /><span>{{ t('providerWaiting') }}</span></div>
    </template>
    <a-button v-else :type="connectionKey ? 'default' : 'primary'" :loading="starting" @click="start">{{ t(connectionKey ? 'providerReconnect' : `${kind}SignIn`) }}</a-button>
  </div>
</template>
