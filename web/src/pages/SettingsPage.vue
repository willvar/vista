<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, reactive, onMounted, onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { IconChevronRight, IconPalette, IconUser, IconPlugConnected, IconInfoCircle, IconSearch, IconLink, IconBook2, IconSun, IconMoon, IconDeviceDesktop, IconDatabase, IconAlertCircle, IconMaximize, IconMinimize, IconPlus, IconFileImport, IconRefresh, IconRobot, IconRobotFace, IconUsers, IconCompass, IconTool, IconMessage, IconFolderOpen, IconFolder, IconFileText } from '@tabler/icons-vue'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Input as VInput, Modal as VModal, Select as VSelect, Switch as VSwitch, Tooltip as VTooltip } from '../ui'
import WorkspaceModal from '../WorkspaceModal.vue'
import ConfirmPopover from '../ConfirmPopover.vue'
import ModelSelectionStats from '../ModelSelectionStats.vue'
import MemorySettings from '../MemorySettings.vue'
import SearchSettings from '../SearchSettings.vue'
import MCPSettings from '../MCPSettings.vue'
import SkillsSettings from '../SkillsSettings.vue'
import ResourceStorageSettings from '../ResourceStorageSettings.vue'
import ProviderLogin from '../ProviderLogin.vue'
import ConfigurationImport from '../ConfigurationImport.vue'
import OMPCatalogSettings from '../OMPCatalogSettings.vue'
import CapacitySettings from '../CapacitySettings.vue'
import UserAgentSettings from '../UserAgentSettings.vue'
import ResetVistaSettings from '../ResetVistaSettings.vue'
import RestartVistaSettings from '../RestartVistaSettings.vue'
import ServerConfigSettings from '../ServerConfigSettings.vue'
import SchemaForm from '../SchemaForm.vue'
import NetworkSettings from '../NetworkSettings.vue'
import ProxySelection from '../ProxySelection.vue'
import { useNetworkConfig } from '../composables/useNetworkConfig'
import type { ModelSetting } from '../client/types'
import VistaMark from '../VistaMark.vue'
import { IconChartBar, IconReload } from '@tabler/icons-vue'
import { useWorkspace } from '../composables/useWorkspace'
import { useProviderSettings } from '../composables/useProviderSettings'
import { useAppearance } from '../ui/appearance'

