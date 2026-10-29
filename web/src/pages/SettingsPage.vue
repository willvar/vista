<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeftOutlined, RightOutlined, SkinOutlined, UserOutlined, ApiOutlined, InfoCircleOutlined, SearchOutlined, LinkOutlined, BookOutlined, SunOutlined, MoonOutlined, DesktopOutlined, HddOutlined, ExclamationCircleOutlined, FullscreenOutlined, FullscreenExitOutlined, PlusOutlined, ImportOutlined, ReloadOutlined, CloseOutlined, RobotOutlined, TeamOutlined, CompassOutlined, ToolOutlined, CommentOutlined, FolderOpenOutlined, FolderOutlined, FileTextOutlined } from '@antdv-next/icons'
import { Alert as AAlert, Button as AButton, Input as AInput, InputNumber as AInputNumber, InputPassword as AInputPassword, Modal as AModal, TextArea as ATextarea, Select as ASelect, Switch as ASwitch, Tooltip as ATooltip, type TextAreaRef } from 'antdv-next'
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
import VistaMark from '../VistaMark.vue'
import { useWorkspace } from '../composables/useWorkspace'
import { useProviderSettings } from '../composables/useProviderSettings'
import { useAppearance } from '../ui/appearance'

const route = useRoute()
const { t } = useI18n({ useScope: 'global' })
const { appearance, setAppearance } = useAppearance()
const appearances = [{ value: 'light', icon: SunOutlined }, { value: 'dark', icon: MoonOutlined }, { value: 'system', icon: DesktopOutlined }] as const
const delegationExamples = ['delegationDepthSolo', 'delegationDepthMain', 'delegationDepthNested'] as const
const delegationNodes = [
  { x: 50, y: 14, level: 0 },
  { x: 25, y: 50, level: 1 }, { x: 75, y: 50, level: 1 },
  { x: 12.5, y: 86, level: 2 }, { x: 37.5, y: 86, level: 2 },
  { x: 62.5, y: 86, level: 2 }, { x: 87.5, y: 86, level: 2 },
]
const workspace = useWorkspace()
const { modelCatalog, modelChoices, modelNameById, backToWorkspace, debugMode, serverStatus } = workspace
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
  { name: 'settings-general', label: 'appearance', icon: SkinOutlined },
  { name: 'settings-personalization', label: 'personalizationSettings', icon: UserOutlined },
  { name: 'settings', label: 'modelConnections', icon: ApiOutlined },
  { name: 'settings-search', label: 'searchSettings', icon: SearchOutlined },
  { name: 'settings-mcp', label: 'mcpSettings', icon: LinkOutlined },
  { name: 'settings-skills', label: 'skillsSettings', icon: BookOutlined },
  { name: 'settings-storage', label: 'storageSettings', icon: HddOutlined },
  { name: 'settings-about', label: 'aboutSettings', icon: InfoCircleOutlined },
]
const settingsIndex = computed(() => route.name === 'settings-index')
const settingsTitle = computed(() => t(sections.find(section => section.name === route.name)?.label || 'settings'))
const mobileViewport = window.matchMedia('(max-width: 830px)')
const mobile = ref(mobileViewport.matches)
const pageHeading = ref<HTMLElement>()
function updateViewport() { mobile.value = mobileViewport.matches }
onMounted(() => mobileViewport.addEventListener('change', updateViewport))
onUnmounted(() => mobileViewport.removeEventListener('change', updateViewport))
watch([settingsIndex, mobile], ([index, small]) => {
  if (index && !small) void workspace.router.replace({ name: 'settings-general', query: route.query })
}, { immediate: true })
watch(() => route.name, async () => {
  if (!mobile.value) return
  await nextTick()
  pageHeading.value?.focus({ preventScroll: true })
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
const storageModalOpen = ref(false)
const resetModalOpen = ref(false)
const providerImportOpen = ref(false)
const providerImport = ref<InstanceType<typeof ConfigurationImport>>()
const importedCount = ref(0)
const instructionsEditorOpen = ref(false)
const instructionsEditor = ref<TextAreaRef>()
const {
  languagePreference, languageChoice, languageOptions, regionOptions, settingsBusy, settingsError,
  providerDisconnectTarget, providerDisconnectError, settings,
  agentInstructions, agentInstructionsPath,
  delegationDepth, maxDelegationDepth, canSaveAgentInstructions, canSaveDelegationDepth, automaticTitles, setAutomaticTitles,
  agentSettingsBusy, agentSettingsLoading, agentSettingsError, agentSettingsSaved,
  loadAgentSettings, saveAgentSettings, modelDraft, modelFormOpen, chosenModels,
  manualModelIDs, canReuseHeader, editingProvider, providerOptions, canSaveProvider,
  selectedSetup, deploymentModelSelection, providerModelOptions, apiFormatOptions, credentialOptions, usesAPIKey, authenticationHeader, changeCredentialMode,
  updateSelectedModels, changeProvider, changeCustomBaseURL,
  startProviderConnection, cancelModelForm, loadModelSettings, editProvider, providerGroups,
  saveProvider, retryModelDiscovery, providerConnected, setDefaultModel, confirmDisconnectProvider, updateProviderDisconnect,
} = useProviderSettings(workspace)
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
  : ['settings-mcp', 'settings-skills', 'settings-storage', 'settings-about'].includes(String(route.name)) ? sectionActions.value?.headerAction : undefined)
const loginBusy = ref(false)
watch(() => modelDraft.value.kind, () => { loginBusy.value = false })
function providerTypeLabel(kind: string, protocol: string) {
  return providerOptions.value.find(option => option.value === kind)?.label || protocol
}
function closeProviderForm() {
  if (!settingsBusy.value) cancelModelForm()
}

function focusInstructionsEditor(open: boolean) {
  if (open && instructionsEditorOpen.value) instructionsEditor.value?.focus()
}

watch(() => route.fullPath, () => {
  importedCount.value = 0
  instructionsEditorOpen.value = false
  providerDisconnectTarget.value = null
  cancelModelForm()
  if (route.name === 'settings-personalization') void loadAgentSettings()
  else if (route.name === 'settings') void loadModelSettings()
}, { immediate: true })
</script>

<template>
<WorkspaceModal :open="true" width="max-content" :keyboard="!providerDisconnectTarget && !modelFormOpen && !providerImportOpen && !mcpModalOpen && !skillModalOpen && !memoryModalOpen && !searchModalOpen && !storageModalOpen && !resetModalOpen && !instructionsEditorOpen" @cancel="backToWorkspace"><section class="workspace settings-page modal-page" :class="{ 'settings-index': settingsIndex }">
        <div class="conversation-header">
          <a-button type="text" class="page-back" :aria-label="t(settingsIndex ? 'backToConversation' : 'backToSettings')" @click="back"><ArrowLeftOutlined /></a-button>
          <div class="page-heading"><small v-if="!settingsIndex">{{ t('settingsHeading') }}</small><h2 ref="pageHeading" tabindex="-1">{{ settingsTitle }}</h2></div>
          <div class="settings-header-actions">
            <a-button v-if="canImport" type="text" class="settings-header-action settings-header-button" :aria-label="t(importTitleKey)" :title="t(importTitleKey)" :disabled="importKind === 'providers' ? settingsBusy : headerAction?.disabled" @click="importedCount = 0; providerImport?.show()"><template #icon><ImportOutlined aria-hidden="true" /></template></a-button>
            <a-button v-if="headerAction" type="text" class="settings-header-action settings-header-button" :aria-label="t(headerAction.label)" :title="t(headerAction.label)" :disabled="headerAction.disabled" :loading="headerAction.loading" @click="headerAction.run()">
              <template #icon><PlusOutlined v-if="route.name === 'settings' || route.name === 'settings-mcp'" aria-hidden="true" /><ReloadOutlined v-else aria-hidden="true" /></template>
            </a-button>
            <a-button type="text" class="modal-close settings-header-button" :aria-label="t('closeSettings')" :title="t('closeSettings')" @click="backToWorkspace">
              <template #icon><CloseOutlined aria-hidden="true" /></template>
            </a-button>
          </div>
        </div>
        <div class="settings-body">
        <nav class="settings-nav" :aria-label="t('settings')">
          <router-link v-for="section in sections" :key="section.name" :to="{ name: section.name, query: route.query }" :class="{ active: route.name === section.name }" :aria-current="route.name === section.name ? 'page' : undefined"><component :is="section.icon" aria-hidden="true" /><span>{{ t(section.label) }}</span><RightOutlined class="settings-nav-chevron" aria-hidden="true" /></router-link>
        </nav>
        <SearchSettings v-if="route.name === 'settings-search'" @modal-open="searchModalOpen = $event" />
        <MCPSettings ref="sectionActions" v-if="route.name === 'settings-mcp'" @modal-open="mcpModalOpen = $event" />
        <SkillsSettings ref="sectionActions" v-if="route.name === 'settings-skills'" @modal-open="skillModalOpen = $event" />
        <ResourceStorageSettings ref="sectionActions" v-if="route.name === 'settings-storage'" @modal-open="storageModalOpen = $event" />
        <ConfigurationImport v-if="canImport" :key="importKind" ref="providerImport" :kind="importKind" @modal-open="providerImportOpen = $event" @imported="onImported" />
        <div v-if="route.name === 'settings-general'" class="settings-content appearance-settings-content">
          <div class="general-setting appearance-setting"><label id="appearance-label">{{ t('colorMode') }}</label><div class="appearance-options" role="group" aria-labelledby="appearance-label"><button v-for="mode in appearances" :key="mode.value" type="button" class="appearance-option" :class="{ selected: appearance === mode.value }" :aria-pressed="appearance === mode.value" @click="setAppearance(mode.value)"><span class="appearance-preview" :data-mode="mode.value"><i></i><span><b></b><b></b><b></b></span></span><span class="appearance-option-label"><component :is="mode.icon" aria-hidden="true" />{{ t(`appearance${mode.value[0]!.toUpperCase()}${mode.value.slice(1)}`) }}</span></button></div></div>
          <div class="locale-settings general-setting" :class="{ 'locale-settings-auto': regionOptions.length <= 1 }">
            <div class="language-setting"><label for="interface-language">{{ t('language') }}</label><a-select id="interface-language" v-model:value="languageChoice" :options="languageOptions" show-search option-filter-prop="searchText" :aria-label="t('language')" /></div>
            <div v-if="regionOptions.length > 1" class="region-setting"><label for="interface-region">{{ t('countryRegion') }}</label><a-select id="interface-region" v-model:value="languagePreference" :options="regionOptions" show-search option-filter-prop="searchText" :aria-label="t('countryRegion')" /></div>
          </div>
          <div class="model-selection-setting general-setting"><label for="model-selection-mode">{{ t('selectionMode') }}</label><a-select id="model-selection-mode" v-model:value="modelSelectionMode" :options="selectionModeOptions" :disabled="!preferencesReady" :aria-label="t('selectionMode')" /><p>{{ t('selectionModeSettingsHint') }}</p></div>
          <ModelSelectionStats v-if="modelSelectionMode === 'auto'" />
        </div>
        <div v-if="route.name === 'settings-personalization'" class="settings-content personalization-settings-content">
          <a-alert v-if="agentSettingsError" type="error" role="alert" :message="agentSettingsError" />
          <a-alert v-if="agentSettingsSaved" type="success" role="status" :message="t('agentSettingsSaved')" />
          <form class="agent-instructions-form" :aria-label="t('globalInstructions')" @submit.prevent="saveAgentSettings('instructions')">
            <div class="general-setting">
              <div class="agent-instructions-heading"><label for="agent-instructions">{{ t('globalInstructions') }}</label><a-button :disabled="agentSettingsLoading || agentSettingsBusy" @click="instructionsEditorOpen = true"><FullscreenOutlined aria-hidden="true" />{{ t('fullscreenEdit') }}</a-button></div>
              <figure id="global-instructions-hint" class="personalization-figure instructions-figure" :aria-label="t('instructionsFigureLabel')">
                <div class="instructions-figure-heading"><span>{{ t('instructionsFramework') }}</span><small>{{ t('instructionsGlobalScope') }}</small></div>
                <div class="instructions-workflow">
                  <div class="personalization-figure-node"><CompassOutlined aria-hidden="true" /><span>{{ t('instructionsUnderstand') }}</span></div>
                  <span class="personalization-figure-link" aria-hidden="true"></span>
                  <div class="personalization-figure-node"><ToolOutlined aria-hidden="true" /><span>{{ t('instructionsAct') }}</span></div>
                  <span class="personalization-figure-link" aria-hidden="true"></span>
                  <div class="personalization-figure-node"><CommentOutlined aria-hidden="true" /><span>{{ t('instructionsRespond') }}</span></div>
                </div>
              </figure>
              <p v-if="agentInstructionsPath" class="instructions-file-location">{{ t('globalInstructionsFile') }}<code>{{ agentInstructionsPath }}</code></p>
              <a-textarea :disabled="agentSettingsLoading || agentSettingsBusy" id="agent-instructions" v-model:value="agentInstructions" :auto-size="{ minRows: 9, maxRows: 18 }" :placeholder="t('globalInstructionsPlaceholder')" :aria-label="t('globalInstructions')" aria-describedby="global-instructions-hint" />
            </div>
            <div class="agent-instructions-footer"><a-button html-type="submit" type="primary" :loading="agentSettingsBusy" :disabled="!canSaveAgentInstructions">{{ t('saveChanges') }}</a-button></div>
            <aside class="instructions-local-example" aria-labelledby="instructions-local-heading">
              <div id="instructions-local-heading" class="instructions-local-heading">{{ t('instructionsDirectoryTitle') }}</div>
              <div class="instructions-local-flow">
                <div class="instructions-directory">
                  <div class="instructions-directory-name"><FolderOpenOutlined aria-hidden="true" /><span>{{ t('instructionsExampleDirectory') }}</span></div>
                  <div class="instructions-directory-entries">
                    <div class="instructions-directory-entry instructions-directory-file"><FileTextOutlined aria-hidden="true" /><div><code>AGENTS.md</code><small>{{ t('instructionsCreateFile') }}</small></div></div>
                    <div class="instructions-directory-entry"><FolderOutlined aria-hidden="true" /><span>{{ t('instructionsSubdirectory') }}</span></div>
                  </div>
                </div>
                <span class="personalization-figure-link" aria-hidden="true"></span>
                <div class="personalization-figure-node"><RobotOutlined aria-hidden="true" /><span>{{ t('instructionsReadLocalRules') }}</span></div>
              </div>
              <p class="instructions-local-scope">{{ t('instructionsLocalScope') }}</p>
            </aside>
          </form>
          <MemorySettings @modal-open="memoryModalOpen = $event" />
          <form class="delegation-depth-form" :aria-label="t('delegationDepth')" @submit.prevent="saveAgentSettings('delegationDepth')">
            <div class="general-setting">
              <label for="delegation-depth">{{ t('delegationDepth') }}</label>
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
                      <span v-for="node in delegationNodes.filter(node => node.level <= depth)" :key="`${node.level}-${node.x}`" class="delegation-tree-agent" :class="{ 'delegation-tree-main': node.level === 0 }" :style="{ left: `${node.x}%`, top: `${node.y}%` }"><UserOutlined /></span>
                    </div>
                    <span>{{ t(label) }}</span>
                  </div>
                </div>
                <div class="delegation-depth-tradeoffs">
                  <div><TeamOutlined aria-hidden="true" /><p>{{ t('delegationDepthBenefit') }}</p></div>
                  <div class="delegation-depth-cost"><ExclamationCircleOutlined aria-hidden="true" /><p>{{ t('delegationDepthCost') }}</p></div>
                </div>
                <figcaption>{{ t('delegationDepthImpact') }}</figcaption>
              </figure>
              <a-input-number id="delegation-depth" v-model:value="delegationDepth" :min="0" :max="maxDelegationDepth" :precision="0" :disabled="agentSettingsLoading || agentSettingsBusy" :aria-label="t('delegationDepth')" aria-describedby="delegation-depth-intro delegation-depth-hint" />
            </div>
            <div class="agent-instructions-footer"><a-button html-type="submit" type="primary" :loading="agentSettingsBusy" :disabled="!canSaveDelegationDepth">{{ t('saveChanges') }}</a-button></div>
          </form>
          <div class="automatic-title-setting general-setting"><div><label for="automatic-titles">{{ t('automaticTitles') }}</label><p id="automatic-titles-hint">{{ t('automaticTitlesHint') }}</p></div><a-switch id="automatic-titles" :checked="automaticTitles" :disabled="agentSettingsLoading || agentSettingsBusy" :aria-label="t('automaticTitles')" aria-describedby="automatic-titles-hint" @update:checked="setAutomaticTitles(Boolean($event))" /></div>
        </div>
        <div v-if="route.name === 'settings'" class="settings-content">
          <div v-if="settings.models.length" class="settings-toolbar"><p>{{ t('manageProvidersHint') }}</p></div>
          <div v-else class="provider-empty-state">
            <span class="provider-empty-icon"><ApiOutlined aria-hidden="true" /></span>
            <h3>{{ t('chooseProviderHint') }}</h3>
            <p>{{ t('addProviderHint') }}</p>
          </div>
          <a-alert v-if="settingsError && !modelFormOpen" type="error" role="alert" :message="settingsError" />
          <a-alert v-if="importedCount" type="success" role="status" :message="t('providerImportSuccess', { count: importedCount })" />
          <div v-if="settings.models.length" class="default-model-setting"><label>{{ t('defaultModel') }}<a-select :value="modelCatalog.default" :options="modelChoices" show-search option-filter-prop="searchText" option-label-prop="fullLabel" :disabled="settingsBusy" @update:value="setDefaultModel(String($event))" /></label></div>
          <div v-if="providerGroups.length" class="configured-providers"><h3>{{ t('connectedProviders') }}</h3>
            <div v-for="group in providerGroups" :key="group.key" class="configured-provider">
              <div class="provider-heading"><div><strong>{{ group.name }}</strong><small class="provider-type">{{ providerTypeLabel(group.kind, group.protocol) }}</small><small v-if="group.baseUrl">{{ group.baseUrl }}</small></div><div v-if="group.models.every(model => model.editable)" class="actions"><a-button size="small" :disabled="settingsBusy" @click="editProvider(group.models[0]!)">{{ t('edit') }}</a-button><confirm-popover :show="providerDisconnectTarget?.key === group.models[0]!.key" :message="t('disconnectProviderConfirm', { name: group.name })" :cancel-text="t('cancel')" :confirm-text="t('disconnectProvider')" :busy="settingsBusy" :error="providerDisconnectError" popover-class="provider-disconnect-popover" @update:show="updateProviderDisconnect($event, group.models[0]!.key, group.name)" @confirm="confirmDisconnectProvider"><template #trigger><a-button size="small" danger :disabled="settingsBusy">{{ t('disconnectProvider') }}</a-button></template></confirm-popover></div></div>
            </div>
          </div>
        </div>
        <div v-if="route.name === 'settings-about'" class="settings-content about-settings-content">
          <div class="about-brand">
            <VistaMark />
            <h3>Vista<small v-if="serverStatus?.version" class="about-version">{{ serverStatus.version }}</small></h3>
            <p class="about-tagline">{{ t('vistaTagline') }}</p>
          </div>
          <div class="debug-mode-setting general-setting"><div><label for="debug-mode">{{ t('debugMode') }}</label><p id="debug-mode-hint">{{ t('debugModeHint') }}</p></div><a-switch id="debug-mode" v-model:checked="debugMode" :aria-label="t('debugMode')" aria-describedby="debug-mode-hint" /></div>
          <ResetVistaSettings v-if="debugMode" @modal-open="resetModalOpen = $event" />
          <OMPCatalogSettings ref="sectionActions" :client="workspace.client" />
          <UserAgentSettings :client="workspace.client" />
          <CapacitySettings :client="workspace.client" :status="serverStatus" />
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
      </section></WorkspaceModal>
  <a-modal :open="instructionsEditorOpen" :title="t('globalInstructions')" :footer="null" :closable="false" :z-index="1200" width="100%"
    class="instructions-editor-modal" wrap-class-name="instructions-editor-wrap" destroy-on-hidden :after-open-change="focusInstructionsEditor"
    @cancel="instructionsEditorOpen = false">
    <template #title><div class="instructions-editor-heading"><span>{{ t('globalInstructions') }}</span><a-button @click="instructionsEditorOpen = false"><FullscreenExitOutlined aria-hidden="true" />{{ t('exitFullscreenEdit') }}</a-button></div></template>
    <a-textarea ref="instructionsEditor" id="fullscreen-agent-instructions" v-model:value="agentInstructions" class="instructions-editor-input"
      :disabled="agentSettingsLoading || agentSettingsBusy" :auto-size="false" :aria-label="t('globalInstructions')"
      :placeholder="t('globalInstructionsPlaceholder')" />
  </a-modal>
  <a-modal :open="modelFormOpen" centered :z-index="1200" width="min(640px, calc(100vw - 2rem))"
    class="provider-connection-modal" :title="editingProvider ? t('editProviderConnection') : t('connectProvider')"
    :footer="null" :closable="!settingsBusy" :keyboard="!settingsBusy" :mask-closable="!settingsBusy" destroy-on-hidden
    :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }"
    @cancel="closeProviderForm">
    <form v-if="modelFormOpen" class="model-form" @submit.prevent="saveProvider">
      <a-alert v-if="settingsError" type="error" role="alert" :message="settingsError" />
      <div v-if="editingProvider" class="provider-kind-field"><span>{{ t('provider') }}</span><span class="provider-kind-value">{{ providerTypeLabel(modelDraft.kind, editingProvider.provider) }}</span></div>
      <label v-else>{{ t('provider') }}<a-select :value="modelDraft.kind || null" :options="providerOptions" show-search option-filter-prop="searchText" :placeholder="t('chooseProvider')" :disabled="settingsBusy" @update:value="changeProvider(String($event))" /></label>
      <p v-if="modelDraft.kind && modelDraft.credentialMode !== 'oauth' && !manualModelIDs" class="provider-help">{{ t(deploymentModelSelection ? 'azureModelHint' : 'providerModelsAutomatic') }}</p>
      <label v-if="modelDraft.kind">{{ t('providerName') }}<a-input v-model:value="modelDraft.name" :disabled="loginBusy" :placeholder="modelDraft.kind === 'custom' ? t('providerNameExample') : selectedSetup?.name" :maxlength="64" /></label>
      <details v-if="modelDraft.kind === 'github-copilot'" class="provider-advanced" :open="editingProvider?.copilotAuth?.enterpriseUrl || editingProvider?.copilotAuth?.clientId ? true : undefined">
        <summary>{{ t('advancedSettings') }}</summary>
        <div class="provider-login-fields">
          <label for="copilot-enterprise-url">{{ t('copilotEnterpriseURL') }}<a-input id="copilot-enterprise-url" v-model:value="modelDraft.copilotAuth.enterpriseUrl" placeholder="company.ghe.com" :maxlength="2048" :disabled="loginBusy" /></label>
          <label for="copilot-client-id">OAuth Client ID<a-input id="copilot-client-id" v-model:value="modelDraft.copilotAuth.clientId" :placeholder="t('copilotDefaultClient')" :maxlength="200" :disabled="loginBusy" /></label>
          <p class="provider-help">{{ t('copilotAdvancedHint') }}</p>
        </div>
      </details>
      <ProviderLogin v-if="modelDraft.credentialMode === 'oauth'" :key="`${modelDraft.kind}:${modelDraft.key}`" :kind="modelDraft.kind === 'github-copilot' ? 'copilot' : 'codex'" :client="workspace.client" :name="modelDraft.name" :connection-key="modelDraft.key" :copilot-auth="modelDraft.copilotAuth" @busy="loginBusy = $event" @connected="providerConnected" />
      <label v-if="modelDraft.credentialMode !== 'oauth' && selectedSetup?.fields?.includes('baseUrl')">{{ t('baseUrl') }}<a-input :value="modelDraft.baseUrl" :placeholder="selectedSetup?.baseUrl || 'https://provider.example/v1'" :maxlength="2048" required @change="changeCustomBaseURL($event.target.value || '')" /></label>
      <label v-if="modelDraft.kind === 'custom'" for="provider-api-format">{{ t('conversationAPIFormat') }}<a-select id="provider-api-format" v-model:value="modelDraft.apiFormat" :options="apiFormatOptions" :aria-label="t('conversationAPIFormat')" /></label>
      <label v-if="selectedSetup?.fields?.includes('resourceName')">{{ t('azureResourceName') }}<a-input v-model:value="modelDraft.resourceName" placeholder="my-resource" :maxlength="128" required /></label>
      <label v-if="selectedSetup?.fields?.includes('region')">{{ t('providerRegion') }}<a-input v-model:value="modelDraft.region" placeholder="us-east-1" :maxlength="64" /></label>
      <label v-if="selectedSetup?.fields?.includes('project')">{{ t('googleCloudProject') }}<a-input v-model:value="modelDraft.project" placeholder="my-project" :maxlength="128" :required="modelDraft.credentialMode !== 'apiKey'" /></label>
      <label v-if="selectedSetup?.fields?.includes('location')">{{ t('providerLocation') }}<a-input v-model:value="modelDraft.location" placeholder="us-central1" :maxlength="64" /></label>
      <div v-if="credentialOptions.length && modelDraft.credentialMode !== 'oauth'" class="provider-auth-field">
        <label for="provider-authentication">{{ t('providerAuthentication') }}<a-select id="provider-authentication" :value="modelDraft.credentialMode" :options="credentialOptions" @update:value="changeCredentialMode(String($event))" /></label>
        <a-tooltip v-if="modelDraft.kind === 'custom'" :trigger="['hover', 'focus']" placement="topRight" :styles="{ root: { maxWidth: 'min(360px, calc(100vw - 32px))' } }">
          <template #title><div class="provider-auth-tooltip">
            <template v-if="authenticationHeader"><p>{{ t('customAuthHeader') }}</p><code>{{ authenticationHeader }}</code></template>
            <p v-else>{{ t('customAuthNone') }}</p>
            <p>{{ t('customAuthOverrideHint') }}</p>
          </div></template>
          <a-button class="provider-auth-help" html-type="button" type="text" size="small" :aria-label="t('apiKeyHelp')"><ExclamationCircleOutlined /></a-button>
        </a-tooltip>
      </div>
      <p v-if="modelDraft.credentialMode === 'default' && (modelDraft.kind === 'amazon-bedrock' || modelDraft.kind === 'google-vertex')" class="provider-help">{{ t(modelDraft.kind === 'amazon-bedrock' ? 'awsCredentialsHint' : 'googleCredentialsHint') }}</p>
      <label v-if="modelDraft.kind && usesAPIKey">{{ t(modelDraft.kind === 'opencode' || modelDraft.kind === 'opencode-go' ? 'opencodeApiKey' : 'providerApiKey') }}<a-input-password v-model:value="modelDraft.apiKey" :maxlength="4096" :placeholder="editingProvider?.hasApiKey ? t('reuseSavedKey') : t('enterApiKey')" /></label>
      <label v-if="modelDraft.credentialMode === 'accessToken'">{{ t('providerAccessToken') }}<a-input-password v-model:value="modelDraft.accessToken" :maxlength="4096" :placeholder="editingProvider?.hasAccessToken ? t('reuseSavedKey') : t('enterAccessToken')" /></label>
      <details v-if="modelDraft.kind === 'custom'" :key="modelDraft.key" class="provider-advanced" :open="editingProvider?.headerNames?.length ? true : undefined">
        <summary>{{ t('advancedSettings') }}</summary>
        <div class="provider-headers">
          <p class="provider-help">{{ t('customHeadersHint') }}</p>
          <div v-for="(header, index) in modelDraft.headers" :key="index" class="provider-header-row">
            <label>{{ t('headerName') }}<a-input v-model:value="header.name" :aria-label="`${t('headerName')} ${index + 1}`" placeholder="X-API-Key" :maxlength="256" /></label>
            <label>{{ t('headerValue') }}<a-input-password v-model:value="header.value" :aria-label="`${t('headerValue')} ${index + 1}`" :placeholder="canReuseHeader(header.name) ? t('reuseSavedHeader') : t('headerValue')" :maxlength="8192" /></label>
            <a-button type="text" html-type="button" :aria-label="`${t('removeHeader')} ${index + 1}`" @click="modelDraft.headers.splice(index, 1)">×</a-button>
          </div>
          <a-button html-type="button" size="small" :disabled="modelDraft.headers.length >= 32" @click="modelDraft.headers.push({ name: '', value: '' })">{{ t('addHeader') }}</a-button>
        </div>
      </details>
      <label v-if="deploymentModelSelection" class="catalog-model-field">{{ t('model') }}<a-select :value="chosenModels[0]" :options="providerModelOptions" show-search option-filter-prop="searchText" allow-clear :placeholder="t('chooseModel')" @update:value="updateSelectedModels($event ? [String($event)] : [])" /></label>
      <label v-if="selectedSetup?.fields?.includes('deploymentId')">{{ t('azureDeploymentName') }}<a-input v-model:value="modelDraft.deploymentId" placeholder="my-deployment" :maxlength="128" required /></label>
      <label v-if="selectedSetup?.fields?.includes('apiVersion')">{{ t('azureApiVersion') }}<a-input v-model:value="modelDraft.apiVersion" placeholder="v1" :maxlength="32" /></label>
      <div v-if="manualModelIDs && modelDraft.kind === 'custom'" class="model-discovery-fallback">
        <p class="provider-help">{{ t('providerModelsUnavailable') }}<a-button type="link" size="small" :disabled="settingsBusy" @click="retryModelDiscovery">{{ t('retryModelDiscovery') }}</a-button></p>
        <label class="model-list-field">{{ t('manualModelIds') }}<a-select :value="chosenModels" :options="chosenModels.map(id => ({ label: modelNameById(id), searchText: `${id} ${modelNameById(id)}`, value: id }))" mode="tags" show-search option-filter-prop="searchText" allow-clear :placeholder="t('selectOrTypeModel')" :disabled="settingsBusy" @update:value="updateSelectedModels(Array.isArray($event) ? $event.map(String) : [])" /><small>{{ t('manualModelIdsHint') }}</small></label>
      </div>
      <small v-if="editingProvider && modelDraft.kind === 'custom'">{{ t('changeAddressHint') }}</small>
      <div class="actions"><a-button html-type="button" :disabled="settingsBusy" @click="closeProviderForm">{{ t('cancel') }}</a-button><a-button v-if="modelDraft.kind && (modelDraft.credentialMode !== 'oauth' || editingProvider)" html-type="submit" type="primary" :loading="settingsBusy" :disabled="settingsBusy || loginBusy || !canSaveProvider">{{ editingProvider ? t('saveChanges') : t('connectProvider') }}</a-button></div>
    </form>
  </a-modal>
</template>
