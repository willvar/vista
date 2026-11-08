<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Modal as VModal } from './ui'
import { EditOutlined, SearchOutlined, FileTextOutlined, RobotOutlined, GlobalOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import ConfirmPopover from './ConfirmPopover.vue'
import SchemaForm from './SchemaForm.vue'
import type { FormIssue } from './schema/validation'
import type { JsonSchema, ControlElement, VerticalLayout } from '@jsonforms/core'

type SearchProvider = { name: string; source: 'settings' | 'environment' | 'none' }
const names: Record<string, string> = { exa: 'Exa', firecrawl: 'Firecrawl', parallel: 'Parallel', tavily: 'Tavily', deepseek: 'DeepSeek', 'deepseek-fetch': 'DeepSeek' }
const jinaName = 'Jina Reader'
type OrderKind = 'order' | 'fetchOrder'
type PreviewKind = 'searchPreviewBytes' | 'fetchPreviewBytes'
const orderSections: { key: OrderKind; preview: PreviewKind }[] = [
  { key: 'order', preview: 'searchPreviewBytes' },
  { key: 'fetchOrder', preview: 'fetchPreviewBytes' },
]
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const providers = ref<SearchProvider[]>([])
const jinaReader = ref<SearchProvider>({ name: 'jina', source: 'none' })
const deepseekFetch = ref<SearchProvider>({ name: 'deepseek', source: 'none' })
const previews = reactive<Record<PreviewKind, { value: number; saved: number }>>({
  searchPreviewBytes: { value: 51200, saved: 51200 },
  fetchPreviewBytes: { value: 51200, saved: 51200 },
})
function previewValue(key: PreviewKind) { return previews[key].value }
function previewDirty(key: PreviewKind) { return previews[key].value !== previews[key].saved }
const orders = reactive<Record<OrderKind, string[]>>({ order: [], fetchOrder: [] })
const savedOrders = reactive<Record<OrderKind, string[]>>({ order: [], fetchOrder: [] })
function orderDirty(key: OrderKind) { return orders[key].join(',') !== savedOrders[key].join(',') }
const editing = ref('')
const keyDraft = ref('')
const credentialChoice = ref('exa'), keyFormValid = ref(false), keyFormEpoch = ref(0)
const keyError = ref('')
const removingKey = ref(false)
const removeKeyError = ref('')
const removalName = computed(() => editing.value.startsWith('deepseek')
  ? `${providerName(editing.value)} · ${t(editing.value === 'deepseek-fetch' ? 'webFetchLabel' : 'webSearchLabel')}`
  : providerName(editing.value))
const editingSource = computed(() => keySource(editing.value))
const canSaveKey = computed(() => {
  const key = keyDraft.value.trim()
  return keyFormValid.value && !!key && new TextEncoder().encode(key).length <= 4096 && !/[\s\p{Cc}]/u.test(key)
})
const loading = ref(true)
const busy = ref('')
const error = ref('')
const updated = ref('')
const editorValid = ref(false), editorDirty = ref(false), editorEpoch = ref(0)
const controller = new AbortController()
watch(editing, value => emit('modal-open', !!value), { flush: 'sync' })
onUnmounted(() => { controller.abort(); emit('modal-open', false) })

async function refresh() {
  const result = await client.request<{ providers: SearchProvider[]; order: string[]; fetchOrder: string[]; jinaReader: SearchProvider; deepseekFetch: SearchProvider } & Record<PreviewKind, number>>('/search-config', { signal: controller.signal })
  if (!controller.signal.aborted) {
    // Saving a key or retrying a read must preserve unsaved ordering edits.
    for (const { key, preview } of orderSections) {
      if (!editorDirty.value && !orderDirty(key)) orders[key] = [...result[key]]
      savedOrders[key] = result[key]
      if (!editorDirty.value && !previewDirty(preview)) {
        previews[preview].value = result[preview]
      }
      previews[preview].saved = result[preview]
    }
    providers.value = result.providers
    jinaReader.value = result.jinaReader
    deepseekFetch.value = result.deepseekFetch
  }
}
async function load() {
  loading.value = true
  error.value = ''
  try { await refresh() }
  catch (cause) { if (!controller.signal.aborted) error.value = String(cause) }
  finally { loading.value = false }
}
onMounted(load)

function providerName(name: string) {
  return name === 'jina' ? jinaName : names[name] || name
}
function keySource(name: string): SearchProvider['source'] {
  return name === 'jina' ? jinaReader.value.source : name === 'deepseek-fetch' ? deepseekFetch.value.source : providers.value.find(provider => provider.name === name)?.source || 'none'
}
function credentialName(name: string, kind: OrderKind) {
  return kind === 'fetchOrder' && name === 'deepseek' ? 'deepseek-fetch' : name
}
function keyStatus(source: SearchProvider['source'], name: string) {
  if (source === 'none' && name.startsWith('deepseek')) return t('searchKeyUnconfigured')
  return t(source === 'settings' ? 'searchKeySaved' : source === 'environment' ? 'searchKeyEnvironment' : 'searchKeyAnonymous')
}
const credentialGroups = computed(() => orderSections.map(section => ({
  key: section.key, label: t(section.key === 'order' ? 'webSearchLabel' : 'webFetchLabel'),
  values: orders[section.key].map(name => credentialName(name, section.key)),
})))
const credentialChoices = computed(() => credentialGroups.value.flatMap(group => group.values.map(name => ({ name, label: providerName(name), fullLabel: `${providerName(name)} · ${group.label}` }))))
const choiceDocument = computed<Record<string, unknown>>({ get: () => ({ provider: credentialChoice.value }), set: value => { credentialChoice.value = value.provider as string } })
const choiceSchema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, required: ['provider'], properties: { provider: { type: 'string', title: t('provider'), enum: credentialChoices.value.map(item => item.name) } } }))
const choiceUISchema = computed<ControlElement>(() => ({ type: 'Control', scope: '#/properties/provider', options: {
  enumGroups: credentialGroups.value,
  enumLabels: Object.fromEntries(credentialChoices.value.map(item => [item.name, item.label])),
  enumFullLabels: Object.fromEntries(credentialChoices.value.map(item => [item.name, item.fullLabel])),
} }))
const keyDocument = computed<Record<string, unknown>>({ get: () => ({ apiKey: keyDraft.value }), set: value => { keyDraft.value = value.apiKey as string } })
const keySchema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, required: ['apiKey'], properties: { apiKey: { type: 'string', title: t('apiKey'), minLength: 1, maxLength: 4096, writeOnly: true } } }))
const keyUISchema = computed<ControlElement>(() => ({ type: 'Control', scope: '#/properties/apiKey', options: { placeholder: editingSource.value === 'settings' ? t('reuseSavedKey') : t('enterApiKey') } }))
function validKeyDocument(value: Record<string, unknown>): FormIssue[] {
  if (typeof value.apiKey !== 'string' || !value.apiKey.trim()) return [] // Schema supplies the required-field message.
  if (new TextEncoder().encode(value.apiKey.trim()).length > 4096) return [{ path: '/apiKey', message: t('configByteLimit', { limit: 4096 }) }]
  return /[\s\p{Cc}]/u.test(value.apiKey.trim()) ? [{ path: '/apiKey', message: t('configKeyWhitespace') }] : []
}
function editKey(name: string) {
  if (loading.value || busy.value) return
  editing.value = name
  keyDraft.value = ''; keyFormEpoch.value++
  keyError.value = ''
  removingKey.value = false
  removeKeyError.value = ''
}
function closeKeyEditor() {
  if (busy.value) return
  editing.value = ''
  keyDraft.value = ''
  keyError.value = ''
  removingKey.value = false
  removeKeyError.value = ''
}
const document = computed<Record<string, unknown>>({
  get: () => ({ order: [...orders.order], fetchOrder: [...orders.fetchOrder], searchPreviewBytes: previewValue('searchPreviewBytes'), fetchPreviewBytes: previewValue('fetchPreviewBytes') }),
  set: value => {
    orders.order = [...value.order as string[]]; orders.fetchOrder = [...value.fetchOrder as string[]]
    for (const key of ['searchPreviewBytes', 'fetchPreviewBytes'] as const) {
      previews[key].value = value[key] as number
    }
    updated.value = ''
  },
})
const schema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, required: ['order', 'fetchOrder', 'searchPreviewBytes', 'fetchPreviewBytes'], properties: {
  order: { type: 'array', title: t('searchOrder'), uniqueItems: true, minItems: 5, maxItems: 5, items: { type: 'string', enum: ['firecrawl', 'parallel', 'tavily', 'exa', 'deepseek'] } },
  fetchOrder: { type: 'array', title: t('fetchOrder'), uniqueItems: true, minItems: 2, maxItems: 2, items: { type: 'string', enum: ['jina', 'deepseek'] } },
  searchPreviewBytes: { type: 'integer', title: `${t('searchPreviewLength')} (${t('configEditorBytes')})`, description: t('configEditorBytesHint'), minimum: 0, maximum: Number.MAX_SAFE_INTEGER },
  fetchPreviewBytes: { type: 'integer', title: `${t('fetchPreviewLength')} (${t('configEditorBytes')})`, description: t('configEditorBytesHint'), minimum: 0, maximum: Number.MAX_SAFE_INTEGER },
} }))
const preferencesUISchema = computed<VerticalLayout>(() => ({ type: 'VerticalLayout', elements: [
  { type: 'Control', scope: '#/properties/order', options: { reorderOnly: true, enumLabels: names, description: t('searchOrderHint') } },
  { type: 'Control', scope: '#/properties/fetchOrder', options: { reorderOnly: true, enumLabels: { jina: jinaName, deepseek: names.deepseek }, description: t('fetchOrderHint') } },
  { type: 'Control', scope: '#/properties/searchPreviewBytes' },
  { type: 'Control', scope: '#/properties/fetchPreviewBytes' },
] }))
const preferencesDirty = computed(() => orderDirty('order') || orderDirty('fetchOrder') || previewDirty('searchPreviewBytes') || previewDirty('fetchPreviewBytes'))
async function savePreferences() {
  if (busy.value || loading.value || !editorValid.value || !preferencesDirty.value) return
  busy.value = 'preferences'; error.value = ''; updated.value = ''
  const next = document.value
  try {
    await client.request('/search-config', { method: 'PATCH', signal: controller.signal, body: JSON.stringify(next) })
    if (!controller.signal.aborted) {
      savedOrders.order = [...next.order as string[]]; savedOrders.fetchOrder = [...next.fetchOrder as string[]]
      previews.searchPreviewBytes.saved = next.searchPreviewBytes as number
      previews.fetchPreviewBytes.saved = next.fetchPreviewBytes as number
      editorEpoch.value++; updated.value = 'preferences'
    }
  } catch (cause) { if (!controller.signal.aborted) error.value = String(cause) }
  finally { busy.value = '' }
}