const StatisticsSettings = defineAsyncComponent({
  loader: () => import('../StatisticsSettings.vue'),
  loadingComponent: () => h('p', { class: 'settings-content', role: 'status' }, t('statisticsLoading')),
})
const route = useRoute()
const { t } = useI18n({ useScope: 'global' })
const { appearance, setAppearance } = useAppearance()
const appearances = [{ value: 'light', icon: IconSun }, { value: 'dark', icon: IconMoon }, { value: 'system', icon: IconDeviceDesktop }] as const
const delegationExamples = ['delegationDepthSolo', 'delegationDepthMain', 'delegationDepthNested'] as const
const delegationNodes = [
  { x: 50, y: 14, level: 0 },
  { x: 25, y: 50, level: 1 }, { x: 75, y: 50, level: 1 },
  { x: 12.5, y: 86, level: 2 }, { x: 37.5, y: 86, level: 2 },
  { x: 62.5, y: 86, level: 2 }, { x: 87.5, y: 86, level: 2 },
]
const workspace = useWorkspace()
const { modelCatalog, defaultModelChoices, modelNameById, backToWorkspace, debugMode, serverStatus } = workspace
const { lastMode, ready: preferencesReady } = workspace.interactionPreferences
const { selectionMode, chooseSelectionMode } = workspace.conversation
const modelSelectionMode = computed({
  get: () => workspace.selected.value ? selectionMode.value : lastMode.value,
  set: (mode: 'manual' | 'auto') => {
    lastMode.value = mode
    if (workspace.selected.value) chooseSelectionMode(mode)
  },
})
const selectionModeOptions = computed(() => [{ label: t('manualMode'), value: 'manual' }, { label: t('autoMode'), value: 'auto' }])
const sections = [
  { name: 'settings-general', label: 'appearance', icon: IconPalette },
  { name: 'settings-personalization', label: 'personalizationSettings', icon: IconUser },
  { name: 'settings', label: 'modelConnections', icon: IconPlugConnected },
  { name: 'settings-search', label: 'searchSettings', icon: IconSearch },
  { name: 'settings-mcp', label: 'mcpSettings', icon: IconLink },
  { name: 'settings-skills', label: 'skillsSettings', icon: IconBook2 },
  { name: 'settings-network', label: 'networkSettings', icon: IconCompass },
  { name: 'settings-statistics', label: 'statisticsSettings', icon: IconChartBar },
  { name: 'settings-storage', label: 'storageSettings', icon: IconDatabase },
  { name: 'settings-about', label: 'aboutSettings', icon: IconInfoCircle },
]
const settingsIndex = computed(() => route.name === 'settings-index')
const settingsTitle = computed(() => t(sections.find(section => section.name === route.name)?.label || 'settings'))
const mobileViewport = window.matchMedia('(max-width: 830px)')
const mobile = ref(mobileViewport.matches)
const modal = ref<InstanceType<typeof WorkspaceModal>>()
function updateViewport() { mobile.value = mobileViewport.matches }
onMounted(() => mobileViewport.addEventListener('change', updateViewport))
onUnmounted(() => mobileViewport.removeEventListener('change', updateViewport))
watch([settingsIndex, mobile], ([index, small]) => {
  if (index && !small) void workspace.router.replace({ name: 'settings-general', query: route.query })
}, { immediate: true })
watch(() => route.name, async () => {
  if (!mobile.value) return
  await nextTick()
  modal.value?.focusTitle()
}, { flush: 'post' })
function back() {
  if (!mobile.value || settingsIndex.value) return backToWorkspace()
  const directory = { name: 'settings-index', query: route.query }
  if (workspace.router.options.history.state.back === workspace.router.resolve(directory).fullPath) workspace.router.back()
  else void workspace.router.replace(directory)
}
const mcpModalOpen = ref(false)
const skillModalOpen = ref(false)
const memoryModalOpen = ref(false)
const searchModalOpen = ref(false)
const networkModalOpen = ref(false)
const storageModalOpen = ref(false)
const resetModalOpen = ref(false)
const restartModalOpen = ref(false)
const portChangeModalOpen = ref(false)
const statisticsModalOpen = ref(false)
const providerImportOpen = ref(false)
const providerImport = ref<InstanceType<typeof ConfigurationImport>>()
const importedCount = ref(0)
const instructionsEditorOpen = ref(false)
const instructionsDocument = computed<Record<string, unknown>>({ get: () => ({ instructions: agentInstructions.value }), set: value => { if (typeof value.instructions === 'string') agentInstructions.value = value.instructions } })
const instructionsSchema = computed(() => ({ type: 'object' as const, additionalProperties: false, required: ['instructions'], properties: { instructions: { type: 'string' as const, title: t('globalInstructions') } } }))
const instructionsUISchema = computed(() => ({ type: 'Control' as const, scope: '#/properties/instructions', options: { multi: true, rows: 24, placeholder: t('globalInstructionsPlaceholder') } }))
function instructionsFocus() { return document.getElementById('fullscreen-agent-instructions-instructions-input') }
const {
  languagePreference, languageChoice, languageOptions, regionOptions, settingsBusy, settingsError,
  showUnrecognizedModels, visibilityBusy, setShowUnrecognizedModels,
  providerDisconnectTarget, providerDisconnectError, settings,
  agentInstructions, agentInstructionsPath,
  maxDelegationDepth,
  agentSettingsDraft, agentSettingsSchema, agentSettingsResetKey,
  agentSettingsResetKeys, canSaveAgentField,
  agentSettingsBusy, agentSettingsLoading, agentSettingsError, agentSettingsSaved,
  loadAgentSettings, saveAgentSettings, modelDraft, modelFormOpen, chosenModels,
  providerDraftVersion, providerConflict, manualModelIDs, canReuseHeader, editingProvider, providerOptions, canSaveProvider,
  providerChoice, providerChoiceSchema, providerChoiceUISchema,
  deploymentModelSelection, providerModelOptions, usesAPIKey, authenticationHeader,
  modelConfig, modelConfigSchema, modelConfigUISchema, modelConfigValid, modelConfigDirty, modelConfigResetKey,
  modelCredentials, modelCredentialsSchema, modelCredentialsUISchema, modelCredentialsValid, showModelCredentials,
  updateSelectedModels, changeProvider,
  startProviderConnection, cancelModelForm, loadModelSettings, editProvider, providerGroups,
  saveProvider, retryModelDiscovery, providerConnected, setDefaultModel, confirmDisconnectProvider, updateProviderDisconnect,
} = useProviderSettings(workspace)
const networkConfigData = useNetworkConfig(workspace, () => route.name === 'settings')
const proxyProfiles = computed(() => networkConfigData.value.value?.config.profiles || [])
const providerProxyValid = ref(true), providerCredentialDirty = ref(false), providerBaseline = ref('')
const providerSnapshot = computed(() => JSON.stringify({ draft: modelDraft.value, models: chosenModels.value, manual: manualModelIDs.value }))
const providerDiscardOpen = ref(false)
const proxyBindingTarget = ref<ModelSetting>(), proxyBindingValue = ref(''), proxyBindingBase = ref(''), proxyBindingRevision = ref(''), proxyBindingValid = ref(true), proxyBindingError = ref('')
const proxyBindingDirty = computed(() => !!proxyBindingTarget.value && proxyBindingValue.value !== proxyBindingBase.value)
const proxyBindingConflict = computed(() => !!proxyBindingTarget.value && proxyBindingRevision.value !== settings.value.settingsRevision)
function editProxyBinding(model: ModelSetting) { proxyBindingTarget.value = model; proxyBindingValue.value = model.proxyId || ''; proxyBindingBase.value = proxyBindingValue.value; proxyBindingRevision.value = settings.value.settingsRevision || ''; proxyBindingError.value = '' }
async function closeProxyBinding() { if (settingsBusy.value || proxyBindingDirty.value && !await askProviderDiscard()) return; proxyBindingTarget.value = undefined }
async function saveProxyBinding() {
  const target = proxyBindingTarget.value; if (!target || !proxyBindingValid.value || proxyBindingConflict.value) return
  settingsBusy.value = true; proxyBindingError.value = ''
  try { await workspace.client.request('/provider-config', { method: 'PATCH', body: JSON.stringify({ key: target.key, proxyId: proxyBindingValue.value, revision: proxyBindingRevision.value }) }); proxyBindingTarget.value = undefined }
  catch (cause) { proxyBindingError.value = cause instanceof Error ? cause.message : String(cause) }
  finally { settingsBusy.value = false }
}
async function reloadProviderDraft() { if (providerDirty.value && !await askProviderDiscard()) return; const latest = settings.value.models.find(model => model.key === modelDraft.value.key); if (latest) { editProvider(latest); providerBaseline.value = providerSnapshot.value; providerCredentialDirty.value = false } }
async function reloadProxyBinding() { if (proxyBindingDirty.value && !await askProviderDiscard()) return; const latest = settings.value.models.find(model => model.key === proxyBindingTarget.value?.key); if (latest) editProxyBinding(latest) }

