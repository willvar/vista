<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Input as AInput, InputPassword as AInputPassword, Modal as AModal, Select as ASelect, Switch as ASwitch, TextArea as ATextarea } from 'antdv-next'
import { EditOutlined, LinkOutlined, ReloadOutlined, DeleteOutlined } from '@antdv-next/icons'
import ConfirmPopover from './ConfirmPopover.vue'
import { VistaAPIError } from './client/vista'
import { useWorkspace } from './composables/useWorkspace'
import { formatDateTime } from './ui/datetime.ts'
import type { MCPCheck as Check, MCPServer, MCPConfig, LoginUpdate } from './client/types'

type Tool = { name: string; description: string; inputSchema: unknown }
type Login = { id: string; name: string; status: string; authorizationUrl?: string; error?: string; expiresAt: string }
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
const draft = reactive({ name: '', transport: 'http', command: '', args: '', directory: '', endpoint: '', auth: 'none', token: '', clientId: '', clientSecret: '', clearClientSecret: false, disabled: false, headers: [] as { name: string; value: string }[] })
const transportOptions = computed(() => [{ label: t('mcpHTTP'), value: 'http' }, { label: t('mcpStdio'), value: 'stdio' }])
const argsExample = '-y\n@modelcontextprotocol/server-filesystem\n/home/user/project'
const authOptions = computed(() => [{ label: t('mcpAuthNone'), value: 'none' }, { label: t('mcpBearerToken'), value: 'token' }, { label: 'OAuth', value: 'oauth' }])
const canSave = computed(() => {
  if (!draft.name.trim() || [...draft.name.trim()].length > 64 || /\p{Cc}/u.test(draft.name)) return false
  if (draft.transport === 'stdio') return !!draft.command.trim() && !!draft.directory.trim()
  const names = draft.headers.map(header => header.name.trim().toLowerCase()).filter(Boolean)
  if (new Set(names).size !== names.length || draft.headers.some(header => !header.name.trim() && header.value)) return false
  return !!draft.endpoint.trim() && (draft.auth !== 'token' || !!draft.token.trim() || !!editing.value?.hasToken)
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
  Object.assign(draft, {
    name: server?.name || '', transport: server?.transport || 'http',
    command: server?.command || '', args: server?.args?.join('\n') || '',
    directory: server?.directory || defaultDirectory.value, endpoint: server?.endpoint || '',
    auth: server?.oauth ? 'oauth' : server?.hasToken ? 'token' : 'none',
    token: '', clientId: server?.clientId || '', clientSecret: '', clearClientSecret: false,
    headers: (server?.headerNames || []).map(name => ({ name, value: '' })),
    disabled: !!server?.disabled,
  })
  formError.value = ''
  editorOpen.value = true
}
function closeEditor() {
  if (busy.value) return
  editorOpen.value = false
  draft.token = draft.clientSecret = ''
  draft.headers = []
}
function canReuseHeader(name: string) {
  return editing.value?.headerNames?.some(saved => saved.toLowerCase() === name.trim().toLowerCase())
}
async function save() {
  if (busy.value || !canSave.value) return
  busy.value = true
  formError.value = ''
  error.value = ''
  saved.value = false
  const http = draft.transport === 'http'
  const body = {
    ...(editing.value ? { id: editing.value.id } : {}),
    name: draft.name.trim(), transport: draft.transport, disabled: draft.disabled,
    ...(http ? { endpoint: draft.endpoint.trim(), oauth: draft.auth === 'oauth',
      token: draft.auth === 'token' ? draft.token.trim() : '', clearToken: draft.auth !== 'token',
      clientId: draft.auth === 'oauth' ? draft.clientId.trim() : '',
      clientSecret: draft.auth === 'oauth' ? draft.clientSecret.trim() : '', clearClientSecret: draft.clearClientSecret,
      headers: Object.fromEntries(draft.headers.filter(header => header.name.trim()).map(header => [header.name.trim(), header.value])),
    } : { command: draft.command.trim(), directory: draft.directory.trim(), args: draft.args ? draft.args.split('\n') : [] }),
  }
  try {
    await client.request('/mcp-config', { method: editing.value ? 'PUT' : 'POST', body: JSON.stringify(body), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    editorOpen.value = false
    draft.token = draft.clientSecret = ''
    draft.headers = []
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) formError.value = String(cause) }
  finally { busy.value = false }
}
async function toggle(server: MCPServer, enabled: boolean) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    await client.request('/mcp-config', { method: 'PATCH', body: JSON.stringify({ id: server.id, disabled: !enabled }), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    server.disabled = !enabled
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) error.value = String(cause) }
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
  return t(({ available: 'mcpAvailable', error: 'mcpUnavailable', authorization_required: 'mcpAuthorizationRequired' } as Record<string, string>)[server.check.status] || 'mcpUnchecked')
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
let loginGeneration = 0
let loginController: AbortController | undefined
const loginActive = computed(() => login.value ? ['starting', 'pending'].includes(login.value.status) : !loginStartFailed.value)
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
async function receiveLogin(update: LoginUpdate, signal = loginController?.signal) {
  if (!login.value || update.id !== login.value.id) return
  loginReadRevision++
  login.value = { ...login.value, ...update }
  loginError.value = update.error || ''
  if (update.status === 'connected') saved.value = true
  if (!['starting', 'pending'].includes(update.status)) login.value.authorizationUrl = ''
  else if (update.status === 'pending' && !login.value.authorizationUrl) await readLogin(loginGeneration, signal, true)
}
async function readLogin(generation: number, signal = loginController?.signal, propagate = false) {
  const revision = ++loginReadRevision
  const id = login.value?.id
  if (!id || generation !== loginGeneration) return
  try {
    const result = await client.request<Login>(`/mcp-auth/${encodeURIComponent(id)}`, { signal })
    if (lifetime.signal.aborted || signal?.aborted || generation !== loginGeneration || revision !== loginReadRevision) return
    login.value = result
    loginError.value = result.error || ''
    if (result.status === 'connected') saved.value = true
  } catch (cause) {
    if (lifetime.signal.aborted || signal?.aborted || generation !== loginGeneration || revision !== loginReadRevision) return
    loginError.value = String(cause)
    if (cause instanceof VistaAPIError && cause.status === 404) { login.value = null; loginStartFailed.value = true; return }
    if (propagate) throw cause
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
    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <a-button v-if="error" :disabled="busy || loading" @click="load">{{ t('retry') }}</a-button>
    <p v-if="loading" role="status">{{ t('mcpLoading') }}</p>
    <p v-else-if="!servers.length && !error" class="mcp-empty">{{ t('mcpEmpty') }}</p>
    <p v-if="saved" class="mcp-saved" role="status">{{ t('mcpSaved') }}</p>
    <article v-for="server in servers" :key="server.id" class="mcp-server" :data-mcp="server.name" :data-mcp-id="server.id">
      <div class="mcp-server-heading"><div class="mcp-name"><strong>{{ server.name }}</strong><span class="mcp-badge">{{ server.transport === 'stdio' ? 'stdio' : 'HTTP' }}</span></div><a-switch :checked="!server.disabled" :disabled="busy" :aria-label="t('mcpEnable', { name: server.name })" @update:checked="toggle(server, !!$event)" /></div>
      <p class="mcp-address">{{ server.transport === 'http' ? server.endpoint : [server.command, ...(server.args || [])].join(' ') }}</p>
      <div class="mcp-status" :class="{ available: !server.disabled && server.check.status === 'available', failed: !server.disabled && server.check.status === 'error' }"><span class="mcp-status-dot" aria-hidden="true"></span>{{ status(server) }}<span v-if="server.check.status === 'available'">· {{ t('mcpToolCount', { count: server.check.toolCount }) }}</span></div>
      <p v-if="server.check.error && !server.disabled" class="mcp-error">{{ server.check.error }}</p>
      <small v-if="checkedAt(server.check.checkedAt)" class="mcp-hint">{{ t('mcpCheckedAt', { time: checkedAt(server.check.checkedAt) }) }}</small>
      <div class="mcp-actions">
        <a-button size="small" :disabled="busy" @click="inspect(server)"><ReloadOutlined aria-hidden="true" />{{ t('mcpCheckTools') }}</a-button>
        <a-button v-if="server.oauth" size="small" :disabled="busy" @click="authorize(server)"><LinkOutlined aria-hidden="true" />{{ t(server.authorized ? 'mcpReauthorize' : 'mcpAuthorize') }}</a-button>
        <a-button type="text" size="small" :disabled="busy" :aria-label="t('mcpEditName', { name: server.name })" @click="edit(server)"><EditOutlined /></a-button>
        <ConfirmPopover :show="deleting === server.id" :message="t('mcpDeleteConfirm', { name: server.name })" :cancel-text="t('cancel')" :confirm-text="t('mcpDelete')" :busy="busy" :error="deleteError" popover-class="mcp-delete-popover" @update:show="deleting = $event ? server.id : ''; deleteError = ''" @confirm="remove(server)"><template #trigger><a-button type="text" size="small" danger :disabled="busy" :aria-label="t('mcpDeleteName', { name: server.name })"><DeleteOutlined /></a-button></template></ConfirmPopover>
      </div>
    </article>
    <p v-if="servers.length >= maxServers" class="mcp-hint">{{ t('mcpServerLimit', { count: maxServers }) }}</p>
  </div>

  <a-modal :open="editorOpen" centered :z-index="1200" width="min(560px, calc(100vw - 2rem))" :title="t(editing ? 'mcpEdit' : 'mcpAdd')" :footer="null" :closable="!busy" :keyboard="!busy" :mask-closable="!busy" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeEditor">
    <form v-if="editorOpen" class="mcp-form" @submit.prevent="save">
      <a-alert v-if="formError" type="error" role="alert" :message="formError" />
      <label for="mcp-name">{{ t('mcpName') }}<a-input id="mcp-name" v-model:value="draft.name" :aria-label="t('mcpName')" aria-describedby="mcp-name-hint" placeholder="my-server" :maxlength="64" required :disabled="busy" /><small id="mcp-name-hint" class="mcp-hint">{{ t('mcpNameHint') }}</small></label>
      <label for="mcp-transport">{{ t('mcpTransport') }}<a-select id="mcp-transport" v-model:value="draft.transport" :options="transportOptions" :virtual="false" :disabled="busy" :aria-label="t('mcpTransport')" /></label>
      <template v-if="draft.transport === 'stdio'">
        <label for="mcp-command">{{ t('mcpCommand') }}<a-input id="mcp-command" v-model:value="draft.command" placeholder="/usr/bin/npx" required :disabled="busy" /></label>
        <label for="mcp-args">{{ t('mcpArguments') }}<a-textarea id="mcp-args" v-model:value="draft.args" :aria-label="t('mcpArguments')" aria-describedby="mcp-args-hint" :auto-size="{ minRows: 3, maxRows: 8 }" :placeholder="argsExample" :disabled="busy" /><small id="mcp-args-hint" class="mcp-hint">{{ t('mcpArgsHint') }}</small></label>
        <label for="mcp-directory">{{ t('mcpDirectory') }}<a-input id="mcp-directory" v-model:value="draft.directory" placeholder="/home/user/project" required :disabled="busy" /></label>
      </template>
      <template v-else>
        <label for="mcp-endpoint">{{ t('mcpEndpoint') }}<a-input id="mcp-endpoint" v-model:value="draft.endpoint" :aria-label="t('mcpEndpoint')" aria-describedby="mcp-endpoint-hint" placeholder="https://example.com/mcp" :maxlength="2048" required :disabled="busy" /><small id="mcp-endpoint-hint" class="mcp-hint">{{ t('mcpEndpointHint') }}</small></label>
        <label for="mcp-auth">{{ t('providerAuthentication') }}<a-select id="mcp-auth" v-model:value="draft.auth" :options="authOptions" :virtual="false" :disabled="busy" :aria-label="t('providerAuthentication')" /></label>
        <label v-if="draft.auth === 'token'" for="mcp-token">{{ t('mcpBearerToken') }}<a-input-password id="mcp-token" v-model:value="draft.token" :aria-label="t('mcpBearerToken')" aria-describedby="mcp-token-hint" :maxlength="4096" :placeholder="editing?.hasToken ? t('reuseSavedKey') : ''" :disabled="busy" /><small id="mcp-token-hint" class="mcp-hint">{{ t('mcpTokenHint') }}</small></label>
        <template v-if="draft.auth === 'oauth'">
          <p class="mcp-hint">{{ t('mcpOAuthHint') }}</p>
          <details class="mcp-oauth-advanced"><summary>{{ t('mcpOAuthAdvanced') }}</summary>
            <label for="mcp-client-id">Client ID<a-input id="mcp-client-id" v-model:value="draft.clientId" :maxlength="1024" :disabled="busy" /></label>
            <label for="mcp-client-secret">Client secret<a-input-password id="mcp-client-secret" v-model:value="draft.clientSecret" :maxlength="4096" :disabled="busy" :placeholder="editing?.hasClientSecret && !draft.clearClientSecret ? t('reuseSavedKey') : ''" /></label>
            <a-button v-if="editing?.hasClientSecret" size="small" :disabled="busy" @click="draft.clearClientSecret = !draft.clearClientSecret; draft.clientSecret = ''">{{ t(draft.clearClientSecret ? 'mcpKeepSecret' : 'mcpClearSecret') }}</a-button>
          </details>
        </template>
        <details class="provider-advanced" :open="editing?.headerNames?.length ? true : undefined">
          <summary>{{ t('advancedSettings') }}</summary>
          <div class="provider-headers">
            <div v-for="(header, index) in draft.headers" :key="index" class="provider-header-row">
              <label>{{ t('headerName') }}<a-input v-model:value="header.name" :aria-label="`${t('headerName')} ${index + 1}`" placeholder="X-API-Key" :maxlength="256" :disabled="busy" /></label>
              <label>{{ t('headerValue') }}<a-input-password v-model:value="header.value" :aria-label="`${t('headerValue')} ${index + 1}`" :placeholder="canReuseHeader(header.name) ? t('reuseSavedHeader') : t('headerValue')" :maxlength="8192" :disabled="busy" /></label>
              <a-button type="text" html-type="button" :disabled="busy" :aria-label="`${t('removeHeader')} ${index + 1}`" @click="draft.headers.splice(index, 1)">×</a-button>
            </div>
            <a-button html-type="button" size="small" :disabled="busy || draft.headers.length >= 32" @click="draft.headers.push({ name: '', value: '' })">{{ t('addHeader') }}</a-button>
          </div>
        </details>
      </template>
      <div class="mcp-form-actions"><a-button :disabled="busy" @click="closeEditor">{{ t('cancel') }}</a-button><a-button html-type="submit" type="primary" :loading="busy" :disabled="busy || !canSave">{{ t('saveChanges') }}</a-button></div>
    </form>
  </a-modal>

  <a-modal :open="!!toolsServer" centered :z-index="1200" width="min(640px, calc(100vw - 2rem))" :title="t('mcpToolsTitle', { name: toolsServer?.name })" :footer="null" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeTools">
    <section v-if="toolsServer" class="mcp-tools" :aria-busy="toolsLoading">
      <p class="mcp-hint">{{ t('mcpCheckHint') }}</p>
      <p v-if="toolsLoading" role="status">{{ t('mcpChecking') }}</p>
      <a-alert v-if="toolsError" type="error" role="alert" :message="toolsError" />
      <div class="mcp-actions"><a-button :loading="toolsLoading" @click="inspect(toolsServer)"><ReloadOutlined aria-hidden="true" />{{ t('mcpCheckAgain') }}</a-button><a-button v-if="toolsServer.oauth" @click="authorize(toolsServer)">{{ t('mcpAuthorize') }}</a-button></div>
      <template v-if="!toolsLoading && !toolsError">
        <p role="status">{{ t('mcpToolCount', { count: tools.length }) }}</p>
        <a-input v-if="tools.length" v-model:value="toolQuery" :placeholder="t('mcpSearchTools')" :aria-label="t('mcpSearchTools')" allow-clear />
        <p v-if="tools.length && !visibleTools.length" class="mcp-hint">{{ t('mcpNoMatchingTools') }}</p>
        <article v-for="tool in visibleTools" :key="tool.name" class="mcp-tool"><h3>{{ tool.name }}</h3><p>{{ tool.description }}</p><details><summary>{{ t('mcpInputSchema') }}</summary><pre>{{ JSON.stringify(tool.inputSchema, null, 2) }}</pre></details></article>
      </template>
    </section>
  </a-modal>

  <a-modal :open="!!loginServer" centered :z-index="1200" width="min(480px, calc(100vw - 2rem))" :title="t('mcpLoginTitle', { name: loginServer?.name })" :footer="null" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeLogin">
    <div v-if="loginServer" class="mcp-login">
      <a-alert v-if="loginError" type="error" role="alert" :message="loginError" />
      <p v-if="loginActive" role="status">{{ t(authorizationURL ? 'mcpLoginPending' : 'mcpLoginPreparing') }}</p>
      <a-button v-if="authorizationURL && loginActive" type="primary" :href="authorizationURL" target="_blank" rel="noopener noreferrer">{{ t('mcpOpenAuthorization') }}</a-button>
      <p v-if="login && !loginActive" role="status">{{ t(({ connected: 'mcpLoginSuccess', canceled: 'mcpLoginCanceled', expired: 'mcpLoginExpired', failed: 'mcpLoginFailed' } as Record<string, string>)[login.status] || 'mcpLoginFailed') }}</p>
      <p v-if="loginActive" class="mcp-hint">{{ t('mcpLoginHint') }}</p>
      <div class="mcp-form-actions"><a-button v-if="!loginActive && login?.status !== 'connected'" @click="authorize(loginServer)">{{ t('retry') }}</a-button><a-button @click="closeLogin">{{ t(loginActive ? 'cancel' : 'close') }}</a-button></div>
    </div>
  </a-modal>
</template>

<style scoped>
.mcp-settings { display: flex; flex-direction: column; gap: 1rem; }
.mcp-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.mcp-toolbar a { white-space: nowrap; }
.mcp-hint { color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; margin: 0; }
.mcp-empty { color: var(--muted); text-align: center; padding: 2rem 1rem; }
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
.mcp-form .ant-select { width: 100%; }
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
@media (max-width: 480px) {
  .mcp-toolbar { flex-direction: column; gap: .7rem; }
  .mcp-server { padding: .8rem; }
}
</style>