async function updateKey(remove = false) {
  const name = editing.value
  if (!name || busy.value || loading.value || (!remove && !canSaveKey.value)) return
  busy.value = name
  keyError.value = ''
  removeKeyError.value = ''
  updated.value = ''
  let saved = false
  try {
    await client.request('/search-config', {
      method: remove ? 'DELETE' : 'PUT', signal: controller.signal,
      body: JSON.stringify(remove ? { name } : { name, apiKey: keyDraft.value.trim() }),
    })
    keyDraft.value = ''
    await refresh()
    if (!controller.signal.aborted) {
      updated.value = name
      saved = true
    }
  } catch (cause) {
    if (!controller.signal.aborted) {
      if (remove) removeKeyError.value = String(cause)
      else keyError.value = String(cause)
    }
  }
  finally { busy.value = '' }
  if (saved) closeKeyEditor()
}
</script>

<template>
  <div class="settings-content search-settings-content" :aria-busy="loading">
    <p class="search-settings-hint">{{ t('searchSettingsIntro') }}</p>
    <figure class="web-capabilities" :aria-label="t('webCapabilitiesLabel')">
      <div class="web-capability-flow">
        <div class="web-flow-actor">
          <RobotOutlined aria-hidden="true" />
          <h3>{{ t('model') }}</h3>
        </div>
        <svg class="web-flow-link" viewBox="0 0 40 32" preserveAspectRatio="none" aria-hidden="true">
          <path class="web-flow-track" d="M0 7H39L35 3 M39 7L35 11 M40 25H1L5 21 M1 25L5 29" />
          <path class="web-flow-packet" pathLength="100" d="M0 7H40" />
          <path class="web-flow-packet web-flow-return" pathLength="100" d="M40 25H0" />
        </svg>
        <div class="web-flow-actor web-tools-actor">
          <h3>{{ t('webQueryTools') }}</h3>
          <div class="web-query-tool"><SearchOutlined aria-hidden="true" /><span>{{ t('webSearchLabel') }}</span></div>
          <div class="web-capability-bridge"><span>{{ t('webReadAsNeeded') }}</span></div>
          <div class="web-query-tool"><FileTextOutlined aria-hidden="true" /><span>{{ t('webFetchLabel') }}</span></div>
        </div>
        <svg class="web-flow-link web-flow-content-link" viewBox="0 0 40 32" preserveAspectRatio="none" aria-hidden="true">
          <path class="web-flow-track" d="M0 7H39L35 3 M39 7L35 11 M40 25H1L5 21 M1 25L5 29" />
          <path class="web-flow-packet" pathLength="100" d="M0 7H40" />
          <path class="web-flow-packet web-flow-return" pathLength="100" d="M40 25H0" />
        </svg>
        <div class="web-flow-actor">
          <GlobalOutlined aria-hidden="true" />
          <h3>{{ t('webOutsideContent') }}</h3>
        </div>
      </div>
      <div class="web-capability-details">
        <div><h3>{{ t('webSearchLabel') }}</h3><p>{{ t('webSearchPurpose') }}</p></div>
        <div><h3>{{ t('webFetchLabel') }}</h3><p>{{ t('webFetchPurpose') }}</p></div>
      </div>
    </figure>
    <p class="search-settings-hint">{{ t('searchSettingsHint') }}</p>

    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error" :disabled="loading || !!busy" @click="load">{{ t('retry') }}</v-button>
    <p v-if="loading" role="status">{{ t('loadingSearchSettings') }}</p>
    <form v-if="!loading && orders.order.length" class="search-config-form" @submit.prevent="savePreferences">
      <SchemaForm id="search-config" v-model="document" :schema="schema" :uischema="preferencesUISchema" :disabled="!!busy" :reset-key="editorEpoch" @validity="editorValid = $event" @dirty="editorDirty = $event">
        <template #searchPreviewBytes-before>
    <section v-if="!loading" class="web-preview-section" :aria-label="t('webPreviewLength')">
      <h2>{{ t('webPreviewLength') }}</h2>
    <figure id="web-preview-hint" class="web-preview-tradeoff" role="img" :aria-label="t('webPreviewHint')">
      <div class="web-preview-legend" aria-hidden="true">
        <span class="preview-risk"><i></i>{{ t('webPreviewRisk') }}</span>
        <span class="preview-cost"><i></i>{{ t('webPreviewCost') }}</span>
      </div>
      <div class="web-preview-chart" aria-hidden="true">
        <div class="web-preview-levels"><span>{{ t('webPreviewHigher') }}</span><span>{{ t('webPreviewLower') }}</span></div>
        <svg viewBox="0 0 480 88" preserveAspectRatio="none">
          <path class="preview-guide" d="M4 8H476 M4 80H476" />
          <path class="preview-risk" d="M4 12C140 12 210 76 476 76" />
          <path class="preview-cost" d="M4 76C210 76 340 12 476 12" />
        </svg>
      </div>
      <figcaption aria-hidden="true"><span>{{ t('webPreviewShorter') }}</span><span>{{ t('webPreviewLength') }}</span><span>{{ t('webPreviewLonger') }}</span></figcaption>
    </figure>
    </section>
        </template>
      </SchemaForm>
      <v-button type="submit" variant="primary" :loading="busy === 'preferences'" :disabled="!!busy || !editorValid || !preferencesDirty">{{ t('saveChanges') }}</v-button>
      <p v-if="updated === 'preferences'" class="search-settings-updated" role="status">{{ t('searchSettingsUpdated') }}</p>
    </form>
    <section v-if="!loading && orders.order.length" class="search-credentials" :aria-label="t('apiKey')">
      <h2>{{ t('apiKey') }}</h2>
      <SchemaForm id="search-credential-choice" v-model="choiceDocument" :schema="choiceSchema" :uischema="choiceUISchema" :disabled="!!busy" />
      <p class="search-settings-hint">{{ keyStatus(keySource(credentialChoice), credentialChoice) }}</p>
      <v-button :disabled="!!busy" @click="editKey(credentialChoice)"><EditOutlined aria-hidden="true" />{{ t('editSearchKey', { name: providerName(credentialChoice) }) }}</v-button>
      <p v-if="updated && updated !== 'preferences'" class="search-settings-updated" role="status">{{ t('searchSettingsUpdated') }}</p>
    </section>

  </div>
  <v-modal :open="!!editing" centered :z-index="1200" width="min(480px, calc(100vw - 2rem))"
    :title="t('editSearchKey', { name: providerName(editing) })" :footer="null" :closable="!busy" :keyboard="!busy && !removingKey" :mask-closable="!busy && !removingKey" destroy-on-hidden
    :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeKeyEditor">
    <form v-if="editing" class="search-key-form" :aria-label="providerName(editing)" @submit.prevent="updateKey()">
      <v-alert v-if="keyError" type="error" role="alert" :message="keyError" />
      <p class="search-settings-hint">{{ keyStatus(editingSource, editing) }}</p>
      <p v-if="editing === 'jina'" class="search-settings-hint">{{ t('jinaReaderHint') }}</p>
      <p v-if="editing === 'deepseek' || editing === 'deepseek-fetch'" class="search-settings-hint">{{ t(editing === 'deepseek' ? 'deepseekSearchKeyHint' : 'deepseekFetchKeyHint') }}</p>
      <SchemaForm id="search-provider-key" v-model="keyDocument" :schema="keySchema" :uischema="keyUISchema" :validate="validKeyDocument" :disabled="!!busy" :reset-key="keyFormEpoch" @validity="keyFormValid = $event" />
      <p v-if="editingSource === 'environment'" class="search-settings-hint">{{ t('searchKeyEnvironmentHint') }}</p>
      <div class="search-key-actions">
        <ConfirmPopover v-if="editingSource === 'settings'" :show="removingKey" :message="t('removeSearchKeyConfirm', { name: removalName })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="!!busy" :error="removeKeyError" :z-index="1300" popover-class="search-key-remove-popover" @update:show="removingKey = $event; removeKeyError = ''" @confirm="updateKey(true)">
          <template #trigger><v-button type="button" danger :disabled="!!busy">{{ t('removeSearchKey') }}</v-button></template>
        </ConfirmPopover>
        <div><v-button type="button" :disabled="!!busy" @click="closeKeyEditor">{{ t('cancel') }}</v-button><v-button type="submit" variant="primary" :loading="!!busy" :disabled="!!busy || !canSaveKey">{{ t('saveChanges') }}</v-button></div>
      </div>
    </form>
  </v-modal>