let providerDiscardResolve: ((value: boolean) => void) | undefined, providerDiscardPending: Promise<boolean> | undefined
const providerDirty = computed(() => providerCredentialDirty.value || modelConfigDirty.value || providerSnapshot.value !== providerBaseline.value)
watch(modelFormOpen, open => { providerBaseline.value = open ? providerSnapshot.value : ''; providerCredentialDirty.value = false; if (!open) finishProviderDiscard(false) }, { flush: 'sync' })
function askProviderDiscard() { if (providerDiscardPending) return providerDiscardPending; providerDiscardOpen.value = true; return providerDiscardPending = new Promise<boolean>(resolve => { providerDiscardResolve = resolve }) }
function finishProviderDiscard(value: boolean) { providerDiscardOpen.value = false; providerDiscardResolve?.(value); providerDiscardResolve = undefined; providerDiscardPending = undefined }
async function leaveProvider() { if (settingsBusy.value) return false; return (!(modelFormOpen.value && providerDirty.value) && !proxyBindingDirty.value) || await askProviderDiscard() }
onBeforeRouteLeave(leaveProvider); onBeforeRouteUpdate(leaveProvider)
let openedConnection = ''
watch([() => route.name, () => route.query.connection, () => settings.value.models, () => providerOptions.value.length], async () => {
  await nextTick()
  if (route.name !== 'settings') { openedConnection = ''; return }
  const key = typeof route.query.connection === 'string' ? route.query.connection : ''
  if (!key || openedConnection === key || modelFormOpen.value || !providerOptions.value.length) return
  const model = settings.value.models.find(model => model.key === key)
  if (model) { openedConnection = key; if (model.editable) editProvider(model); else editProxyBinding(model) }
}, { immediate: true, flush: 'post' })
onUnmounted(() => finishProviderDiscard(false))
const agentFieldValidity = reactive({ instructions: true, delegationDepth: true, automaticTitles: true })
const agentFieldSchemas = computed(() => Object.fromEntries(['instructions', 'delegationDepth', 'automaticTitles'].map(field => [field, {
  type: 'object', additionalProperties: false, required: [field], properties: { [field]: agentSettingsSchema.value.properties![field] },
}])))
const instructionsInlineUI = computed(() => ({ type: 'Control' as const, scope: '#/properties/instructions', options: { multi: true, rows: 9, placeholder: t('globalInstructionsPlaceholder') } }))
const delegationUISchema = { type: 'Control' as const, scope: '#/properties/delegationDepth', options: { descriptionIds: 'delegation-depth-intro delegation-depth-hint' } }
const delegationDocument = computed<Record<string, unknown>>({ get: () => ({ delegationDepth: agentSettingsDraft.value.delegationDepth }), set: value => { agentSettingsDraft.value = { ...agentSettingsDraft.value, delegationDepth: value.delegationDepth } } })
const titlesDocument = computed<Record<string, unknown>>({ get: () => ({ automaticTitles: agentSettingsDraft.value.automaticTitles }), set: value => { agentSettingsDraft.value = { ...agentSettingsDraft.value, automaticTitles: value.automaticTitles }; void saveAgentSettings('automaticTitles') } })

type HeaderAction = { label: string; run: () => unknown; disabled: boolean; loading: boolean }
const sectionActions = ref<{ headerAction: HeaderAction; imported?: () => unknown } | null>(null)
const importKind = computed(() => route.name === 'settings-mcp' ? 'mcp' : route.name === 'settings-skills' ? 'skills' : 'providers')
const importTitleKey = computed(() => importKind.value === 'providers' ? 'providerImport' : importKind.value === 'mcp' ? 'mcpImport' : 'skillsImport')
const canImport = computed(() => ['settings', 'settings-mcp', 'settings-skills'].includes(String(route.name)))
function onImported(count: number) {
  importedCount.value = count
  if (importKind.value === 'providers') void providerConnected()
  else void sectionActions.value?.imported?.()
}
const headerAction = computed<HeaderAction | undefined>(() => route.name === 'settings'
  ? { label: 'connectProvider', run: startProviderConnection, disabled: settingsBusy.value, loading: false }
  : ['settings-mcp', 'settings-network', 'settings-about', 'settings-statistics'].includes(String(route.name)) ? sectionActions.value?.headerAction : undefined)
const loginBusy = ref(false)
watch(() => modelDraft.value.kind, () => { loginBusy.value = false })
function providerTypeLabel(kind: string, protocol: string) {
  return providerOptions.value.find(option => option.value === kind)?.label || protocol
}
async function closeProviderForm() {
  if (settingsBusy.value || providerDirty.value && !await askProviderDiscard()) return
  cancelModelForm()
}

