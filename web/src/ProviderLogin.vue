<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Spin as ASpin } from 'antdv-next'
import { CopyOutlined } from '@antdv-next/icons'
import { VistaAPIError, type VistaClient } from './client/vista'
import type { CopilotAuthOptions } from './client/types'

type Login = { id: string; verificationUrl: string; userCode: string; status: 'pending' | 'connected' | 'failed' | 'expired' | 'canceled'; error?: string }
const props = defineProps<{ kind: 'codex' | 'copilot'; client: VistaClient; name: string; connectionKey?: string; copilotAuth?: CopilotAuthOptions }>()
const emit = defineEmits<{ connected: []; busy: [value: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const login = ref<Login | null>(null)
const starting = ref(false)
const error = ref('')
const copied = ref(false)
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false

async function poll(id: string, ticket: number) {
  try {
    const result = await props.client.request<Login>(`/provider-auth/${props.kind}/${id}`)
    if (disposed || ticket !== generation) return
    login.value = result
    error.value = ''
    if (result.status === 'connected') { emit('busy', false); emit('connected'); return }
    if (result.status !== 'pending') {
      error.value = result.error || (result.status === 'expired' ? t('providerLoginExpired') : '')
      emit('busy', false)
      return
    }
  } catch (cause) {
    if (disposed || ticket !== generation) return
    if (cause instanceof VistaAPIError && cause.status === 404) {
      login.value = null
      error.value = t('providerLoginExpired')
      emit('busy', false)
      return
    }
    error.value = String(cause)
    // Keep checking after a transient connection failure. The server owns
    // authorization and may still finish while this browser reconnects.
  }
  timer = setTimeout(() => { void poll(id, ticket) }, 1500)
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
    login.value = result
    void poll(result.id, ticket)
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
    clearTimeout(timer)
    login.value = null
    error.value = ''
    emit('busy', false)
  } catch (cause) { error.value = String(cause) }
}

async function copyCode() {
  try { await navigator.clipboard.writeText(login.value!.userCode); copied.value = true }
  catch { error.value = t('providerCopyManually') }
}

onBeforeUnmount(() => {
  disposed = true
  emit('busy', false)
  generation++
  clearTimeout(timer)
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