</template>

<style scoped>
.search-settings-content { display: flex; flex-direction: column; gap: 1.25rem; }
.search-settings-hint, .search-settings-updated { margin: 0; }
.search-settings-hint, .search-settings-updated { color: var(--muted); }
.web-capabilities { margin: 0; padding: 1rem; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-subtle); }
.web-capability-flow { display: grid; grid-template-columns: minmax(0, 1fr) minmax(14px, .35fr) minmax(0, 1.4fr) minmax(14px, .35fr) minmax(0, 1fr); align-items: center; }
.web-flow-actor { display: flex; min-width: 0; flex-direction: column; align-items: center; gap: .65rem; padding: 1rem .35rem; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); text-align: center; }
.web-flow-actor>.anticon { color: var(--accent); font-size: 28px; }
.web-flow-actor h3 { margin: 0; font-size: 1rem; line-height: 1.5; overflow-wrap: anywhere; }
.web-tools-actor { gap: .5rem; padding: .75rem .4rem; }
.web-tools-actor h3 { color: var(--muted); font-size: var(--font-size-small); font-weight: 400; }
.web-query-tool { display: flex; align-items: center; justify-content: center; gap: .4rem; color: var(--accent); font-size: var(--font-size-small); font-weight: 600; }
.web-query-tool>.anticon { flex: none; }
.web-query-tool>span { min-width: 0; overflow-wrap: anywhere; }
.web-capability-bridge { position: relative; align-self: stretch; padding: .4rem 0; color: var(--muted); font-size: var(--font-size-small); line-height: 1.5; text-wrap: balance; }
.web-capability-bridge::before { content: ''; position: absolute; top: 0; bottom: 0; left: 50%; border-left: 1px dashed var(--border-strong); }
.web-capability-bridge span { position: relative; display: inline-block; padding: .2rem 0; background: var(--surface); }
.web-flow-link { display: block; width: 100%; height: 32px; overflow: visible; fill: none; }
.web-flow-track { stroke: var(--border-strong); stroke-width: 1; stroke-linecap: round; stroke-linejoin: round; vector-effect: non-scaling-stroke; }
.web-flow-packet { opacity: 0; stroke: var(--accent); stroke-width: 2.5; stroke-linecap: round; stroke-dasharray: 10 110; stroke-dashoffset: 10; vector-effect: non-scaling-stroke; }
@media (prefers-reduced-motion: no-preference) {
  .web-flow-packet { animation: web-query-transfer 6s linear infinite; }
  .web-flow-content-link .web-flow-packet { animation-delay: 1.2s; }
  .web-flow-content-link .web-flow-return { animation-delay: 3s; }
  .web-flow-return { animation-delay: 4.2s; }
}
@keyframes web-query-transfer {
  0% { opacity: 0; stroke-dashoffset: 10; }
  2% { opacity: .8; }
  20% { opacity: .8; stroke-dashoffset: -100; }
  22%, 100% { opacity: 0; stroke-dashoffset: -100; }
}
.web-capability-details { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; margin-top: 1.25rem; }
.web-capability-details h3 { margin: 0 0 .4rem; font-size: 1rem; }
.web-capability-details p { margin: 0; color: var(--text-secondary); line-height: 1.7; }
@media (max-width: 520px) {
  .web-capabilities { padding: .75rem; }
  .web-flow-actor h3 { font-size: var(--font-size-small); }
  .web-capability-details { grid-template-columns: minmax(0, 1fr); }
}
.web-preview-section { display: flex; flex-direction: column; gap: 1.25rem; min-width: 0; padding-top: 1.5rem; border-top: 1px solid var(--border); }
.web-preview-section>h2, .search-credentials>h2 { margin: 0; font-size: 1rem; font-weight: 600; }
.web-preview-tradeoff { margin: 0; padding: 1rem; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-subtle); }
.web-preview-legend { display: flex; flex-wrap: wrap; gap: .5rem 1.25rem; margin-bottom: .75rem; }
.web-preview-legend>span { display: inline-flex; align-items: center; gap: .5rem; }
.web-preview-legend i { flex: none; width: 1.25rem; border-top: 2px solid currentColor; }
.web-preview-legend .preview-cost i { border-top-style: dashed; }
.preview-risk { color: var(--accent); stroke: var(--accent); }
.preview-cost { color: var(--warning); stroke: var(--warning); }
.web-preview-chart { display: flex; gap: .75rem; }
.web-preview-levels { display: flex; flex-direction: column; justify-content: space-between; padding: 0 0 .1rem; color: var(--muted); }
.web-preview-chart svg { flex: 1; min-width: 0; height: 88px; overflow: visible; }
.web-preview-chart path { fill: none; stroke-width: 2.5; vector-effect: non-scaling-stroke; }
.web-preview-chart .preview-cost { stroke-dasharray: 6 5; }
.web-preview-chart .preview-guide { stroke: var(--border); stroke-width: 1; }
.web-preview-tradeoff figcaption { display: flex; justify-content: space-between; gap: .5rem; margin: .5rem 0 0 2.75rem; color: var(--muted); }
.search-key-form, .search-config-form, .search-credentials { display: flex; flex-direction: column; gap: .65rem; min-width: 0; }
.search-provider-actions, .search-key-actions, .search-key-actions>div { display: flex; align-items: center; flex-wrap: wrap; gap: .5rem; }
.search-key-actions { margin-top: .5rem; }
.search-key-actions>div { margin-left: auto; }
</style>
