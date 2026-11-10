import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ControlElement, JsonSchema7, UISchemaElement } from '@jsonforms/core'
import { VistaAPIError, type VistaClient } from '../client/vista'
import type { createWorkspaceSync } from '../client/workspaceSync'
import type { AgentSettings, ModelCatalog, ModelInfo, ModelSetting, ModelSettings, ProviderSetup } from '../client/types'
import { languages } from '../i18n/languages.ts'
import { readLanguagePreference, setLanguagePreference, type LanguagePreference } from '../i18n'

type SettingsDependencies = {
  client: VistaClient
  error: Ref<string>
  stateSync: ReturnType<typeof createWorkspaceSync>
  modelCatalog: Ref<ModelCatalog>
}

export function useProviderSettings({ client, error, stateSync, modelCatalog }: SettingsDependencies) {
  const { t, locale } = useI18n({ useScope: 'global' })
  function api<T = unknown>(path: string, options?: RequestInit) { return client.request<T>(path, options) }
  const languagePreference = ref<LanguagePreference>(readLanguagePreference())
  const languageChoice = computed({
    get: () => languages.find(language => language.value === languagePreference.value)?.language ?? 'auto',
    set: (value: string) => {
      if (value === 'auto') languagePreference.value = 'auto'
      else {
        const first = languages.find(language => language.language === value)
        if (first) languagePreference.value = first.value
      }
    },
  })
  const languageOptions = computed(() => [
    { label: t('automaticLanguage'), value: 'auto', searchText: `${t('automaticLanguage')} Auto 自动 自動` },
    ...[...new Set(languages.map(language => language.language))].map(value => {
      const variants = languages.filter(language => language.language === value)
      const label = variants[0]!.label
      return { label, value, searchText: `${label} ${label.normalize('NFD').replace(/\p{M}/gu, '')} ${variants.map(language => `${language.value} ${language.aliases}`).join(' ')}` }
    }),
  ])
  const regionOptions = computed(() => {
    const names = new Intl.DisplayNames([locale.value], { type: 'region' })
    return languages.filter(language => language.language === languageChoice.value).map(language => {
      const label = names.of(language.region)!
      return { label, value: language.value, searchText: `${label} ${language.region}` }
    })
  })
  let languageRevision = 0
  watch(languagePreference, async preference => {
    const revision = ++languageRevision
    try { await setLanguagePreference(preference) }
    catch (cause) {
      if (revision !== languageRevision) return
      window.dispatchEvent(new CustomEvent('vista:asset-error', { detail: cause }))
    }
  })

  const settingsBusy = ref(false)
  const settingsError = ref('')
  const visibilityBusy = ref(false)
  const showUnrecognizedModels = computed(() => !!modelCatalog.value.showUnrecognizedModels)
  async function setShowUnrecognizedModels(enabled: boolean) {
    if (visibilityBusy.value) return
    visibilityBusy.value = true
    settingsError.value = ''
    try { await api('/preferences', { method: 'PATCH', body: JSON.stringify({ showUnrecognizedModels: enabled }) }) }
    catch (cause) { settingsError.value = String(cause) }
    finally { visibilityBusy.value = false }
  }
  const providerDisconnectTarget = ref<{ key: string; name: string } | null>(null)
  const providerDisconnectError = ref('')
  const settings = ref<ModelSettings>({ default: '', models: [] })
  const agentInstructions = ref('')
  const agentInstructionsPath = ref('')
  const savedAgentInstructions = ref('')
  const delegationDepth = ref(1)
  const savedDelegationDepth = ref(1)
  const maxDelegationDepth = ref(8)
  const automaticTitles = ref(true)
  const savedAutomaticTitles = ref(true)
  const agentSettingsBusy = ref(false)
  const agentSettingsLoading = ref(false)
  const agentSettingsError = ref('')
  const agentSettingsSaved = ref(false)
  const agentSettingsResetKey = ref(0)
  type AgentField = 'instructions' | 'delegationDepth' | 'automaticTitles'
  const agentSettingsResetKeys = reactive<Record<AgentField, number>>({ instructions: 0, delegationDepth: 0, automaticTitles: 0 })
  function canSaveAgentField(field: AgentField) {
    return !agentSettingsLoading.value && !agentSettingsBusy.value && (field === 'instructions' ? agentInstructions.value !== savedAgentInstructions.value : field === 'delegationDepth' ? delegationDepth.value !== savedDelegationDepth.value : automaticTitles.value !== savedAutomaticTitles.value)
  }
  const agentSettingsDraft = computed<Record<string, unknown>>({
    get: () => ({ instructions: agentInstructions.value, delegationDepth: delegationDepth.value, automaticTitles: automaticTitles.value }),
    set: value => {
      if (typeof value.instructions === 'string') agentInstructions.value = value.instructions
      if (typeof value.delegationDepth === 'number') delegationDepth.value = value.delegationDepth
      if (typeof value.automaticTitles === 'boolean') automaticTitles.value = value.automaticTitles
    },
  })
  const agentSettingsSchema = computed<JsonSchema7>(() => ({
    type: 'object', additionalProperties: false,
    required: ['instructions', 'delegationDepth', 'automaticTitles'],
    properties: {
      instructions: { type: 'string', title: t('globalInstructions') },
      delegationDepth: { type: 'integer', minimum: 0, maximum: maxDelegationDepth.value, title: t('delegationDepth'), description: t('delegationDepthHint', { max: maxDelegationDepth.value }) },
      automaticTitles: { type: 'boolean', title: t('automaticTitles'), description: t('automaticTitlesHint') },
    },
  }))
  watch([agentInstructions, delegationDepth, automaticTitles], () => { agentSettingsSaved.value = false }, { flush: 'sync' })
  let agentSettingsRevision = 0
  onScopeDispose(() => { agentSettingsRevision++ })
  async function loadAgentSettings() {
    const revision = ++agentSettingsRevision
    agentSettingsLoading.value = true
    agentSettingsError.value = ''
    agentSettingsSaved.value = false
    try {
      const result = await api<AgentSettings>('/agent-settings')
      if (revision !== agentSettingsRevision) return
      agentInstructions.value = result.instructions
      agentInstructionsPath.value = result.instructionsPath
      savedAgentInstructions.value = result.instructions
      delegationDepth.value = result.delegationDepth
      savedDelegationDepth.value = result.delegationDepth
      maxDelegationDepth.value = result.maxDelegationDepth
      automaticTitles.value = result.automaticTitles
      savedAutomaticTitles.value = result.automaticTitles
      agentSettingsResetKey.value++
      for (const field of ['instructions', 'delegationDepth', 'automaticTitles'] as const) agentSettingsResetKeys[field]++
    } catch (e) { if (revision === agentSettingsRevision) agentSettingsError.value = String(e) }
    finally { if (revision === agentSettingsRevision) agentSettingsLoading.value = false }
  }
  async function saveAgentSettings(field: AgentField) {
    if (!canSaveAgentField(field)) return
    // Keep partial-update semantics: unchanged fields may have been edited elsewhere.
    const input: Partial<Pick<AgentSettings, 'instructions' | 'delegationDepth' | 'automaticTitles'>> = {}
    if (field === 'instructions' && agentInstructions.value !== savedAgentInstructions.value) input.instructions = agentInstructions.value
    if (field === 'delegationDepth' && delegationDepth.value !== savedDelegationDepth.value) input.delegationDepth = delegationDepth.value
    if (field === 'automaticTitles' && automaticTitles.value !== savedAutomaticTitles.value) input.automaticTitles = automaticTitles.value
    const revision = agentSettingsRevision
    agentSettingsBusy.value = true
    agentSettingsError.value = ''
    agentSettingsSaved.value = false
    try {
      await api('/agent-settings', { method: 'PUT', body: JSON.stringify(input) })
      if (revision !== agentSettingsRevision) return
      if (input.instructions !== undefined) savedAgentInstructions.value = input.instructions
      if (input.delegationDepth !== undefined) savedDelegationDepth.value = input.delegationDepth
      if (input.automaticTitles !== undefined) savedAutomaticTitles.value = input.automaticTitles
      agentSettingsResetKey.value++
      for (const key of Object.keys(input) as AgentField[]) agentSettingsResetKeys[key]++
      agentSettingsSaved.value = true
    } catch (e) { if (revision === agentSettingsRevision) agentSettingsError.value = String(e) }
    finally { agentSettingsBusy.value = false }
  }
  const emptyDraft = () => ({ key: '', kind: '', apiFormat: 'openai-chat', headers: [] as { name: string; value: string }[], copilotAuth: { enterpriseUrl: '', clientId: '' }, name: '', providerId: '', baseUrl: '', apiKey: '', credentialMode: 'apiKey', accessToken: '', region: '', project: '', location: '', resourceName: '', deploymentId: '', apiVersion: '' })
  const modelDraft = ref(emptyDraft())
  const providerSetups = ref<ProviderSetup[]>([])
  const modelFormOpen = ref(false)
  const chosenModels = ref<string[]>([])
  const manualModelIDs = ref(false)
  let draftRevision = 0
  let connectionRequest: AbortController | undefined
  onScopeDispose(() => { draftRevision++; connectionRequest?.abort() })
  watch(modelDraft, () => { draftRevision++ }, { deep: true, flush: 'sync' })
  watch(() => [modelDraft.value.baseUrl, modelDraft.value.apiFormat, modelDraft.value.apiKey, modelDraft.value.credentialMode, JSON.stringify(modelDraft.value.headers)], () => {
    if (modelDraft.value.kind !== 'custom') return
    manualModelIDs.value = false
    chosenModels.value = []
    settingsError.value = ''
  }, { flush: 'sync' })
  const editingProvider = computed(() => settings.value.models.find(model => model.key === modelDraft.value.key))
  const providerOptions = computed(() => providerSetups.value
    .map(setup => ({ label: setup.id === 'custom' ? t('customProvider') : setup.name, value: setup.id, searchText: `${setup.id} ${setup.name} ${setup.aliases || ''} ${setup.id === 'custom' ? t('customProvider') : ''}` }))
    .sort((a, b) => a.label.localeCompare(b.label, 'en', { sensitivity: 'base' })))
  const providerChoice = computed<Record<string, unknown>>({ get: () => ({ kind: modelDraft.value.kind }), set: value => { if (typeof value.kind === 'string' && value.kind !== modelDraft.value.kind) changeProvider(value.kind) } })
  const providerChoiceSchema = computed<JsonSchema7>(() => ({ type: 'object', additionalProperties: false, required: ['kind'], properties: { kind: { type: 'string', title: t('provider'), enum: ['', ...providerOptions.value.map(option => option.value)] } } }))
  const providerChoiceUISchema = computed<ControlElement>(() => ({ type: 'Control', scope: '#/properties/kind', options: {
    showSearch: true, placeholder: t('chooseProvider'), enumLabels: { '': t('chooseProvider'), ...Object.fromEntries(providerOptions.value.map(option => [option.value, option.label])) },
    enumSearchText: Object.fromEntries(providerOptions.value.map(option => [option.value, option.searchText])),
  } }))
  const selectedSetup = computed(() => providerSetups.value.find(setup => setup.id === modelDraft.value.kind))
  const deploymentModelSelection = computed(() => modelDraft.value.kind === 'azure')
  const providerModelOptions = computed(() => selectedSetup.value?.models?.map(model => ({ label: model.name || model.id, value: model.id, searchText: `${model.name} ${model.id}` })) || [])
  const apiFormatLabels: Record<string, string> = { 'openai-chat': 'OpenAI Chat Completions', 'openai-responses': 'OpenAI Responses', anthropic: 'Anthropic Messages', gemini: 'Google Gemini generateContent', cohere: 'Cohere Chat v2' }
  const apiFormatOptions = computed(() => (selectedSetup.value?.apiFormats || []).map(value => ({ value, label: apiFormatLabels[value] || value })))
  const credentialOptions = computed(() => (selectedSetup.value?.credentialModes || []).map(mode => ({ value: mode, label: mode === 'bearer' ? 'Bearer' : mode === 'x-api-key' || mode === 'x-goog-api-key' ? mode : t(mode === 'apiKey' ? 'apiKey' : mode === 'none' ? 'noAuthentication' : mode === 'accessToken' ? 'providerAccessToken' : modelDraft.value.kind === 'amazon-bedrock' ? 'awsDefaultCredentials' : modelDraft.value.kind === 'google-vertex' ? 'googleDefaultCredentials' : 'noAuthentication') })))
  const usesAPIKey = computed(() => modelDraft.value.credentialMode === 'apiKey' || modelDraft.value.kind === 'custom' && modelDraft.value.credentialMode !== 'none')
  const authenticationHeader = computed(() => modelDraft.value.credentialMode === 'none' ? '' : modelDraft.value.credentialMode === 'bearer' ? 'Authorization: Bearer API_KEY' : `${modelDraft.value.credentialMode}: API_KEY`)
  const modelConfigValid = ref(true)
  const modelConfigDirty = ref(false)
  const modelConfigResetKey = ref(0)
  // This allowlist is deliberately separate from the credential-bearing connection draft.
  const publicModelFields = {
    name: { label: 'providerName', maxLength: 64 },
    baseUrl: { label: 'baseUrl', maxLength: 2048 },
    region: { label: 'providerRegion', maxLength: 64 },
    project: { label: 'googleCloudProject', maxLength: 128 },
    location: { label: 'providerLocation', maxLength: 64 },
    resourceName: { label: 'azureResourceName', maxLength: 128 },
    deploymentId: { label: 'azureDeploymentName', maxLength: 128 },
    apiVersion: { label: 'azureApiVersion', maxLength: 32 },
  } as const
  type PublicModelField = keyof typeof publicModelFields
  const modelConfigFields = computed(() => ['name', ...(selectedSetup.value?.fields || [])]
    .filter((field): field is PublicModelField => Object.hasOwn(publicModelFields, field)
      && (field !== 'baseUrl' || modelDraft.value.credentialMode !== 'oauth')))
  const modelConfigSchema = computed<JsonSchema7>(() => {
    const properties: Record<string, JsonSchema7> = {}
    for (const field of modelConfigFields.value) {
      properties[field] = { type: 'string', title: t(publicModelFields[field].label), maxLength: publicModelFields[field].maxLength }
      if (field === 'baseUrl' || field === 'resourceName' || field === 'deploymentId') properties[field]!.pattern = '\\S'
    }
    if (modelDraft.value.kind === 'custom') properties.apiFormat = { type: 'string', title: t('conversationAPIFormat'), enum: apiFormatOptions.value.map(option => option.value) }
    if (credentialOptions.value.length && modelDraft.value.credentialMode !== 'oauth') {
      properties.credentialMode = { type: 'string', title: t('providerAuthentication'), enum: credentialOptions.value.map(option => option.value) }
    }
    if (modelDraft.value.kind === 'github-copilot') properties.copilotAuth = {
      type: 'object', title: t('advancedSettings'), additionalProperties: false, required: ['enterpriseUrl', 'clientId'],
      properties: {
        enterpriseUrl: { type: 'string', title: t('copilotEnterpriseURL'), maxLength: 2048 },
        clientId: { type: 'string', title: 'OAuth Client ID', maxLength: 200 },
      },
    }
    return {
      type: 'object', additionalProperties: false, properties, required: Object.keys(properties),
      ...(modelDraft.value.kind === 'google-vertex' ? {
        if: { properties: { credentialMode: { not: { const: 'apiKey' } } }, required: ['credentialMode'] },
        then: { properties: { project: { pattern: '\\S' } } },
      } : {}),
    }
  })
  const modelConfigUISchema = computed<UISchemaElement>(() => {
    const control = (field: string, options?: Record<string, unknown>): ControlElement => ({ type: 'Control', scope: `#/properties/${field}`, ...(options ? { options } : {}) })
    const elements: UISchemaElement[] = modelConfigFields.value.map(field => control(field, {
      placeholder: field === 'name' ? modelDraft.value.kind === 'custom' ? t('providerNameExample') : selectedSetup.value?.name
        : field === 'baseUrl' ? selectedSetup.value?.baseUrl || 'https://provider.example/v1' : undefined,
    }))
    if (modelDraft.value.kind === 'custom') elements.push(control('apiFormat', { enumLabels: Object.fromEntries(apiFormatOptions.value.map(option => [option.value, option.label])) }))
    if (credentialOptions.value.length && modelDraft.value.credentialMode !== 'oauth') elements.push(control('credentialMode', { enumLabels: Object.fromEntries(credentialOptions.value.map(option => [option.value, option.label])) }))
    if (modelDraft.value.kind === 'github-copilot') elements.push({
      type: 'Group', label: t('advancedSettings'), elements: [
        control('copilotAuth/properties/enterpriseUrl', { placeholder: 'company.ghe.com' }),
        control('copilotAuth/properties/clientId', { placeholder: t('copilotDefaultClient') }),
      ],
    } as UISchemaElement)
    return { type: 'VerticalLayout', elements }
  })
  const modelConfig = computed<Record<string, unknown>>({
    get: () => {
      const form = modelDraft.value
      const value: Record<string, unknown> = Object.fromEntries(modelConfigFields.value.map(field => [field, form[field]]))
      if (modelDraft.value.kind === 'custom') value.apiFormat = form.apiFormat
      if (credentialOptions.value.length && form.credentialMode !== 'oauth') value.credentialMode = form.credentialMode
      if (form.kind === 'github-copilot') value.copilotAuth = { enterpriseUrl: form.copilotAuth.enterpriseUrl, clientId: form.copilotAuth.clientId }
      return value
    },
    set: value => {
      if (typeof value.credentialMode === 'string' && value.credentialMode !== modelDraft.value.credentialMode) changeCredentialMode(value.credentialMode)
      if (typeof value.baseUrl === 'string' && value.baseUrl !== modelDraft.value.baseUrl) changeCustomBaseURL(value.baseUrl)
      for (const field of modelConfigFields.value) {
        if (field !== 'baseUrl' && typeof value[field] === 'string') modelDraft.value[field] = value[field]
      }
      if (modelDraft.value.kind === 'custom' && typeof value.apiFormat === 'string') modelDraft.value.apiFormat = value.apiFormat
      const auth = value.copilotAuth as Record<string, unknown> | undefined
      if (modelDraft.value.kind === 'github-copilot' && auth) {
        if (typeof auth.enterpriseUrl === 'string') modelDraft.value.copilotAuth.enterpriseUrl = auth.enterpriseUrl
        if (typeof auth.clientId === 'string') modelDraft.value.copilotAuth.clientId = auth.clientId
      }
    },
  })
  function resetModelConfigEditor() {
    modelConfigDirty.value = false
    modelConfigResetKey.value++
  }
  watch([modelConfigValid, modelConfigDirty], () => { draftRevision++ }, { flush: 'sync' })
  function sameCredentialDestination() {
    const previous = editingProvider.value
    const form = modelDraft.value
    const base = (url: string) => form.kind === 'github-copilot' ? url.replace(/\/+$/, '').replace(/\/v1$/, '') : url.trim()
    return !!previous && base(previous.baseUrl || '') === base(form.baseUrl) && ['resourceName', 'region', 'project', 'location'].every(field => (previous[field as keyof ModelSetting] || '') === form[field as keyof typeof form])
  }
  function canReuseHeader(name: string) {
    return sameCredentialDestination() && !!editingProvider.value?.headerNames?.some(saved => saved.toLowerCase() === name.trim().toLowerCase())
  }
  const modelCredentialsValid = ref(true)
  const showModelCredentials = computed(() => !!modelDraft.value.kind && (usesAPIKey.value || modelDraft.value.credentialMode === 'accessToken' || modelDraft.value.kind === 'custom'))
  const modelCredentials = computed<Record<string, unknown>>({
    get: () => ({ apiKey: modelDraft.value.apiKey, accessToken: modelDraft.value.accessToken, headers: modelDraft.value.headers.map(header => ({ ...header })) }),
    set: value => { modelDraft.value.apiKey = value.apiKey as string; modelDraft.value.accessToken = value.accessToken as string; modelDraft.value.headers = value.headers as { name: string; value: string }[] },
  })
  const modelCredentialsSchema = computed<JsonSchema7>(() => ({ type: 'object', additionalProperties: false, required: ['apiKey', 'accessToken', 'headers'], properties: {
    apiKey: { type: 'string', title: t(modelDraft.value.kind === 'opencode' || modelDraft.value.kind === 'opencode-go' ? 'opencodeApiKey' : 'providerApiKey'), writeOnly: true, minLength: usesAPIKey.value && !(editingProvider.value?.hasApiKey && sameCredentialDestination()) ? 1 : 0, maxLength: 4096 },
    accessToken: { type: 'string', title: t('providerAccessToken'), writeOnly: true, minLength: modelDraft.value.credentialMode === 'accessToken' && !(editingProvider.value?.hasAccessToken && sameCredentialDestination()) ? 1 : 0, maxLength: 4096 },
    headers: { type: 'array', title: t('customHeaders'), maxItems: 32, items: { type: 'object', additionalProperties: false, required: ['name', 'value'], properties: {
      name: { type: 'string', title: t('headerName'), minLength: 1, maxLength: 256, default: '' },
      value: { type: 'string', title: t('headerValue'), writeOnly: true, maxLength: 8192, default: '' },
    } } },
  } }))
  const modelCredentialsUISchema = computed<UISchemaElement>(() => {
    const elements: ControlElement[] = []
    if (usesAPIKey.value) elements.push({ type: 'Control', scope: '#/properties/apiKey', options: { placeholder: editingProvider.value?.hasApiKey ? t('reuseSavedKey') : t('enterApiKey') } })
    if (modelDraft.value.credentialMode === 'accessToken') elements.push({ type: 'Control', scope: '#/properties/accessToken', options: { placeholder: editingProvider.value?.hasAccessToken ? t('reuseSavedKey') : t('enterAccessToken') } })
    if (modelDraft.value.kind === 'custom') elements.push({ type: 'Control', scope: '#/properties/headers' })
    return { type: 'VerticalLayout', elements }
  })
  const canSaveProvider = computed(() => {
    const form = modelDraft.value
    if (!modelConfigValid.value || !selectedSetup.value || showModelCredentials.value && !modelCredentialsValid.value) return false
    const headerNames = form.headers.map(header => header.name.trim().toLowerCase())
    if (new Set(headerNames).size !== headerNames.length || form.headers.some(header => !header.name.trim() || !header.value && !canReuseHeader(header.name))) return false
    if ((manualModelIDs.value || deploymentModelSelection.value) && (!chosenModels.value.length || chosenModels.value.length > (form.kind === 'azure' ? 1 : 16) || chosenModels.value.some(id => !id.trim() || id.length > 200))) return false
    const previous = editingProvider.value
    if (form.credentialMode === 'oauth') return !!previous && (form.kind !== 'github-copilot'
      || form.copilotAuth.enterpriseUrl.trim() === (previous.copilotAuth?.enterpriseUrl || '') && form.copilotAuth.clientId.trim() === (previous.copilotAuth?.clientId || ''))
    if (form.credentialMode === 'default' || form.credentialMode === 'none') return true
    if (form.credentialMode === 'accessToken') return !!form.accessToken || !!(previous?.hasAccessToken && sameCredentialDestination())
    return !!form.apiKey || !!(previous?.hasApiKey && sameCredentialDestination())
  })
  function updateSelectedModels(ids: string[]) { chosenModels.value = [...new Set(ids.map(id => id.trim()).filter(Boolean))] }
  function clearCustomModels() { manualModelIDs.value = false; chosenModels.value = []; settingsError.value = '' }
  function changeProvider(kind: string) {
    const setup = providerSetups.value.find(item => item.id === kind)
    modelDraft.value = { ...emptyDraft(), kind, baseUrl: setup?.baseUrl || '', credentialMode: setup?.credentialModes?.[0] || 'apiKey', region: kind === 'amazon-bedrock' ? 'us-east-1' : '', location: kind === 'google-vertex' ? 'us-central1' : '' }
    resetModelConfigEditor()
    clearCustomModels()
  }
  function changeCustomBaseURL(value: string) { modelDraft.value.baseUrl = value; if (modelDraft.value.kind === 'custom') { clearCustomModels() } }
  function changeCredentialMode(mode: string) {
    modelDraft.value.credentialMode = mode
    if (modelDraft.value.kind !== 'custom' || mode === 'none') modelDraft.value.apiKey = ''
    modelDraft.value.accessToken = ''
  }
  function settingProviderName(model: Pick<ModelInfo, 'name' | 'providerId' | 'provider'>) {
    return model.name || providerSetups.value.find(setup => setup.id === model.providerId)?.name || model.providerId || model.provider
  }
  function resetModelDraft() {
    modelDraft.value = emptyDraft()
    resetModelConfigEditor()
    clearCustomModels()
  }
  function showModelForm() {
    modelFormOpen.value = true
  }
  function startProviderConnection() { resetModelDraft(); showModelForm() }
  function cancelModelForm() { resetModelDraft(); modelFormOpen.value = false }
  let settingsRevision = 0
  let setupsRevision = 0
  let catalogRevision = ''
  let unsubscribeModels: (() => void) | undefined
  async function readModelSettings(signal?: AbortSignal) {
    const revision = ++settingsRevision
    const models = await api<ModelSettings>('/model-config', { signal })
    if (signal?.aborted || revision !== settingsRevision) return
    settings.value = models
  }
  async function readProviderSetups(signal?: AbortSignal) {
    const revision = ++setupsRevision
    const setups = await api<ProviderSetup[]>('/provider-config', { signal })
    if (!signal?.aborted && revision === setupsRevision) providerSetups.value = setups
  }
  onScopeDispose(() => { settingsRevision++; setupsRevision++; unsubscribeModels?.() })
  async function loadModelSettings() {
    cancelModelForm()
    if (unsubscribeModels) return
    unsubscribeModels = stateSync.subscribe(['models', 'catalog'], async (topics, signal, snapshots) => {
      const catalog = snapshots.catalog
      const nextRevision = catalog ? `${catalog.revision}:${catalog.modelsDevRevision || ''}` : ''
      const settingsRevision = snapshots.models?.settingsRevision
      try {
        await Promise.all([
          topics.includes('models') && (!settingsRevision || settingsRevision !== settings.value.settingsRevision) ? readModelSettings(signal) : Promise.resolve(),
          (!providerSetups.value.length || topics.includes('catalog') && !catalog?.updating && (!nextRevision || nextRevision !== catalogRevision))
            ? readProviderSetups(signal) : Promise.resolve(),
        ])
        if (signal.aborted) return
        if (catalog && !catalog.updating) catalogRevision = nextRevision
      } catch (cause) {
        if (!signal.aborted) { settingsError.value = String(cause); error.value = String(cause) }
        throw cause
      }
    })
  }
  function editProvider(model: ModelSetting) {
    clearCustomModels()
    modelDraft.value = { ...emptyDraft(), key: model.key, kind: model.kind || '', apiFormat: model.provider, headers: (model.headerNames || []).map(name => ({ name, value: '' })), copilotAuth: { enterpriseUrl: model.copilotAuth?.enterpriseUrl || '', clientId: model.copilotAuth?.clientId || '' }, name: model.name || '', providerId: model.providerId || '', baseUrl: model.baseUrl || '', credentialMode: model.credentialMode || 'apiKey', region: model.region || '', project: model.project || '', location: model.location || '', resourceName: model.resourceName || '', deploymentId: model.deploymentId || '', apiVersion: model.apiVersion || '' }
    chosenModels.value = settings.value.models.filter(item => model.connectionId ? item.connectionId === model.connectionId : item.key === model.key).map(item => item.id)
    settingsError.value = ''
    resetModelConfigEditor()
    showModelForm()
  }
  const providerGroups = computed(() => {
    const groups = new Map<string, { key: string; name: string; kind: string; protocol: string; baseUrl: string; models: ModelSetting[] }>()
    for (const model of settings.value.models) {
      const kind = model.kind || ''
      const key = model.connectionId || (kind === 'custom' ? `custom:${model.provider}:${model.baseUrl || ''}` : kind || `${model.provider}:${model.providerId || ''}:${model.baseUrl || ''}`)
      if (!groups.has(key)) groups.set(key, { key, name: settingProviderName(model), kind, protocol: model.provider, baseUrl: model.baseUrl || '', models: [] })
      groups.get(key)!.models.push(model)
    }
    return [...groups.values()]
  })
  async function saveProvider() {
    if (settingsBusy.value || !canSaveProvider.value) return
    const form = modelDraft.value
    const revision = draftRevision
    const request = new AbortController()
    connectionRequest = request
    settingsBusy.value = true
    settingsError.value = ''
    try {
      await api('/provider-config', { method: editingProvider.value ? 'PUT' : 'POST', signal: request.signal, body: JSON.stringify({
        key: form.key, name: form.name.trim(),
        provider: form.kind === 'custom' ? form.apiFormat : 'catalog',
        headers: form.kind === 'custom' ? Object.fromEntries(form.headers.map(header => [header.name.trim(), header.value])) : undefined,
        providerId: form.kind,
        baseUrl: form.baseUrl.trim(), apiKey: usesAPIKey.value ? form.apiKey : '',
        credentialMode: form.credentialMode, accessToken: form.credentialMode === 'accessToken' ? form.accessToken : '',
        region: form.region.trim(), project: form.project.trim(), location: form.location.trim(),
        resourceName: form.resourceName.trim(), deploymentId: form.deploymentId.trim(), apiVersion: form.apiVersion.trim(),
        ids: manualModelIDs.value || deploymentModelSelection.value ? chosenModels.value : [],
      }) })
      if (revision === draftRevision) {
        resetModelDraft()
        modelFormOpen.value = false
      }
    } catch (e) {
      if (revision !== draftRevision) return
      settingsError.value = e instanceof Error ? e.message : String(e)
      if (e instanceof VistaAPIError && e.code === 'model_ids_required') manualModelIDs.value = true
    }
    finally { if (connectionRequest === request) connectionRequest = undefined; settingsBusy.value = false }
  }
  async function retryModelDiscovery() {
    if (settingsBusy.value || !modelConfigValid.value || modelDraft.value.kind !== 'custom') return
    manualModelIDs.value = false
    chosenModels.value = []
    await saveProvider()
  }
  async function providerConnected() {
    cancelModelForm()
    modelFormOpen.value = false
  }
  async function disconnectProvider(key: string) {
    settingsBusy.value = true
    providerDisconnectError.value = ''
    try {
      await api('/provider-config', { method: 'DELETE', body: JSON.stringify({ key }) })
      if (modelDraft.value.key === key) resetModelDraft()
      providerDisconnectTarget.value = null
    } catch (e) { providerDisconnectError.value = String(e) }
    finally { settingsBusy.value = false }
  }
  async function setDefaultModel(key: string) {
    settingsBusy.value = true
    settingsError.value = ''
    try {
      await api('/model-config', { method: 'PATCH', body: JSON.stringify({ key }) })
    } catch (e) { settingsError.value = String(e) }
    finally { settingsBusy.value = false }
  }
  async function confirmDisconnectProvider() {
    const target = providerDisconnectTarget.value
    if (target) await disconnectProvider(target.key)
  }
  function showProviderDisconnect(key: string, name: string) {
    providerDisconnectError.value = ''
    providerDisconnectTarget.value = { key, name }
  }
  function updateProviderDisconnect(show: boolean, key: string, name: string) {
    if (show) showProviderDisconnect(key, name)
    else if (providerDisconnectTarget.value?.key === key) providerDisconnectTarget.value = null
  }

  return {
    languagePreference, languageChoice, languageOptions, regionOptions, settingsBusy, settingsError,
    showUnrecognizedModels, visibilityBusy, setShowUnrecognizedModels,
    providerDisconnectTarget, providerDisconnectError, settings,
    agentInstructions, agentInstructionsPath, savedAgentInstructions,
    maxDelegationDepth,
    agentSettingsDraft, agentSettingsSchema, agentSettingsResetKey,
    agentSettingsResetKeys, canSaveAgentField,
    agentSettingsBusy, agentSettingsLoading, agentSettingsError, agentSettingsSaved,
    loadAgentSettings, saveAgentSettings, modelDraft, modelFormOpen, chosenModels,
    manualModelIDs, canReuseHeader, editingProvider, providerOptions, canSaveProvider,
    providerChoice, providerChoiceSchema, providerChoiceUISchema,
    selectedSetup, deploymentModelSelection, providerModelOptions, usesAPIKey, authenticationHeader,
    modelConfig, modelConfigSchema, modelConfigUISchema, modelConfigValid, modelConfigDirty, modelConfigResetKey,
    modelCredentials, modelCredentialsSchema, modelCredentialsUISchema, modelCredentialsValid, showModelCredentials,
    updateSelectedModels, changeProvider,
    startProviderConnection, cancelModelForm, loadModelSettings, editProvider, providerGroups,
    saveProvider, retryModelDiscovery, providerConnected, setDefaultModel, confirmDisconnectProvider, updateProviderDisconnect,
  }
}
