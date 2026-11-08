<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RuleEffect, type ControlElement, type JsonSchema7, type VerticalLayout } from '@jsonforms/core'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Input as VInput, Modal as VModal, Switch as VSwitch } from './ui'
import { EditOutlined, LinkOutlined, ReloadOutlined, DeleteOutlined } from '@antdv-next/icons'
import ConfirmPopover from './ConfirmPopover.vue'
import CopyAction from './ui/CopyAction.vue'
import SchemaForm from './SchemaForm.vue'
import type { FormIssue } from './schema/validation'
import { VistaAPIError } from './client/vista'
import { useWorkspace } from './composables/useWorkspace'
import { formatDateTime } from './ui/datetime.ts'
import type { MCPCheck as Check, MCPServer, MCPConfig, LoginUpdate } from './client/types'

type Tool = { name: string; description: string; inputSchema: unknown }
type Login = { id: string; name: string; status: string; authorizationUrl?: string; error?: string; expiresAt: string }
type MCPDocument = Record<string, unknown> & {
  name: string; transport: 'http' | 'stdio'; disabled: boolean
  command?: string; args?: string[]; directory?: string; endpoint?: string
  auth?: 'none' | 'token' | 'oauth'; clientId?: string
}
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const lifetime = new AbortController()
const servers = ref<MCPServer[]>([])
const maxServers = ref(4)
const defaultDirectory = ref('')
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const saved = ref(false)
const deleting = ref('')
const deleteError = ref('')
const editorOpen = ref(false)
const editing = ref<MCPServer | null>(null)
const formError = ref('')
// This is a public connection draft, not raw config.json or an authentication snapshot.
// Keep credential values, header names/values, source IDs and login state separate.
const draft = ref<MCPDocument>({ name: '', transport: 'http', disabled: false })
const credentials = reactive({ token: '', clientSecret: '', clearClientSecret: false, headers: [] as { name: string; value: string }[] })
const schemaEditor = ref<InstanceType<typeof SchemaForm>>()
const editorValid = ref(false)
const credentialValid = ref(true)
const editorResetKey = ref(0)
// Both platform path forms are accepted here; the server validates its own OS.
const absolutePathPattern = String.raw`^(?:/|[A-Za-z]:[\\/]|\\\\[^\\/]+[\\/][^\\/]+)`
const schema = computed((): JsonSchema7 => ({
  type: 'object',
  additionalProperties: false,
  required: ['name', 'transport', 'disabled'],
  properties: {
    name: { type: 'string', minLength: 1, maxLength: 64, pattern: String.raw`^(?=[\s\S]*\S)[^\u0000-\u001f\u007f-\u009f]+$`, description: t('mcpNameHint') },
    transport: { type: 'string', enum: ['http', 'stdio'] },
    disabled: { type: 'boolean' },
    command: { type: 'string' },
    args: { type: 'array', maxItems: 32, items: { type: 'string', maxLength: 1024 } },
    directory: { type: 'string' },
    endpoint: { type: 'string', maxLength: 2048, description: t('mcpEndpointHint') },
    auth: { type: 'string', enum: ['none', 'token', 'oauth'] },
    clientId: { type: 'string' },
  },
  // Known inactive public fields remain draft-only so transport/auth switches
  // retain edits. Unknown fields are rejected in every branch, never passed on.
  allOf: [
    {
      if: { properties: { transport: { const: 'stdio' } }, required: ['transport'] },
      then: { required: ['command', 'directory'], properties: { command: { pattern: absolutePathPattern }, directory: { pattern: absolutePathPattern } } },
    },
    {
      if: { properties: { transport: { const: 'http' } }, required: ['transport'] },
      then: {
        required: ['endpoint', 'auth'],
        properties: { endpoint: { minLength: 1, pattern: '^https?://[^/?#@]+(?:[/?][^#]*)?#?$' } },
        allOf: [{ if: { properties: { auth: { const: 'oauth' } }, required: ['auth'] }, then: { properties: { clientId: { maxLength: 1024 } } } }],
      },
    },
  ],
}))
const uischema = computed<VerticalLayout>(() => {
  const control = (field: string, label: string, options?: Record<string, unknown>): ControlElement => ({ type: 'Control', scope: `#/properties/${field}`, label, ...(options ? { options } : {}) })
  const show = (field: string, value: string) => ({ effect: RuleEffect.SHOW, condition: { scope: `#/properties/${field}`, schema: { const: value }, failWhenUndefined: true } })
  return {
    type: 'VerticalLayout',
    elements: [
      control('name', t('mcpName')),
      control('transport', t('mcpTransport'), { enumLabels: { http: t('mcpHTTP'), stdio: t('mcpStdio') } }),
      control('disabled', t('mcpDisabled')),
      {
        type: 'VerticalLayout', rule: show('transport', 'stdio'),
        elements: [
          control('command', t('mcpCommand')),
          control('args', t('mcpArguments'), { multi: true, rows: 2 }),
          control('directory', t('mcpDirectory')),
        ],
      },
      {
        type: 'VerticalLayout', rule: show('transport', 'http'),
        elements: [
          control('endpoint', t('mcpEndpoint')),
          control('auth', t('providerAuthentication'), { enumLabels: { none: t('mcpAuthNone'), token: t('mcpBearerToken'), oauth: 'OAuth' } }),
          { ...control('clientId', 'Client ID'), rule: show('auth', 'oauth') },
        ],
      },
    ],
  }
})
function updateDraft(value: Record<string, unknown>) { draft.value = value as MCPDocument }
function validEndpoint(value: string) {
  try {
    const url = new URL(value)
    if (!url.hostname || url.username || url.password || url.hash) return false
    if (url.protocol === 'https:') return true
    return url.protocol === 'http:' && (url.hostname === 'localhost' || url.hostname === '[::1]' ||
      /^127(?:\.\d{1,3}){3}$/.test(url.hostname) || /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(url.hostname))
  } catch { return false }
}
const utf8 = new TextEncoder()
function validPublicDocument(value: Record<string, unknown>): FormIssue[] {
  if (value.transport === 'stdio') return Array.isArray(value.args) ? value.args.flatMap((arg, index) => typeof arg === 'string' && utf8.encode(arg).length > 1024 ? [{ path: `/args/${index}`, message: t('configByteLimit', { limit: 1024 }) }] : []) : []
  const issues: FormIssue[] = []
  if (value.transport === 'http' && typeof value.endpoint === 'string' && value.endpoint.trim() && !validEndpoint(value.endpoint.trim())) issues.push({ path: '/endpoint', message: t('configSecureEndpoint') })
  if (value.auth === 'oauth' && typeof value.clientId === 'string' && utf8.encode(value.clientId.trim()).length > 1024) issues.push({ path: '/clientId', message: t('configByteLimit', { limit: 1024 }) })
  return issues
}
const credentialDocument = computed<Record<string, unknown>>({
  get: () => ({ token: credentials.token, clientSecret: credentials.clientSecret, clearClientSecret: credentials.clearClientSecret, headers: credentials.headers.map(header => ({ ...header })) }),
  set: value => { credentials.token = value.token as string; credentials.clientSecret = value.clientSecret as string; credentials.clearClientSecret = value.clearClientSecret as boolean; credentials.headers = value.headers as { name: string; value: string }[] },
})
const credentialSchema = computed<JsonSchema7>(() => ({ type: 'object', additionalProperties: false, required: ['token', 'clientSecret', 'clearClientSecret', 'headers'], properties: {
  token: { type: 'string', title: t('mcpBearerToken'), writeOnly: true, minLength: draft.value.auth === 'token' && !editing.value?.hasToken ? 1 : 0, maxLength: 4096 },
  clientSecret: { type: 'string', title: 'Client secret', writeOnly: true, maxLength: 4096 },
  clearClientSecret: { type: 'boolean', title: t('mcpClearSecret') },
  headers: { type: 'array', title: t('customHeaders'), maxItems: 32, items: { type: 'object', additionalProperties: false, required: ['name', 'value'], properties: {
    name: { type: 'string', title: t('headerName'), minLength: 1, maxLength: 256, default: '' },
    value: { type: 'string', title: t('headerValue'), writeOnly: true, maxLength: 8192, default: '' },
  } } },
} }))
const credentialUISchema = computed<VerticalLayout>(() => {
  const elements: ControlElement[] = []
  if (draft.value.auth === 'token') elements.push({ type: 'Control', scope: '#/properties/token', options: { placeholder: editing.value?.hasToken ? t('reuseSavedKey') : t('enterApiKey') } })
  if (draft.value.auth === 'oauth') {
    elements.push({ type: 'Control', scope: '#/properties/clientSecret', options: { placeholder: editing.value?.hasClientSecret ? t('reuseSavedKey') : '' } })
    if (editing.value?.hasClientSecret) elements.push({ type: 'Control', scope: '#/properties/clearClientSecret' })
  }
  elements.push({ type: 'Control', scope: '#/properties/headers' })
  return { type: 'VerticalLayout', elements }
})
const canSave = computed(() => {
  // Invalid local fields never replace the draft; validity gates the last valid value.
  if (!editorValid.value) return false
  if (draft.value.transport === 'stdio') return true
  if (!credentialValid.value) return false
  const names = credentials.headers.map(header => header.name.trim().toLowerCase()).filter(Boolean)
  if (new Set(names).size !== names.length || credentials.headers.some(header => !header.name.trim() && header.value)) return false
  return draft.value.auth !== 'token' || !!credentials.token.trim() || !!editing.value?.hasToken
})