watch(() => route.fullPath, () => {
  importedCount.value = 0
  instructionsEditorOpen.value = false
  providerDisconnectTarget.value = null
  proxyBindingTarget.value = undefined
  cancelModelForm()
  if (route.name === 'settings-personalization' || route.name === 'settings-general') void loadAgentSettings()
  else if (route.name === 'settings') void loadModelSettings()
}, { immediate: true })
</script>

<template>
<WorkspaceModal ref="modal" :open="true" width="max-content" :title="settingsTitle" :page-class="settingsIndex ? 'settings-page settings-index' : 'settings-page'"
  :close-label="t('closeSettings')" :back-label="t(settingsIndex ? 'backToConversation' : 'backToSettings')"
  :keyboard="!providerDisconnectTarget && !modelFormOpen && !providerImportOpen && !mcpModalOpen && !skillModalOpen && !memoryModalOpen && !searchModalOpen && !networkModalOpen && !providerDiscardOpen && !proxyBindingTarget && !storageModalOpen && !resetModalOpen && !restartModalOpen && !portChangeModalOpen && !statisticsModalOpen && !instructionsEditorOpen" @cancel="backToWorkspace" @back="back">
        <template #header-actions>
            <v-button v-if="canImport" variant="text" class="icon-button" :aria-label="t(importTitleKey)" :title="t(importTitleKey)" :disabled="importKind === 'providers' ? settingsBusy : headerAction?.disabled" @click="importedCount = 0; providerImport?.show()"><template #icon><IconFileImport aria-hidden="true" /></template></v-button>
            <v-button v-if="headerAction" variant="text" class="icon-button" :aria-label="t(headerAction.label)" :title="t(headerAction.label)" :disabled="headerAction.disabled" :loading="headerAction.loading" @click="headerAction.run()">
              <template #icon><IconPlus v-if="route.name === 'settings' || route.name === 'settings-mcp' || route.name === 'settings-network'" aria-hidden="true" /><IconReload v-else-if="route.name === 'settings-statistics'" aria-hidden="true" /><IconRefresh v-else aria-hidden="true" /></template>
            </v-button>
        </template>
        <div class="settings-body">
        <nav class="settings-nav" :aria-label="t('settings')">
          <router-link v-for="section in sections" :key="section.name" :to="{ name: section.name, query: route.query }" :class="{ active: route.name === section.name }" :aria-current="route.name === section.name ? 'page' : undefined"><component :is="section.icon" aria-hidden="true" /><span>{{ t(section.label) }}</span><IconChevronRight class="settings-nav-chevron" aria-hidden="true" /></router-link>
        </nav>
        <NetworkSettings ref="sectionActions" v-if="route.name === 'settings-network'" @modal-open="networkModalOpen = $event" />
        <SearchSettings v-if="route.name === 'settings-search'" @modal-open="searchModalOpen = $event" />
        <StatisticsSettings ref="sectionActions" v-if="route.name === 'settings-statistics'" @modal-open="statisticsModalOpen = $event" />
        <MCPSettings ref="sectionActions" v-if="route.name === 'settings-mcp'" @modal-open="mcpModalOpen = $event" />
        <SkillsSettings ref="sectionActions" v-if="route.name === 'settings-skills'" @modal-open="skillModalOpen = $event" />
        <ResourceStorageSettings ref="sectionActions" v-if="route.name === 'settings-storage'" @modal-open="storageModalOpen = $event" />
        <ConfigurationImport v-if="canImport" :key="importKind" ref="providerImport" :kind="importKind" @modal-open="providerImportOpen = $event" @imported="onImported" />
        <div v-if="route.name === 'settings-general'" class="settings-content appearance-settings-content">
          <div class="general-setting appearance-setting"><label id="appearance-label">{{ t('colorMode') }}</label><div class="appearance-options" role="group" aria-labelledby="appearance-label"><button v-for="mode in appearances" :key="mode.value" type="button" class="appearance-option" :class="{ selected: appearance === mode.value }" :aria-pressed="appearance === mode.value" @click="setAppearance(mode.value)"><span class="appearance-preview" :data-mode="mode.value"><i></i><span><b></b><b></b><b></b></span></span><span class="appearance-option-label"><component :is="mode.icon" aria-hidden="true" />{{ t(`appearance${mode.value[0]!.toUpperCase()}${mode.value.slice(1)}`) }}</span></button></div></div>
          <div class="locale-settings general-setting" :class="{ 'locale-settings-auto': regionOptions.length <= 1 }">
            <div class="language-setting"><label for="interface-language">{{ t('language') }}</label><v-select id="interface-language" v-model:value="languageChoice" :options="languageOptions" show-search option-filter-prop="searchText" :aria-label="t('language')" /></div>
            <div v-if="regionOptions.length > 1" class="region-setting"><label for="interface-region">{{ t('countryRegion') }}</label><v-select id="interface-region" v-model:value="languagePreference" :options="regionOptions" show-search option-filter-prop="searchText" :aria-label="t('countryRegion')" /></div>
          </div>
          <div class="model-selection-setting general-setting"><label for="model-selection-mode">{{ t('selectionMode') }}</label><v-select id="model-selection-mode" v-model:value="modelSelectionMode" :options="selectionModeOptions" :disabled="!preferencesReady" :aria-label="t('selectionMode')" /><p>{{ t('selectionModeSettingsHint') }}</p></div>
          <ModelSelectionStats v-if="modelSelectionMode === 'auto'" />
        </div>
        <div v-if="route.name === 'settings-personalization'" class="settings-content personalization-settings-content">
          <v-alert v-if="agentSettingsError" type="error" role="alert" :message="agentSettingsError" />
          <v-alert v-if="agentSettingsSaved" type="success" role="status" :message="t('agentSettingsSaved')" />
          <form class="agent-instructions-form" :aria-label="t('personalizationSettings')" @submit.prevent="agentFieldValidity.instructions && saveAgentSettings('instructions')">
            <div class="general-setting">
              <div class="agent-instructions-heading"><strong>{{ t('globalInstructions') }}</strong><v-button :disabled="agentSettingsLoading || agentSettingsBusy || !agentFieldValidity.instructions" @click="instructionsEditorOpen = true"><IconMaximize aria-hidden="true" />{{ t('fullscreenEdit') }}</v-button></div>
              <figure id="global-instructions-hint" class="personalization-figure instructions-figure" :aria-label="t('instructionsFigureLabel')">
                <div class="instructions-figure-heading"><span>{{ t('instructionsFramework') }}</span><small>{{ t('instructionsGlobalScope') }}</small></div>
                <div class="instructions-workflow">
                  <div class="personalization-figure-node"><IconCompass aria-hidden="true" /><span>{{ t('instructionsUnderstand') }}</span></div>
                  <span class="personalization-figure-link" aria-hidden="true"></span>
                  <div class="personalization-figure-node"><IconTool aria-hidden="true" /><span>{{ t('instructionsAct') }}</span></div>
                  <span class="personalization-figure-link" aria-hidden="true"></span>
                  <div class="personalization-figure-node"><IconMessage aria-hidden="true" /><span>{{ t('instructionsRespond') }}</span></div>
                </div>
              </figure>
              <SchemaForm id="agent-settings" v-model="instructionsDocument" :schema="agentFieldSchemas.instructions" :uischema="instructionsInlineUI"
                :disabled="agentSettingsLoading || agentSettingsBusy || instructionsEditorOpen" :reset-key="agentSettingsResetKeys.instructions"
                @validity="agentFieldValidity.instructions = $event" />
            </div>
            <div class="agent-instructions-footer"><v-button type="submit" variant="primary" :loading="agentSettingsBusy" :disabled="!agentFieldValidity.instructions || !canSaveAgentField('instructions')">{{ t('saveChanges') }}</v-button></div>
            <aside class="instructions-local-example" aria-labelledby="instructions-local-heading">
              <div id="instructions-local-heading" class="instructions-local-heading">{{ t('instructionsDirectoryTitle') }}</div>
              <div class="instructions-local-flow">
                <div class="instructions-directory">
                  <div class="instructions-directory-name"><IconFolderOpen aria-hidden="true" /><span>{{ t('instructionsExampleDirectory') }}</span></div>
                  <div class="instructions-directory-entries">
                    <div class="instructions-directory-entry instructions-directory-file"><IconFileText aria-hidden="true" /><div><code>AGENTS.md</code><small>{{ t('instructionsCreateFile') }}</small></div></div>
                    <div class="instructions-directory-entry"><IconFolder aria-hidden="true" /><span>{{ t('instructionsSubdirectory') }}</span></div>
                  </div>
                </div>
                <span class="personalization-figure-link" aria-hidden="true"></span>
                <div class="personalization-figure-node"><IconRobot aria-hidden="true" /><span>{{ t('instructionsReadLocalRules') }}</span></div>
              </div>
              <p class="instructions-local-scope">{{ t('instructionsLocalScope') }}</p>
            </aside>
          </form>
          <MemorySettings @modal-open="memoryModalOpen = $event" />
          <form class="delegation-depth-form" :aria-label="t('delegationDepth')" @submit.prevent="agentFieldValidity.delegationDepth && saveAgentSettings('delegationDepth')">
            <div class="general-setting">
              <p id="delegation-depth-intro">{{ t('delegationDepthIntro') }}</p>
              <figure id="delegation-depth-hint" class="delegation-depth-figure" :aria-label="t('delegationDepthHint', { max: maxDelegationDepth })">
                <div class="delegation-depth-examples">
                  <div v-for="(label, depth) in delegationExamples" :key="depth" class="delegation-depth-example">
                    <strong>{{ depth }}</strong>
                    <div class="delegation-tree" aria-hidden="true">
                      <svg viewBox="0 0 100 100" preserveAspectRatio="none">
                        <path v-if="depth >= 1" d="M50 24V32 M25 40V32H75V40" />
                        <path v-if="depth >= 2" d="M25 60V68 M12.5 76V68H37.5V76 M75 60V68 M62.5 76V68H87.5V76" />
                      </svg>
                      <span v-for="node in delegationNodes.filter(node => node.level <= depth)" :key="`${node.level}-${node.x}`" class="delegation-tree-agent" :class="{ 'delegation-tree-main': node.level === 0 }" :style="{ left: `${node.x}%`, top: `${node.y}%` }"><IconRobotFace /></span>
                    </div>
                    <span>{{ t(label) }}</span>
                  </div>
                </div>
                <div class="delegation-depth-tradeoffs">
                  <div><IconUsers aria-hidden="true" /><p>{{ t('delegationDepthBenefit') }}</p></div>
                  <div class="delegation-depth-cost"><IconAlertCircle aria-hidden="true" /><p>{{ t('delegationDepthCost') }}</p></div>
                </div>
                <figcaption>{{ t('delegationDepthImpact') }}</figcaption>
              </figure>
              <SchemaForm id="delegation-settings" v-model="delegationDocument" :schema="agentFieldSchemas.delegationDepth" :uischema="delegationUISchema" :disabled="agentSettingsLoading || agentSettingsBusy"
                :reset-key="agentSettingsResetKeys.delegationDepth" @validity="agentFieldValidity.delegationDepth = $event" />
            </div>
            <div class="agent-instructions-footer"><v-button type="submit" variant="primary" :loading="agentSettingsBusy" :disabled="!agentFieldValidity.delegationDepth || !canSaveAgentField('delegationDepth')">{{ t('saveChanges') }}</v-button></div>
          </form>
          <div class="automatic-title-setting general-setting">
            <SchemaForm id="title-settings" v-model="titlesDocument" :schema="agentFieldSchemas.automaticTitles" :disabled="agentSettingsLoading || agentSettingsBusy"
              :reset-key="agentSettingsResetKeys.automaticTitles" @validity="agentFieldValidity.automaticTitles = $event" />
          </div>
        </div>
        <div v-if="route.name === 'settings'" class="settings-content">
          <div v-if="settings.models.length" class="settings-toolbar"><p>{{ t('manageProvidersHint') }}</p></div>
          <v-empty v-else class="vista-empty-state provider-empty-state">
            <template #description><h3>{{ t('chooseProviderHint') }}</h3><p>{{ t('addProviderHint') }}</p></template>
          </v-empty>
          <v-alert v-if="settingsError && !modelFormOpen" type="error" role="alert" :message="settingsError" />
          <v-alert v-if="importedCount" type="success" role="status" :message="t('providerImportSuccess', { count: importedCount })" />
          <div v-if="settings.models.length" class="default-model-setting"><label>{{ t('defaultModel') }}<v-select :value="modelCatalog.default" :options="defaultModelChoices" show-search option-filter-prop="searchText" option-label-prop="fullLabel" :disabled="settingsBusy" @update:value="setDefaultModel(String($event))" /></label></div>
          <div class="model-visibility-setting general-setting"><div><label for="show-unrecognized-models">{{ t('showUnrecognizedModels') }}</label><p id="model-visibility-hint">{{ t('showUnrecognizedModelsHint') }}</p></div><v-switch id="show-unrecognized-models" :checked="showUnrecognizedModels" :loading="visibilityBusy" :aria-label="t('showUnrecognizedModels')" aria-describedby="model-visibility-hint" @update:checked="setShowUnrecognizedModels($event === true)" /></div>
          <div v-if="providerGroups.length" class="configured-providers"><h3>{{ t('connectedProviders') }}</h3>
            <div v-for="group in providerGroups" :key="group.key" class="configured-provider">
              <div class="provider-heading"><div><strong>{{ group.name }}</strong><small class="provider-type">{{ providerTypeLabel(group.kind, group.protocol) }}</small><small v-if="group.baseUrl">{{ group.baseUrl }}</small></div><div class="actions"><template v-if="group.models.every(model => model.editable)"><v-button :disabled="settingsBusy" @click="editProvider(group.models[0]!)">{{ t('edit') }}</v-button><confirm-popover :show="providerDisconnectTarget?.key === group.models[0]!.key" :message="t('disconnectProviderConfirm', { name: group.name })" :cancel-text="t('cancel')" :confirm-text="t('disconnectProvider')" :busy="settingsBusy" :error="providerDisconnectError" popover-class="provider-disconnect-popover" @update:show="updateProviderDisconnect($event, group.models[0]!.key, group.name)" @confirm="confirmDisconnectProvider"><template #trigger><v-button danger :disabled="settingsBusy">{{ t('disconnectProvider') }}</v-button></template></confirm-popover></template><v-button v-else :disabled="settingsBusy" @click="editProxyBinding(group.models[0]!)">{{ t('networkProxy') }}</v-button></div></div>
            </div>
          </div>
        </div>
        <div v-if="route.name === 'settings-about'" class="settings-content about-settings-content">
          <div class="about-brand">
            <VistaMark />
            <h3>Vista<small v-if="serverStatus?.version" class="about-version">{{ serverStatus.version }}</small></h3>
            <p class="about-tagline">{{ t('vistaTagline') }}</p>
          </div>
          <OMPCatalogSettings ref="sectionActions" :client="workspace.client" />
          <UserAgentSettings :client="workspace.client" />
          <CapacitySettings :client="workspace.client" :status="serverStatus" />
          <ServerConfigSettings @modal-open="portChangeModalOpen = $event" />
          <div class="debug-mode-setting general-setting"><div><label for="debug-mode">{{ t('debugMode') }}</label><p id="debug-mode-hint">{{ t('debugModeHint') }}</p></div><v-switch id="debug-mode" v-model:checked="debugMode" :aria-label="t('debugMode')" aria-describedby="debug-mode-hint" /></div>
          <ResetVistaSettings v-if="debugMode" @modal-open="resetModalOpen = $event" />
          <RestartVistaSettings @modal-open="restartModalOpen = $event" />
          <dl v-if="serverStatus" class="about-build-info">
            <template v-if="serverStatus.binaryHash">
              <dt>{{ t('binaryHash') }}</dt>
              <dd><code :title="serverStatus.binaryHash">{{ serverStatus.binaryHash.slice(0, 12) }}</code></dd>
            </template>
            <template v-if="serverStatus.sourceCommit">
              <dt>{{ t('sourceCommit') }}</dt>
              <dd><code :title="serverStatus.sourceCommit">{{ serverStatus.sourceCommit.slice(0, 12) }}<template v-if="serverStatus.sourceModified">-dirty</template></code></dd>
            </template>
            <template v-if="serverStatus.hostOS">
              <dt>{{ t('hostOS') }}</dt>
              <dd><code>{{ serverStatus.hostOS }}</code></dd>
            </template>
          </dl>
        </div>
        </div>
      </WorkspaceModal>
  <v-modal :open="instructionsEditorOpen" :title="t('globalInstructions')" :footer="null" :closable="false" :z-index="1200" width="100%"
    class="instructions-editor-modal" wrap-class-name="instructions-editor-wrap" destroy-on-hidden :initial-focus-el="instructionsFocus"
    @cancel="instructionsEditorOpen = false">
    <template #header-actions><v-button @click="instructionsEditorOpen = false"><IconMinimize aria-hidden="true" />{{ t('exitFullscreenEdit') }}</v-button></template>
    <SchemaForm id="fullscreen-agent-instructions" v-model="instructionsDocument" :schema="instructionsSchema" :uischema="instructionsUISchema" class="instructions-editor-input"
      :disabled="agentSettingsLoading || agentSettingsBusy" :reset-key="agentSettingsResetKey" />
  </v-modal>
  <v-modal :open="modelFormOpen" centered :z-index="1200" width="min(640px, calc(100vw - 2rem))"
    class="provider-connection-modal" :title="editingProvider ? t('editProviderConnection') : t('connectProvider')"
    :footer="null" :close-disabled="settingsBusy" :keyboard="!settingsBusy && !providerDiscardOpen" :mask-closable="!settingsBusy && !providerDiscardOpen" destroy-on-hidden
    :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }"
    @cancel="closeProviderForm">
    <form v-if="modelFormOpen" class="model-form" @submit.prevent="!loginBusy && saveProvider()">
      <v-alert v-if="settingsError" type="error" role="alert" :message="settingsError" />
      <template v-if="providerConflict"><v-alert type="warning" role="alert" :message="t('networkConflict')" /><v-button type="button" @click="reloadProviderDraft">{{ t('networkReload') }}</v-button></template>
      <div v-if="editingProvider" class="provider-kind-field"><span>{{ t('provider') }}</span><span class="provider-kind-value">{{ providerTypeLabel(modelDraft.kind, editingProvider.provider) }}</span></div>
      <SchemaForm v-else id="provider-choice" class="provider-kind-select" v-model="providerChoice" :schema="providerChoiceSchema" :uischema="providerChoiceUISchema" :disabled="settingsBusy" />
      <p v-if="modelDraft.kind && modelDraft.credentialMode !== 'oauth' && !manualModelIDs" class="provider-help">{{ t(deploymentModelSelection ? 'azureModelHint' : 'providerModelsAutomatic') }}</p>
      <SchemaForm v-if="modelDraft.kind" id="model-connection-config" class="provider-public-config" v-model="modelConfig"
        :schema="modelConfigSchema" :uischema="modelConfigUISchema" :disabled="settingsBusy || loginBusy" :reset-key="modelConfigResetKey"
        @validity="modelConfigValid = $event" @dirty="modelConfigDirty = $event" />
      <p v-if="modelDraft.kind" class="provider-help">{{ t('configEditorPrivateHint') }}</p>
      <div v-if="modelDraft.kind" class="provider-proxy-field">
        <v-alert v-if="networkConfigData.error.value" type="error" role="alert" :message="networkConfigData.error.value" />
        <ProxySelection id="provider-proxy" v-model="modelDraft.proxyId" :profiles="proxyProfiles" :disabled="settingsBusy || loginBusy || !networkConfigData.value.value" @validity="providerProxyValid = $event" />
      </div>
      <p v-if="modelDraft.kind === 'github-copilot'" class="provider-help">{{ t('copilotAdvancedHint') }}</p>
      <fieldset v-if="modelDraft.credentialMode === 'oauth'" class="provider-login-workflow" :disabled="providerConflict || !modelConfigValid || !providerProxyValid || !networkConfigData.value.value || settingsBusy">
        <ProviderLogin :key="`${modelDraft.kind}:${modelDraft.key}`" :kind="modelDraft.kind === 'github-copilot' ? 'copilot' : 'codex'" :client="workspace.client" :name="modelDraft.name" :connection-key="modelDraft.key" :proxy-id="modelDraft.proxyId" :revision="editingProvider ? providerDraftVersion : undefined" :copilot-auth="modelDraft.copilotAuth" @busy="loginBusy = $event" @connected="providerConnected" />
      </fieldset>
      <div v-if="modelDraft.kind === 'custom'" class="provider-auth-heading provider-auth-info">
        <span>{{ t('providerAuthentication') }}</span>
        <v-tooltip :trigger="['hover', 'focus']" placement="topRight" :styles="{ root: { maxWidth: 'min(360px, calc(100vw - 32px))' } }">
          <template #title><div class="provider-auth-tooltip">
            <template v-if="authenticationHeader"><p>{{ t('customAuthHeader') }}</p><code>{{ authenticationHeader }}</code></template>
            <p v-else>{{ t('customAuthNone') }}</p>
            <p>{{ t('customAuthOverrideHint') }}</p>
          </div></template>
          <v-button class="icon-button provider-auth-help" type="button" variant="text" :aria-label="t('apiKeyHelp')"><IconAlertCircle /></v-button>
        </v-tooltip>
      </div>
      <p v-if="modelDraft.credentialMode === 'default' && (modelDraft.kind === 'amazon-bedrock' || modelDraft.kind === 'google-vertex')" class="provider-help">{{ t(modelDraft.kind === 'amazon-bedrock' ? 'awsCredentialsHint' : 'googleCredentialsHint') }}</p>
      <SchemaForm v-if="showModelCredentials" id="model-credentials" class="provider-public-config" v-model="modelCredentials" :schema="modelCredentialsSchema" :uischema="modelCredentialsUISchema"
        :disabled="settingsBusy || loginBusy" :reset-key="`${modelConfigResetKey}:${modelDraft.credentialMode}`" @validity="modelCredentialsValid = $event" @dirty="providerCredentialDirty = $event" />
      <label v-if="deploymentModelSelection" class="catalog-model-field">{{ t('model') }}<v-select :value="chosenModels[0]" :options="providerModelOptions" show-search option-filter-prop="searchText" allow-clear :placeholder="t('chooseModel')" @update:value="updateSelectedModels($event ? [String($event)] : [])" /></label>
      <div v-if="manualModelIDs && modelDraft.kind === 'custom'" class="model-discovery-fallback">
        <p class="provider-help">{{ t('providerModelsUnavailable') }}<v-button variant="link" :disabled="settingsBusy || !modelConfigValid" @click="retryModelDiscovery">{{ t('retryModelDiscovery') }}</v-button></p>
        <label class="model-list-field">{{ t('manualModelIds') }}<v-select :value="chosenModels" :options="chosenModels.map(id => ({ label: modelNameById(id), searchText: `${id} ${modelNameById(id)}`, value: id }))" mode="tags" show-search option-filter-prop="searchText" allow-clear :placeholder="t('selectOrTypeModel')" :disabled="settingsBusy" @update:value="updateSelectedModels(Array.isArray($event) ? $event.map(String) : [])" /><small>{{ t('manualModelIdsHint') }}</small></label>
      </div>
      <small v-if="editingProvider && modelDraft.kind === 'custom'">{{ t('changeAddressHint') }}</small>
      <div class="actions"><v-button type="button" :disabled="settingsBusy" @click="closeProviderForm">{{ t('cancel') }}</v-button><v-button v-if="modelDraft.kind && (modelDraft.credentialMode !== 'oauth' || editingProvider)" type="submit" variant="primary" :loading="settingsBusy" :disabled="settingsBusy || loginBusy || !canSaveProvider || !providerProxyValid || !networkConfigData.value.value">{{ editingProvider ? t('saveChanges') : t('connectProvider') }}</v-button></div>
    </form>
  </v-modal>
  <v-modal :open="!!proxyBindingTarget" :title="t('networkProxy')" :footer="null" :z-index="1200" :close-disabled="settingsBusy" :keyboard="!settingsBusy && !providerDiscardOpen" :mask-closable="!settingsBusy && !providerDiscardOpen" @cancel="closeProxyBinding">
    <p>{{ proxyBindingTarget?.name }}</p><router-link :to="{ name: 'settings-network', query: route.query }">{{ t('networkSettings') }}</router-link>
    <ProxySelection id="advanced-provider-proxy" v-model="proxyBindingValue" :profiles="proxyProfiles" :disabled="settingsBusy || !networkConfigData.value.value" @validity="proxyBindingValid = $event" />
    <v-alert v-if="proxyBindingConflict" type="warning" role="alert" :message="t('networkConflict')" /><v-button v-if="proxyBindingConflict" @click="reloadProxyBinding">{{ t('networkReload') }}</v-button>
    <v-alert v-if="proxyBindingError" type="error" role="alert" :message="proxyBindingError" />
    <div class="actions"><v-button :disabled="settingsBusy" @click="closeProxyBinding">{{ t('cancel') }}</v-button><v-button variant="primary" :loading="settingsBusy" :disabled="!proxyBindingDirty || !proxyBindingValid || proxyBindingConflict" @click="saveProxyBinding">{{ t('saveChanges') }}</v-button></div>
  </v-modal>
  <v-modal :open="providerDiscardOpen" :title="t('networkDiscard')" :footer="null" :z-index="1400" @cancel="finishProviderDiscard(false)"><p>{{ t('configEditorDiscard') }}</p><div class="actions"><v-button @click="finishProviderDiscard(false)">{{ t('cancel') }}</v-button><v-button danger @click="finishProviderDiscard(true)">{{ t('networkDiscard') }}</v-button></div></v-modal>
</template>
<style scoped>
.provider-kind-select,.provider-kind-field,.provider-public-config,.provider-auth-info,.provider-login-workflow { grid-column: 1 / -1; min-width: 0; }
.provider-proxy-field { grid-column: 1 / -1; min-width: 0; }
.provider-login-workflow { margin: 0; padding: 0; border: 0; }
</style>
