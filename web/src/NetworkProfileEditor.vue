<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RuleEffect, type ControlElement, type JsonSchema7 } from '@jsonforms/core'
import SchemaForm from './SchemaForm.vue'
import NetworkCheckPanel from './NetworkCheckPanel.vue'
import type { NetworkCheck, ProxyProfile } from '@vista/client'
import { Alert as VAlert, Button as VButton } from './ui'
import type { FormIssue } from './schema/validation'
import { newProxyProfile, validConnectionIP } from './client/network'
const props = defineProps<{ profile?: ProxyProfile; busy?: boolean; error?: string; stale?: boolean; check?: (profile: ProxyProfile, reuse: boolean, target: string, signal: AbortSignal) => Promise<NetworkCheck> }>()
const emit = defineEmits<{ save: [profile: ProxyProfile, reuse: boolean]; cancel: []; dirty: [value: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const original = props.profile || newProxyProfile()
const basic = ref<Record<string, unknown>>({ name: original.name, protocol: original.protocol, server: original.server, port: original.port, authentication: original.authentication, connectIp: original.connectIp || '', verifyCertificate: !original.insecureSkipVerify })
const credentials = ref<Record<string, unknown>>({ username: '', password: '', reuse: false })
const validBasic = ref(false), validCredentials = ref(true)
const dirtyBasic = ref(false), dirtyCredentials = ref(false)
const dirty = computed(() => dirtyBasic.value || dirtyCredentials.value)
watch(dirty, value => emit('dirty', value), { immediate: true })
const connectionIP = computed(() => basic.value.protocol === 'https' ? String(basic.value.connectIp).trim() : '')
const changedEntry = computed(() => basic.value.server !== original.server || basic.value.port !== original.port || basic.value.protocol !== original.protocol || connectionIP.value !== (original.connectIp || ''))
const showCredentials = computed(() => basic.value.authentication === 'password')
const defaultPorts = { http: 80, https: 443, socks5: 1080 }
let draftProtocol = original.protocol
function normalizeBasicDraft(value: Record<string, unknown>) {
  const protocol = value.protocol as ProxyProfile['protocol']
  const port = protocol !== draftProtocol && value.port === defaultPorts[draftProtocol] ? defaultPorts[protocol] : value.port
  draftProtocol = protocol
  return { ...value, port }
}
const basicSchema = computed<JsonSchema7>(() => ({ type: 'object', additionalProperties: false, required: ['name', 'protocol', 'server', 'port', 'authentication', 'connectIp', 'verifyCertificate'], properties: {
  name: { type: 'string', title: t('networkProfileName'), minLength: 1, maxLength: 128, pattern: '\\S' },
  protocol: { type: 'string', title: t('networkProtocol'), enum: ['http', 'https', 'socks5'] },
  server: { type: 'string', title: t('networkServer'), minLength: 1, maxLength: 253, pattern: '^[^/@?#\\\\\\s]+$' },
  port: { type: 'integer', title: t('networkPort'), minimum: 1, maximum: 65535 },
  authentication: { type: 'string', title: t('providerAuthentication'), enum: ['none', 'password'] },
  connectIp: { type: 'string', title: t('networkConnectIP'), description: t('networkConnectIPHint') },
  verifyCertificate: { type: 'boolean', title: t('networkVerifyCertificate'), description: t('networkVerifyCertificateHint'), enum: [true, false] },
} }))
const field = (name: string, options: Record<string, unknown> = {}): ControlElement => ({ type: 'Control', scope: `#/properties/${name}`, options: { showErrorsOnBlur: true, ...options } })
const basicUI = computed(() => ({ type: 'VerticalLayout' as const, elements: [
  { type: 'HorizontalLayout' as const, elements: [field('name'), field('protocol')] },
  { type: 'HorizontalLayout' as const, elements: [field('server'), field('port')] },
  { type: 'HorizontalLayout' as const, rule: { effect: RuleEffect.SHOW, condition: { scope: '#/properties/protocol', schema: { const: 'https' }, failWhenUndefined: true } }, elements: [field('connectIp'), field('verifyCertificate', { enumLabels: { true: t('networkVerifyCertificateOn'), false: t('networkVerifyCertificateOff') } })] },
  field('authentication', { enumLabels: { none: t('noAuthentication'), password: t('networkPasswordAuth') } }),
] }))
function basicIssues(value: Record<string, unknown>): FormIssue[] {
  return value.protocol === 'https' && typeof value.connectIp === 'string' && !validConnectionIP(value.connectIp)
    ? [{ path: '/connectIp', message: t('configFieldInvalid') }] : []
}
const credentialSchema = computed<JsonSchema7>(() => ({ type: 'object', additionalProperties: false, required: ['username', 'password', 'reuse'], properties: {
  username: { type: 'string', title: t('networkUsername'), writeOnly: true, maxLength: 255 },
  password: { type: 'string', title: t('networkPassword'), writeOnly: true, maxLength: basic.value.protocol === 'socks5' ? 255 : 4096 },
  reuse: { type: 'boolean', title: t('networkReuseChanged') },
} }))
const credentialUI = computed(() => ({ type: 'VerticalLayout' as const, elements: [
  { type: 'HorizontalLayout' as const, elements: ['username', 'password'].map(name => field(name, { placeholder: original.hasCredentials ? t('reuseSavedKey') : '' })) },
  ...(original.hasCredentials && changedEntry.value ? [field('reuse')] : []),
] }))
function credentialIssues(value: Record<string, unknown>): FormIssue[] {
  if (!showCredentials.value) return []
  if (!value.username && !value.password && original.hasCredentials && (!changedEntry.value || value.reuse)) return []
  return ['username', 'password'].flatMap(field => !value[field] ? [{ path: `/${field}`, message: t('configFieldRequired') }] : [])
}
function currentProfile(): ProxyProfile | undefined {
  if (!validBasic.value || showCredentials.value && !validCredentials.value) return
  return { id: original.id, name: String(basic.value.name).trim(), protocol: basic.value.protocol as ProxyProfile['protocol'], server: String(basic.value.server).trim(), port: Number(basic.value.port), authentication: basic.value.authentication as ProxyProfile['authentication'], connectIp: connectionIP.value, insecureSkipVerify: basic.value.protocol === 'https' && basic.value.verifyCertificate === false, ...(showCredentials.value ? { username: String(credentials.value.username), password: String(credentials.value.password) } : {}) }
}
function save() { const profile = currentProfile(); if (profile && !props.stale) emit('save', profile, credentials.value.reuse === true) }
const checkRevision = ref(0)
watch([basic, credentials, validBasic, validCredentials], () => { checkRevision.value++ }, { deep: true })
async function checkCurrent(target: string, signal: AbortSignal) {
  const profile = currentProfile(); if (!profile || !props.check) throw new Error(t('networkUnavailable')); return props.check(profile, credentials.value.reuse === true, target, signal)
}
onBeforeUnmount(() => { credentials.value = { username: '', password: '', reuse: false } })
</script>
<template>
  <form class="network-profile-editor" @submit.prevent="save">
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <SchemaForm id="proxy-profile" v-model="basic" :schema="basicSchema" :uischema="basicUI" :validate="basicIssues" :normalize-draft="normalizeBasicDraft" :disabled="busy" @validity="validBasic = $event" @dirty="dirtyBasic = $event" />
    <template v-if="showCredentials">
      <p v-if="original.hasCredentials" class="network-help">{{ t('networkCredentialsSaved') }}</p>
      <SchemaForm id="proxy-credentials" v-model="credentials" :schema="credentialSchema" :uischema="credentialUI" :validate="credentialIssues" :disabled="busy" @validity="validCredentials = $event" @dirty="dirtyCredentials = $event" />
    </template>
    <template v-if="check"><NetworkCheckPanel :perform="checkCurrent" :revision-key="checkRevision" :disabled="busy || stale || !validBasic || showCredentials && !validCredentials" /></template>
    <div class="network-actions">
      <v-button type="button" :disabled="busy" @click="emit('cancel')">{{ t('cancel') }}</v-button>
      <v-button type="submit" variant="primary" :loading="busy" :disabled="stale || !validBasic || showCredentials && !validCredentials">{{ t('saveChanges') }}</v-button>
    </div>
  </form>
</template>
<style scoped>
.network-profile-editor { display: flex; flex-direction: column; gap: .8rem; min-width: 0; }
.network-help { color: var(--muted); line-height: 1.6; margin: 0; }
.network-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: .5rem; }
</style>