let configRevision = 0
function acceptConfig(result: MCPConfig) {
  servers.value = result.servers
  maxServers.value = result.maxServers
  defaultDirectory.value = result.defaultDirectory
}
async function refresh(signal = lifetime.signal) {
  const revision = ++configRevision
  const result = await client.request<MCPConfig>('/mcp-config', { signal })
  if (lifetime.signal.aborted || signal.aborted || revision !== configRevision) return
  acceptConfig(result)
}
async function load() {
  loading.value = true
  error.value = ''
  try { await refresh() }
  catch (cause) { if (!lifetime.signal.aborted) error.value = String(cause) }
  finally { loading.value = false }
}

function edit(server?: MCPServer) {
  if (busy.value) return
  editing.value = server || null
  draft.value = {
    name: server?.name || '', transport: server?.transport || 'http',
    command: server?.command || '', args: [...(server?.args || [])],
    directory: server?.directory || defaultDirectory.value, endpoint: server?.endpoint || '',
    auth: server?.oauth ? 'oauth' : server?.hasToken ? 'token' : 'none',
    clientId: server?.clientId || '', disabled: !!server?.disabled,
  }
  Object.assign(credentials, {
    token: '', clientSecret: '', clearClientSecret: false,
    headers: (server?.headerNames || []).map(name => ({ name, value: '' })),
  })
  editorValid.value = false
  editorResetKey.value++
  formError.value = ''
  editorOpen.value = true
}
function closeEditor() {
  if (busy.value) return
  editorOpen.value = false
  editorValid.value = false
  credentials.token = credentials.clientSecret = ''
  credentials.headers = []
}
async function save() {
  if (busy.value || !canSave.value) return
  busy.value = true
  formError.value = ''
  error.value = ''
  saved.value = false
  const value = draft.value
  const http = value.transport === 'http'
  // Only active, explicitly supported fields enter the existing settings API.
  // Identity and blank-preserve/explicit-clear semantics stay outside the JSON.
  const body = {
    ...(editing.value ? { id: editing.value.id } : {}),
    name: value.name.trim(), transport: value.transport, disabled: value.disabled,
    ...(http ? { endpoint: value.endpoint!.trim(), oauth: value.auth === 'oauth',
      token: value.auth === 'token' ? credentials.token.trim() : '', clearToken: value.auth !== 'token',
      clientId: value.auth === 'oauth' ? (value.clientId || '').trim() : '',
      clientSecret: value.auth === 'oauth' ? credentials.clientSecret.trim() : '', clearClientSecret: credentials.clearClientSecret,
      headers: Object.fromEntries(credentials.headers.filter(header => header.name.trim()).map(header => [header.name.trim(), header.value])),
    } : { command: value.command!.trim(), directory: value.directory!.trim(), args: [...(value.args || [])] }),
  }
  try {
    await client.request('/mcp-config', { method: editing.value ? 'PUT' : 'POST', body: JSON.stringify(body), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    schemaEditor.value?.reset()
    editorOpen.value = false
    editorValid.value = false
    credentials.token = credentials.clientSecret = ''
    credentials.headers = []
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) formError.value = String(cause) }
  finally { busy.value = false }
}
async function remove(server: MCPServer) {
  if (busy.value) return
  busy.value = true
  deleteError.value = ''
  saved.value = false
  try {
    await client.request('/mcp-config', { method: 'DELETE', body: JSON.stringify({ id: server.id }), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    servers.value = servers.value.filter(item => item.id !== server.id)
    deleting.value = ''
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) deleteError.value = String(cause) }
  finally { busy.value = false }
}
function status(server: MCPServer) {
  if (server.disabled) return t('mcpDisabled')
  if (server.oauth && !server.authorized) return t('mcpAuthorizationRequired')
  return t(({ connecting: 'mcpConnecting', available: 'mcpAvailable', error: 'mcpUnavailable', authorization_required: 'mcpAuthorizationRequired' } as Record<string, string>)[server.check.status] || 'mcpUnchecked')
}
function checkedAt(value?: string) { return value && !value.startsWith('0001-') ? formatDateTime(value, locale.value) : '' }

const toolsServer = ref<MCPServer | null>(null)
const tools = ref<Tool[]>([])
const toolQuery = ref('')
const toolsLoading = ref(false)
const toolsError = ref('')
const visibleTools = computed(() => tools.value.filter(tool => `${tool.name} ${tool.description}`.toLowerCase().includes(toolQuery.value.toLowerCase())))
let checkController: AbortController | undefined
function closeTools() {
  checkController?.abort()
  toolsServer.value = null
}
async function inspect(server: MCPServer) {
  checkController?.abort()
  const controller = new AbortController()
  checkController = controller
  toolsServer.value = server
  tools.value = []
  toolsError.value = ''
  toolQuery.value = ''
  toolsLoading.value = true
  try {
    const result = await client.request<{ check: Check; tools: Tool[] | null }>(`/mcp-servers/${encodeURIComponent(server.id)}/check`, { method: 'POST', signal: controller.signal })
    if (controller.signal.aborted) return
    server.check = result.check
    tools.value = result.tools || []
    toolsError.value = result.check.error || ''
  } catch (cause) { if (!controller.signal.aborted) toolsError.value = String(cause) }
  finally { if (!controller.signal.aborted) toolsLoading.value = false }
}

const loginServer = ref<MCPServer | null>(null)
const login = ref<Login | null>(null)
const loginError = ref('')
const loginStartFailed = ref(false)
const callbackURL = ref('')
const loginSubmitting = ref(false)
const loginStages: Record<string, number> = { starting: 0, pending: 1, exchanging: 2 }
let loginGeneration = 0
let loginController: AbortController | undefined
const loginActive = computed(() => login.value ? loginStages[login.value.status] !== undefined : !loginStartFailed.value)
const canSubmitCallback = computed(() => login.value?.status === 'pending' && !loginSubmitting.value && !!callbackURL.value.trim() && callbackURL.value.length <= 8192)
const authorizationURL = computed(() => {
  try {
    const url = new URL(login.value?.authorizationUrl || '')
    return ['http:', 'https:'].includes(url.protocol) ? url.href : ''
  } catch { return '' }
})
function cancelLogin(id: string) { void client.request(`/mcp-auth/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => {}) }
function closeLogin() {
  loginGeneration++
  loginController?.abort()
  if (login.value && loginActive.value) cancelLogin(login.value.id)
  loginServer.value = null
  login.value = null
  callbackURL.value = ''
  loginSubmitting.value = false
}
async function authorize(server: MCPServer) {
  closeTools()
  closeLogin()
  const generation = loginGeneration
  loginServer.value = server
  loginError.value = ''
  loginStartFailed.value = false
  loginController = new AbortController()
  try {
    // The start request must return its ID even if the dialog has been closed,
    // so that a late response can still cancel the server-side login.
    const result = await client.request<Login>(`/mcp-servers/${encodeURIComponent(server.id)}/login`, { method: 'POST' })
    if (lifetime.signal.aborted || generation !== loginGeneration) { cancelLogin(result.id); return }
    login.value = result
    // Only the attempt-specific read exposes its authorization URL. Terminal
    // outcomes and configuration arrive through the shared state stream.
    const update = stateSync.snapshot()?.mcp?.logins.find(item => item.id === result.id)
    if (update) await receiveLogin(update)
  } catch (cause) {
    if (!lifetime.signal.aborted && generation === loginGeneration) {
      loginError.value = String(cause)
      loginStartFailed.value = true
    }
  }
}
let loginReadRevision = 0
function acceptLogin(update: LoginUpdate & Partial<Login>) {
  if (!login.value || update.id !== login.value.id) return false
  const current = loginStages[login.value.status]
  const next = loginStages[update.status]
  // SSE may finish the attempt before an HTTP acknowledgement arrives.
  // Terminal outcomes are immutable, and active phases only move forwards.
  if (current === undefined || (next !== undefined && next < current)) return false
  loginReadRevision++
  login.value = { ...login.value, ...update }
  loginError.value = update.error || ''
  if (update.status === 'connected') saved.value = true
  if (update.status !== 'starting' && update.status !== 'pending') {
    login.value.authorizationUrl = ''
    callbackURL.value = ''
  }
  return true
}
async function receiveLogin(update: LoginUpdate, signal = loginController?.signal) {
  if (!acceptLogin(update)) return
  if (update.status === 'pending' && !login.value?.authorizationUrl) await readLogin(loginGeneration, signal, true)
}
async function readLogin(generation: number, signal = loginController?.signal, propagate = false) {
  const revision = ++loginReadRevision
  const id = login.value?.id
  if (!id || generation !== loginGeneration) return
  try {
    const result = await client.request<Login>(`/mcp-auth/${encodeURIComponent(id)}`, { signal })
    if (lifetime.signal.aborted || signal?.aborted || generation !== loginGeneration || revision !== loginReadRevision || login.value?.id !== id) return
    acceptLogin(result)
  } catch (cause) {
    if (lifetime.signal.aborted || signal?.aborted || generation !== loginGeneration || revision !== loginReadRevision || login.value?.id !== id) return
    loginError.value = String(cause)
    if (cause instanceof VistaAPIError && cause.status === 404) { login.value = null; loginStartFailed.value = true; callbackURL.value = ''; return }
    if (propagate) throw cause
  }
}
async function submitCallback() {
  if (!canSubmitCallback.value || !login.value || !loginController) return
  const generation = loginGeneration
  const id = login.value.id
  const signal = loginController.signal
  const current = () => !lifetime.signal.aborted && !signal.aborted && generation === loginGeneration && login.value?.id === id
  loginSubmitting.value = true
  loginError.value = ''
  try {
    const result = await client.request<Login>(`/mcp-auth/${encodeURIComponent(id)}`, {
      method: 'POST', body: JSON.stringify({ callbackUrl: callbackURL.value }), signal,
    })
    if (!current()) return
    callbackURL.value = ''
    acceptLogin(result)
  } catch (cause) {
    if (!current() || login.value?.status !== 'pending') return
    // A conflict may be an accepted submission from another client. Recover
    // its current phase without resending the callback or reopening the URL.
    if (cause instanceof VistaAPIError && cause.status === 409) await readLogin(generation, signal)
    if (current() && login.value?.status === 'pending') loginError.value = String(cause)
  } finally {
    if (current()) loginSubmitting.value = false
  }
}
const unsubscribe = stateSync.subscribe(['mcp'], async (_, signal, snapshots) => {
  try {
    if (snapshots.mcp) {
      configRevision++
      acceptConfig(snapshots.mcp)
      const update = snapshots.mcp.logins.find(item => item.id === login.value?.id)
      if (update) await receiveLogin(update, signal)
      else if (login.value && loginActive.value) await readLogin(loginGeneration, signal, true)
    } else {
      await Promise.all([
        login.value && loginActive.value ? readLogin(loginGeneration, signal, true) : Promise.resolve(),
        refresh(signal),
      ])
    }
    error.value = ''
  } catch (cause) { if (!lifetime.signal.aborted && !signal.aborted) error.value = String(cause); throw cause }
  finally { loading.value = false }
})
watch(() => editorOpen.value || !!toolsServer.value || !!loginServer.value || !!deleting.value, open => emit('modal-open', open), { flush: 'sync' })
onUnmounted(() => {
  lifetime.abort()
  unsubscribe()
  checkController?.abort()
  closeLogin()
  emit('modal-open', false)
})
defineExpose({ imported: () => { saved.value = true }, headerAction: computed(() => ({ label: 'mcpAdd', run: () => edit(), disabled: loading.value || busy.value || servers.value.length >= maxServers.value, loading: false })) })
</script>

<template>
  <div class="settings-content mcp-settings" :aria-busy="loading">
    <div class="mcp-toolbar">
      <p class="mcp-hint">{{ t('mcpSettingsHint') }} <a href="https://modelcontextprotocol.io/docs/getting-started/intro" target="_blank" rel="noopener noreferrer">{{ t('mcpLearnMore') }}</a></p>
    </div>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error" :disabled="busy || loading" @click="load">{{ t('retry') }}</v-button>
    <p v-if="loading" role="status">{{ t('mcpLoading') }}</p>
    <v-empty v-else-if="!servers.length && !error" class="vista-empty-state mcp-empty" :description="t('mcpEmpty')" role="status" />
    <p v-if="saved" class="mcp-saved" role="status">{{ t('mcpSaved') }}</p>
    <article v-for="server in servers" :key="server.id" class="mcp-server" :data-mcp="server.name" :data-mcp-id="server.id">
      <div class="mcp-server-heading"><div class="mcp-name"><strong>{{ server.name }}</strong><span class="mcp-badge">{{ server.transport === 'stdio' ? 'stdio' : 'HTTP' }}</span></div></div>
      <p class="mcp-address">{{ server.transport === 'http' ? server.endpoint : [server.command, ...(server.args || [])].join(' ') }}</p>
      <div class="mcp-status" :class="{ available: !server.disabled && server.check.status === 'available', failed: !server.disabled && server.check.status === 'error' }"><span class="mcp-status-dot" aria-hidden="true"></span>{{ status(server) }}<span v-if="!server.disabled && server.check.status === 'available' && server.check.toolCount >= 0">· {{ t('mcpToolCount', { count: server.check.toolCount }) }}</span></div>
      <p v-if="server.check.error && !server.disabled" class="mcp-error">{{ server.check.error }}</p>
      <small v-if="checkedAt(server.check.checkedAt)" class="mcp-hint">{{ t('mcpCheckedAt', { time: checkedAt(server.check.checkedAt) }) }}</small>
      <div class="mcp-actions">
        <v-button :disabled="busy" @click="inspect(server)"><ReloadOutlined aria-hidden="true" />{{ t('mcpCheckTools') }}</v-button>
        <v-button v-if="server.oauth" :disabled="busy" @click="authorize(server)"><LinkOutlined aria-hidden="true" />{{ t(server.authorized ? 'mcpReauthorize' : 'mcpAuthorize') }}</v-button>
        <v-button class="icon-button" variant="text" :disabled="busy" :aria-label="t('mcpEditName', { name: server.name })" @click="edit(server)"><EditOutlined /></v-button>
        <ConfirmPopover :show="deleting === server.id" :message="t('mcpDeleteConfirm', { name: server.name })" :cancel-text="t('cancel')" :confirm-text="t('mcpDelete')" :busy="busy" :error="deleteError" popover-class="mcp-delete-popover" @update:show="deleting = $event ? server.id : ''; deleteError = ''" @confirm="remove(server)"><template #trigger><v-button class="icon-button" variant="text" danger :disabled="busy" :aria-label="t('mcpDeleteName', { name: server.name })"><DeleteOutlined /></v-button></template></ConfirmPopover>
      </div>
    </article>
    <p v-if="servers.length >= maxServers" class="mcp-hint">{{ t('mcpServerLimit', { count: maxServers }) }}</p>
  </div>

  <v-modal :open="editorOpen" centered :z-index="1200" width="min(560px, calc(100vw - 2rem))" :title="t(editing ? 'mcpEdit' : 'mcpAdd')" :footer="null" :closable="!busy" :keyboard="!busy" :mask-closable="!busy" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeEditor">
    <form v-if="editorOpen" class="mcp-form" @submit.prevent="save">
      <v-alert v-if="formError" type="error" role="alert" :message="formError" />
      <p id="mcp-public-config-hint" class="mcp-hint">{{ t('configEditorPrivateHint') }}</p>
      <SchemaForm id="mcp-config" ref="schemaEditor" :model-value="draft" :schema="schema" :uischema="uischema" :validate="validPublicDocument" :disabled="busy"
        :reset-key="editorResetKey" aria-describedby="mcp-public-config-hint" @update:model-value="updateDraft" @validity="editorValid = $event" />
      <template v-if="draft.transport === 'http'">
        <p v-if="draft.auth === 'token'" class="mcp-hint">{{ t('mcpTokenHint') }}</p>
        <p v-if="draft.auth === 'oauth'" class="mcp-hint">{{ t('mcpOAuthHint') }}</p>
        <SchemaForm id="mcp-credentials" v-model="credentialDocument" :schema="credentialSchema" :uischema="credentialUISchema" :disabled="busy" :reset-key="editorResetKey" @validity="credentialValid = $event" />
      </template>
      <div class="mcp-form-actions"><v-button :disabled="busy" @click="closeEditor">{{ t('cancel') }}</v-button><v-button type="submit" variant="primary" :loading="busy" :disabled="busy || !canSave">{{ t('saveChanges') }}</v-button></div>
    </form>
  </v-modal>

  <v-modal :open="!!toolsServer" centered :z-index="1200" width="min(640px, calc(100vw - 2rem))" :title="t('mcpToolsTitle', { name: toolsServer?.name })" :footer="null" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeTools">
    <section v-if="toolsServer" class="mcp-tools" :aria-busy="toolsLoading">
      <p class="mcp-hint">{{ t('mcpCheckHint') }}</p>
      <p v-if="toolsLoading" role="status">{{ t('mcpChecking') }}</p>
      <v-alert v-if="toolsError" type="error" role="alert" :message="toolsError" />
      <div class="mcp-actions"><v-button :loading="toolsLoading" @click="inspect(toolsServer)"><ReloadOutlined aria-hidden="true" />{{ t('mcpCheckAgain') }}</v-button><v-button v-if="toolsServer.oauth" @click="authorize(toolsServer)">{{ t('mcpAuthorize') }}</v-button></div>
      <template v-if="!toolsLoading && !toolsError">
        <p v-if="tools.length" role="status">{{ t('mcpToolCount', { count: tools.length }) }}</p>
        <v-input v-if="tools.length" v-model:value="toolQuery" :placeholder="t('mcpSearchTools')" :aria-label="t('mcpSearchTools')" allow-clear />
        <v-empty v-if="!visibleTools.length" class="vista-empty-state" :description="t(tools.length ? 'mcpNoMatchingTools' : 'mcpToolCount', { count: 0 })" role="status" />
        <article v-for="tool in visibleTools" :key="tool.name" class="mcp-tool"><h3>{{ tool.name }}</h3><p>{{ tool.description }}</p><details><summary>{{ t('mcpInputSchema') }}</summary><pre>{{ JSON.stringify(tool.inputSchema, null, 2) }}</pre></details></article>
      </template>
    </section>
  </v-modal>

  <v-modal :open="!!loginServer" centered :z-index="1200" width="min(480px, calc(100vw - 2rem))" :title="t('mcpLoginTitle', { name: loginServer?.name })" :footer="null" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeLogin">
    <div v-if="loginServer" class="mcp-login">
      <v-alert v-if="loginError" type="error" role="alert" :message="loginError" />
      <p v-if="loginActive" role="status">{{ t(login?.status === 'exchanging' || loginSubmitting ? 'mcpLoginExchanging' : login?.status === 'pending' ? 'mcpLoginPending' : 'mcpLoginPreparing') }}</p>
      <div v-if="authorizationURL && login?.status === 'pending'" class="mcp-authorization-actions">
        <v-button :href="authorizationURL" target="_blank" rel="noopener noreferrer">{{ t('mcpOpenAuthorization') }}</v-button>
        <CopyAction :text="authorizationURL" :label="t('mcpCopyAuthorization')" />
      </div>
      <p v-if="login && !loginActive" role="status">{{ t(({ connected: 'mcpLoginSuccess', canceled: 'mcpLoginCanceled', expired: 'mcpLoginExpired', failed: 'mcpLoginFailed' } as Record<string, string>)[login.status] || 'mcpLoginFailed') }}</p>
      <p v-if="loginActive" id="mcp-login-hint" class="mcp-hint">{{ t('mcpLoginHint') }}</p>
      <form v-if="login?.status === 'pending' || login?.status === 'exchanging'" class="mcp-form" autocomplete="off" @submit.prevent="submitCallback">
        <label for="mcp-callback-url">{{ t('mcpCallbackURL') }}<v-input id="mcp-callback-url" v-model:value="callbackURL" :aria-label="t('mcpCallbackURL')" aria-describedby="mcp-login-hint" type="text" autocomplete="off" autocapitalize="none" :spellcheck="false" :maxlength="8192" required :disabled="login.status !== 'pending' || loginSubmitting" /></label>
        <div class="mcp-form-actions"><v-button type="submit" variant="primary" :loading="loginSubmitting" :disabled="!canSubmitCallback">{{ t('mcpSubmitCallback') }}</v-button></div>
      </form>
      <div class="mcp-form-actions"><v-button v-if="!loginActive && login?.status !== 'connected'" @click="authorize(loginServer)">{{ t('retry') }}</v-button><v-button @click="closeLogin">{{ t(loginActive ? 'cancel' : 'close') }}</v-button></div>
    </div>
  </v-modal>
</template>

<style scoped>
.mcp-settings { display: flex; flex-direction: column; gap: 1rem; }
.mcp-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.mcp-toolbar a { white-space: nowrap; }
.mcp-hint { color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; margin: 0; }
.mcp-saved { color: var(--accent); margin: 0; font-size: var(--font-size-body); }
.mcp-server { min-width: 0; border: 1px solid var(--border); border-radius: 12px; padding: 1rem; }
.mcp-server-heading, .mcp-name { display: flex; align-items: center; gap: .6rem; }
.mcp-server-heading { justify-content: space-between; }
.mcp-name { min-width: 0; flex-wrap: wrap; overflow-wrap: anywhere; }
.mcp-badge { color: var(--muted); font-size: var(--font-size-small); padding: .1rem .4rem; background: var(--surface-subtle); border-radius: 5px; }
.mcp-address { color: var(--muted); margin: .6rem 0; font: var(--font-size-small)/1.5 var(--font-mono, monospace); overflow-wrap: anywhere; }
.mcp-status { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem; font-size: var(--font-size-body); color: var(--muted); }
.mcp-status.available { color: var(--accent); }
.mcp-status.failed, .mcp-error { color: var(--danger, #dc4446); }
.mcp-status-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.mcp-error { font-size: var(--font-size-body); white-space: pre-wrap; overflow-wrap: anywhere; margin: .5rem 0; }
.mcp-actions { display: flex; flex-wrap: wrap; gap: .4rem; margin-top: .8rem; }
.mcp-form { display: flex; flex-direction: column; gap: 1rem; }
.mcp-form label { display: flex; flex-direction: column; gap: .4rem; min-width: 0; }
.mcp-oauth-advanced { display: flex; flex-direction: column; gap: .8rem; }
.mcp-oauth-advanced summary { cursor: pointer; color: var(--muted); font-size: var(--font-size-body); margin-bottom: .6rem; }
.mcp-oauth-advanced label { margin-top: .8rem; }
.mcp-oauth-advanced > button { margin-top: .6rem; }
.mcp-form-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: .5rem; margin-top: .5rem; }
.mcp-tool { padding: 1rem 0; border-bottom: 1px solid var(--border); }
.mcp-tool:last-child { border: none; }
.mcp-tool h3 { margin: 0; font-size: var(--font-size-body); overflow-wrap: anywhere; }
.mcp-tool p { white-space: pre-wrap; overflow-wrap: anywhere; color: var(--muted); font-size: var(--font-size-body); }
.mcp-tool summary { cursor: pointer; color: var(--muted); font-size: var(--font-size-small); }
.mcp-tool pre { white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--font-size-small); margin-bottom: 0; }
.mcp-login { display: flex; flex-direction: column; align-items: stretch; gap: 1rem; }
.mcp-login p { margin: 0; }
.mcp-authorization-actions { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
@media (max-width: 480px) {
  .mcp-toolbar { flex-direction: column; gap: .7rem; }
  .mcp-server { padding: .8rem; }
}
</style>
