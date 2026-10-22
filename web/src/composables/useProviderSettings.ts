import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { VistaAPIError, type VistaClient } from '../client/vista'
import type { AgentSettings, ModelInfo, ModelSetting, ModelSettings, ProviderSetup } from '../client/types'
import { languages } from '../i18n/languages.ts'
import { readLanguagePreference, setLanguagePreference, type LanguagePreference } from '../i18n'

type SettingsDependencies = {
  client: VistaClient
  refresh: () => Promise<void>
  error: Ref<string>
}

export function useProviderSettings({ client, refresh, error }: SettingsDependencies) {
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
  watch(languagePreference, setLanguagePreference)

  const settingsBusy = ref(false)
  const settingsError = ref('')
  const providerDisconnectTarget = ref<{ key: string; name: string } | null>(null)
  const providerDisconnectError = ref('')
  const settings = ref<ModelSettings>({ default: '', models: [] })
  const agentInstructions = ref('')
  const agentInstructionsPath = ref('')
  const savedAgentInstructions = ref('')
  const delegationDepth = ref<number | null>(1)
  const savedDelegationDepth = ref(1)
  const maxDelegationDepth = ref(8)
  const agentSettingsBusy = ref(false)
  const agentSettingsLoading = ref(false)
  const agentSettingsError = ref('')
  const agentSettingsSaved = ref(false)
  const canSaveAgentInstructions = computed(() => !agentSettingsLoading.value && !agentSettingsBusy.value
    && agentInstructions.value !== savedAgentInstructions.value)
  const canSaveDelegationDepth = computed(() => !agentSettingsLoading.value && !agentSettingsBusy.value
    && delegationDepth.value !== null && Number.isInteger(delegationDepth.value)
    && delegationDepth.value >= 0 && delegationDepth.value <= maxDelegationDepth.value
    && delegationDepth.value !== savedDelegationDepth.value)
  watch([agentInstructions, delegationDepth], () => { agentSettingsSaved.value = false })
  async function loadAgentSettings() {
    agentSettingsLoading.value = true
    agentSettingsError.value = ''
    agentSettingsSaved.value = false
    try {
      const result = await api<AgentSettings>('/agent-settings')
      agentInstructions.value = result.instructions
      agentInstructionsPath.value = result.instructionsPath
      savedAgentInstructions.value = result.instructions
      delegationDepth.value = result.delegationDepth
      savedDelegationDepth.value = result.delegationDepth
      maxDelegationDepth.value = result.maxDelegationDepth
    } catch (e) { agentSettingsError.value = String(e) }
    finally { agentSettingsLoading.value = false }
  }
  async function saveAgentSettings(field: 'instructions' | 'delegationDepth') {
    if (!(field === 'instructions' ? canSaveAgentInstructions.value : canSaveDelegationDepth.value)) return
    const input = field === 'instructions' ? { instructions: agentInstructions.value } : { delegationDepth: delegationDepth.value! }
    agentSettingsBusy.value = true
    agentSettingsError.value = ''
    agentSettingsSaved.value = false
    try {
      await api('/agent-settings', { method: 'PUT', body: JSON.stringify(input) })
      if (input.instructions !== undefined) savedAgentInstructions.value = input.instructions
      if (input.delegationDepth !== undefined) savedDelegationDepth.value = input.delegationDepth
      agentSettingsSaved.value = true
    } catch (e) { agentSettingsError.value = String(e) }
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
  const selectedSetup = computed(() => providerSetups.value.find(setup => setup.id === modelDraft.value.kind))
  const deploymentModelSelection = computed(() => modelDraft.value.kind === 'azure')
  const providerModelOptions = computed(() => selectedSetup.value?.models?.map(model => ({ label: model.name || model.id, value: model.id, searchText: `${model.name} ${model.id}` })) || [])
  const apiFormatLabels: Record<string, string> = { 'openai-chat': 'OpenAI Chat Completions', 'openai-responses': 'OpenAI Responses', anthropic: 'Anthropic Messages', gemini: 'Google Gemini generateContent', cohere: 'Cohere Chat v2' }
  const apiFormatOptions = computed(() => (selectedSetup.value?.apiFormats || []).map(value => ({ value, label: apiFormatLabels[value] || value })))
  const credentialOptions = computed(() => (selectedSetup.value?.credentialModes || []).map(mode => ({ value: mode, label: mode === 'bearer' ? 'Bearer' : mode === 'x-api-key' || mode === 'x-goog-api-key' ? mode : t(mode === 'apiKey' ? 'apiKey' : mode === 'none' ? 'noAuthentication' : mode === 'accessToken' ? 'providerAccessToken' : modelDraft.value.kind === 'amazon-bedrock' ? 'awsDefaultCredentials' : modelDraft.value.kind === 'google-vertex' ? 'googleDefaultCredentials' : 'noAuthentication') })))
  const usesAPIKey = computed(() => modelDraft.value.credentialMode === 'apiKey' || modelDraft.value.kind === 'custom' && modelDraft.value.credentialMode !== 'none')
  const authenticationHeader = computed(() => modelDraft.value.credentialMode === 'none' ? '' : modelDraft.value.credentialMode === 'bearer' ? 'Authorization: Bearer API_KEY' : `${modelDraft.value.credentialMode}: API_KEY`)
  function sameCredentialDestination() {
    const previous = editingProvider.value
    const form = modelDraft.value
    const base = (url: string) => form.kind === 'github-copilot' ? url.replace(/\/+$/, '').replace(/\/v1$/, '') : url.trim()
    return !!previous && base(previous.baseUrl || '') === base(form.baseUrl) && ['resourceName', 'region', 'project', 'location'].every(field => (previous[field as keyof ModelSetting] || '') === form[field as keyof typeof form])
  }
  function canReuseHeader(name: string) {
    return sameCredentialDestination() && !!editingProvider.value?.headerNames?.some(saved => saved.toLowerCase() === name.trim().toLowerCase())
  }
  const canSaveProvider = computed(() => {
    const form = modelDraft.value
    if (!selectedSetup.value || selectedSetup.value.fields?.includes('baseUrl') && !form.baseUrl.trim()) return false
    if (form.kind === 'custom' && !apiFormatOptions.value.some(option => option.value === form.apiFormat)) return false
    if (form.kind === 'azure' && (!form.resourceName.trim() || !form.deploymentId.trim())) return false
    if (form.kind === 'google-vertex' && form.credentialMode !== 'apiKey' && !form.project.trim()) return false
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
    clearCustomModels()
  }
  function showModelForm() {
    modelFormOpen.value = true
  }
  function startProviderConnection() { resetModelDraft(); showModelForm() }
  function cancelModelForm() { resetModelDraft(); modelFormOpen.value = false }
  async function loadModelSettings() {
    cancelModelForm()
    try {
      const [models, setups] = await Promise.all([api<ModelSettings>('/model-config'), api<ProviderSetup[]>('/provider-config')])
      settings.value = models
      providerSetups.value = setups
    } catch (e) { settingsError.value = String(e); error.value = String(e) }
  }
  async function refreshProviderCatalog() {
    try {
      providerSetups.value = await api<ProviderSetup[]>('/provider-config')
      await refresh()
    } catch (e) { settingsError.value = String(e) }
  }
  function editProvider(model: ModelSetting) {
    clearCustomModels()
    modelDraft.value = { ...emptyDraft(), key: model.key, kind: model.kind || '', apiFormat: model.provider, headers: (model.headerNames || []).map(name => ({ name, value: '' })), copilotAuth: { enterpriseUrl: model.copilotAuth?.enterpriseUrl || '', clientId: model.copilotAuth?.clientId || '' }, name: model.name || '', providerId: model.providerId || '', baseUrl: model.baseUrl || '', credentialMode: model.credentialMode || 'apiKey', region: model.region || '', project: model.project || '', location: model.location || '', resourceName: model.resourceName || '', deploymentId: model.deploymentId || '', apiVersion: model.apiVersion || '' }
    chosenModels.value = settings.value.models.filter(item => model.connectionId ? item.connectionId === model.connectionId : item.key === model.key).map(item => item.id)
    settingsError.value = ''
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
      await refresh()
      settings.value = await api('/model-config') as ModelSettings
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
    if (settingsBusy.value || modelDraft.value.kind !== 'custom') return
    manualModelIDs.value = false
    chosenModels.value = []
    await saveProvider()
  }
  async function providerConnected() {
    await refresh()
    await loadModelSettings()
    modelFormOpen.value = false
  }
  async function disconnectProvider(key: string) {
    settingsBusy.value = true
    providerDisconnectError.value = ''
    try {
      await api('/provider-config', { method: 'DELETE', body: JSON.stringify({ key }) })
      if (modelDraft.value.key === key) resetModelDraft()
      await refresh()
      settings.value = await api('/model-config') as ModelSettings
      providerDisconnectTarget.value = null
    } catch (e) { providerDisconnectError.value = String(e) }
    finally { settingsBusy.value = false }
  }
  async function setDefaultModel(key: string) {
    settingsBusy.value = true
    settingsError.value = ''
    try {
      await api('/model-config', { method: 'PATCH', body: JSON.stringify({ key }) })
      await refresh()
      settings.value = await api('/model-config') as ModelSettings
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
    providerDisconnectTarget, providerDisconnectError, settings,
    agentInstructions, agentInstructionsPath, savedAgentInstructions,
    delegationDepth, maxDelegationDepth, canSaveAgentInstructions, canSaveDelegationDepth,
    agentSettingsBusy, agentSettingsLoading, agentSettingsError, agentSettingsSaved,
    loadAgentSettings, saveAgentSettings, modelDraft, modelFormOpen, chosenModels,
    manualModelIDs, canReuseHeader, editingProvider, providerOptions, canSaveProvider,
    selectedSetup, deploymentModelSelection, providerModelOptions, apiFormatOptions, credentialOptions, usesAPIKey, authenticationHeader, changeCredentialMode,
    updateSelectedModels, changeProvider, changeCustomBaseURL,
    startProviderConnection, cancelModelForm, loadModelSettings, refreshProviderCatalog, editProvider, providerGroups,
    saveProvider, retryModelDiscovery, providerConnected, setDefaultModel, confirmDisconnectProvider, updateProviderDisconnect,
  }
}
