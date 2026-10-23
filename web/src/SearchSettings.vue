<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Checkbox as ACheckbox, InputNumber as AInputNumber, InputPassword as AInputPassword, Modal as AModal } from 'antdv-next'
import { ArrowDownOutlined, ArrowUpOutlined, EditOutlined, SearchOutlined, FileTextOutlined, RobotOutlined, GlobalOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import ConfirmPopover from './ConfirmPopover.vue'

type SearchProvider = { name: string; source: 'settings' | 'environment' | 'none' }
const names: Record<string, string> = { exa: 'Exa', firecrawl: 'Firecrawl', parallel: 'Parallel', tavily: 'Tavily', deepseek: 'DeepSeek', 'deepseek-fetch': 'DeepSeek' }
const jinaName = 'Jina Reader'
type OrderKind = 'order' | 'fetchOrder'
type PreviewKind = 'summaryLength' | 'fetchPreviewLength'
const orderSections: { key: OrderKind; title: string; hint: string; preview: PreviewKind; previewTitle: string; previewHint: string; defaultLength: number }[] = [
  { key: 'order', title: 'searchOrder', hint: 'searchOrderHint', preview: 'summaryLength', previewTitle: 'searchPreviewLength', previewHint: 'searchPreviewHint', defaultLength: 1000 },
  { key: 'fetchOrder', title: 'fetchOrder', hint: 'fetchOrderHint', preview: 'fetchPreviewLength', previewTitle: 'fetchPreviewLength', previewHint: 'fetchPreviewHint', defaultLength: 8000 },
]
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const providers = ref<SearchProvider[]>([])
const jinaReader = ref<SearchProvider>({ name: 'jina', source: 'none' })
const deepseekFetch = ref<SearchProvider>({ name: 'deepseek', source: 'none' })
const previews = reactive<Record<PreviewKind, { length: number | null; unlimited: boolean; saved: number }>>({
  summaryLength: { length: 1000, unlimited: false, saved: 1000 },
  fetchPreviewLength: { length: 8000, unlimited: false, saved: 8000 },
})
function previewValue(key: PreviewKind) { return previews[key].unlimited ? 0 : previews[key].length }
function previewDirty(key: PreviewKind) { return previewValue(key) !== previews[key].saved }
function validPreview(key: PreviewKind) { return previews[key].unlimited || Number.isSafeInteger(previews[key].length) && previews[key].length! > 0 }
const orders = reactive<Record<OrderKind, string[]>>({ order: [], fetchOrder: [] })
const savedOrders = reactive<Record<OrderKind, string[]>>({ order: [], fetchOrder: [] })
function orderDirty(key: OrderKind) { return orders[key].join(',') !== savedOrders[key].join(',') }
const editing = ref('')
const keyDraft = ref('')
const keyError = ref('')
const removingKey = ref(false)
const removeKeyError = ref('')
const removalName = computed(() => editing.value.startsWith('deepseek')
  ? `${providerName(editing.value)} · ${t(editing.value === 'deepseek-fetch' ? 'webFetchLabel' : 'webSearchLabel')}`
  : providerName(editing.value))
const editingSource = computed(() => keySource(editing.value))
const canSaveKey = computed(() => {
  const key = keyDraft.value.trim()
  return !!key && new TextEncoder().encode(key).length <= 4096 && !/[\s\p{Cc}]/u.test(key)
})
const loading = ref(true)
const busy = ref('')
const error = ref('')
const updated = ref('')
const controller = new AbortController()
watch(editing, value => emit('modal-open', !!value), { flush: 'sync' })
onUnmounted(() => { controller.abort(); emit('modal-open', false) })

async function refresh() {
  const result = await client.request<{ providers: SearchProvider[]; order: string[]; fetchOrder: string[]; jinaReader: SearchProvider; deepseekFetch: SearchProvider } & Record<PreviewKind, number>>('/search-config', { signal: controller.signal })
  if (!controller.signal.aborted) {
    // Saving a key or retrying a read must preserve unsaved ordering edits.
    for (const { key, preview, defaultLength } of orderSections) {
      if (!orderDirty(key)) orders[key] = [...result[key]]
      savedOrders[key] = result[key]
      if (!previewDirty(preview)) {
        previews[preview].unlimited = result[preview] === 0
        previews[preview].length = result[preview] || defaultLength
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
  return name === 'vista' ? t('vistaReader') : name === 'jina' ? jinaName : names[name] || name
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
function editKey(name: string) {
  if (loading.value || busy.value || name === 'vista') return
  editing.value = name
  keyDraft.value = ''
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
function move(key: OrderKind, index: number, direction: number) {
  const target = index + direction
  if (loading.value || busy.value || target < 0 || target >= orders[key].length) return
  const next = [...orders[key]]
  ;[next[index], next[target]] = [next[target]!, next[index]!]
  orders[key] = next
  updated.value = ''
}
async function saveOrder(key: OrderKind) {
  if (busy.value || loading.value || !orderDirty(key)) return
  busy.value = key
  error.value = ''
  updated.value = ''
  const next = [...orders[key]]
  try {
    await client.request('/search-config', {
      method: 'PATCH', signal: controller.signal, body: JSON.stringify({ [key]: next }),
    })
    if (!controller.signal.aborted) {
      savedOrders[key] = next
      updated.value = key
    }
  } catch (cause) { if (!controller.signal.aborted) error.value = String(cause) }
  finally { busy.value = '' }
}

async function savePreview(key: PreviewKind) {
  if (busy.value || loading.value || !previewDirty(key) || !validPreview(key)) return
  busy.value = key
  error.value = ''
  updated.value = ''
  const next = previewValue(key)!
  try {
    await client.request('/search-config', { method: 'PATCH', signal: controller.signal, body: JSON.stringify({ [key]: next }) })
    if (!controller.signal.aborted) {
      previews[key].saved = next
      updated.value = key
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

    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <a-button v-if="error" :disabled="loading || !!busy" @click="load">{{ t('retry') }}</a-button>
    <p v-if="loading" role="status">{{ t('loadingSearchSettings') }}</p>
    <div class="web-order-sections">
    <template v-for="section in orderSections" :key="section.key">
    <form v-if="orders[section.key].length" class="web-order-form" :class="section.key === 'order' ? 'search-order-form' : 'fetch-order-form'" :aria-label="t(section.title)" @submit.prevent="saveOrder(section.key)">
      <h2>{{ t(section.title) }}</h2>
      <p class="search-settings-hint">{{ t(section.hint) }}</p>
      <ol :aria-label="t(section.title)">
        <li v-for="(name, index) in orders[section.key]" :key="name" :data-provider="name">
          <span class="search-order-number" aria-hidden="true">{{ index + 1 }}</span>
          <span class="search-order-name">
            <span class="search-provider-name">{{ providerName(name) }}</span>
            <span v-if="name !== 'vista'" class="search-quota-tag" :class="{ 'has-key': keySource(credentialName(name, section.key)) !== 'none' }" :title="keyStatus(keySource(credentialName(name, section.key)), name)">{{ t(keySource(credentialName(name, section.key)) !== 'none' ? 'searchPaidQuota' : name === 'deepseek' ? 'searchKeyUnconfigured' : 'searchFreeQuota') }}</span>
          </span>
          <div class="search-order-actions">
            <a-button v-if="name !== 'vista'" html-type="button" type="text" :aria-label="t('editSearchKey', { name: providerName(name) })" :title="t('editSearchKey', { name: providerName(name) })" :disabled="loading || !!busy" @click="editKey(credentialName(name, section.key))"><EditOutlined /></a-button>
            <a-button html-type="button" type="text" :aria-label="t('searchMoveUp', { name: providerName(name) })" :title="t('searchMoveUp', { name: providerName(name) })" :disabled="loading || !!busy || index === 0" @click="move(section.key, index, -1)"><ArrowUpOutlined /></a-button>
            <a-button html-type="button" type="text" :aria-label="t('searchMoveDown', { name: providerName(name) })" :title="t('searchMoveDown', { name: providerName(name) })" :disabled="loading || !!busy || index === orders[section.key].length - 1" @click="move(section.key, index, 1)"><ArrowDownOutlined /></a-button>
          </div>
        </li>
      </ol>
      <div class="search-provider-actions"><a-button html-type="submit" type="primary" :loading="busy === section.key" :disabled="loading || !!busy || !orderDirty(section.key)">{{ t('saveSearchOrder') }}</a-button></div>
      <p v-if="updated === section.key || orders[section.key].some(name => credentialName(name, section.key) === updated)" class="search-settings-updated" role="status">{{ t('searchSettingsUpdated') }}</p>
    </form>

    </template>
    </div>
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
    <div class="web-preview-forms">
    <form v-for="section in orderSections" :key="section.preview" class="web-preview-form" :aria-label="t(section.previewTitle)" @submit.prevent="savePreview(section.preview)">
      <label :for="section.preview">{{ t(section.previewTitle) }}</label>
      <p :id="`${section.preview}-hint`" class="search-settings-hint">{{ t(section.previewHint) }}</p>
      <div class="web-preview-controls">
        <a-input-number :id="section.preview" v-model:value="previews[section.preview].length" :aria-label="t(section.previewTitle)" :aria-describedby="`${section.preview}-hint web-preview-hint`" :min="1" :precision="0" :disabled="loading || !!busy || previews[section.preview].unlimited" />
        <span>{{ t('webPreviewCharacters') }}</span>
        <a-checkbox v-model:checked="previews[section.preview].unlimited" :disabled="loading || !!busy">{{ t('webPreviewUnlimited') }}</a-checkbox>
      </div>
      <div class="search-provider-actions"><a-button html-type="submit" type="primary" :loading="busy === section.preview" :disabled="loading || !!busy || !previewDirty(section.preview) || !validPreview(section.preview)">{{ t('saveChanges') }}</a-button></div>
      <p v-if="updated === section.preview" class="search-settings-updated" role="status">{{ t('searchSettingsUpdated') }}</p>
    </form>
    </div>
    </section>
  </div>
  <a-modal :open="!!editing" centered :z-index="1200" width="min(480px, calc(100vw - 2rem))"
    :title="t('editSearchKey', { name: providerName(editing) })" :footer="null" :closable="!busy" :keyboard="!busy && !removingKey" :mask-closable="!busy && !removingKey" destroy-on-hidden
    :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeKeyEditor">
    <form v-if="editing" class="search-key-form" :aria-label="providerName(editing)" @submit.prevent="updateKey()">
      <a-alert v-if="keyError" type="error" role="alert" :message="keyError" />
      <p class="search-settings-hint">{{ keyStatus(editingSource, editing) }}</p>
      <p v-if="editing === 'jina'" class="search-settings-hint">{{ t('jinaReaderHint') }}</p>
      <p v-if="editing === 'deepseek' || editing === 'deepseek-fetch'" class="search-settings-hint">{{ t(editing === 'deepseek' ? 'deepseekSearchKeyHint' : 'deepseekFetchKeyHint') }}</p>
      <label for="search-provider-key">{{ t('apiKey') }}</label>
      <a-input-password id="search-provider-key" v-model:value="keyDraft" :aria-label="`${providerName(editing)} API Key`" :maxlength="4096" :disabled="!!busy" autocomplete="new-password" :placeholder="editingSource === 'settings' ? t('reuseSavedKey') : t('enterApiKey')" />
      <p v-if="editingSource === 'environment'" class="search-settings-hint">{{ t('searchKeyEnvironmentHint') }}</p>
      <div class="search-key-actions">
        <ConfirmPopover v-if="editingSource === 'settings'" :show="removingKey" :message="t('removeSearchKeyConfirm', { name: removalName })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="!!busy" :error="removeKeyError" :z-index="1300" popover-class="search-key-remove-popover" @update:show="removingKey = $event; removeKeyError = ''" @confirm="updateKey(true)">
          <template #trigger><a-button html-type="button" danger :disabled="!!busy">{{ t('removeSearchKey') }}</a-button></template>
        </ConfirmPopover>
        <div><a-button html-type="button" :disabled="!!busy" @click="closeKeyEditor">{{ t('cancel') }}</a-button><a-button html-type="submit" type="primary" :loading="!!busy" :disabled="!!busy || !canSaveKey">{{ t('saveChanges') }}</a-button></div>
      </div>
    </form>
  </a-modal>
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
.web-order-sections, .web-preview-forms { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr)); gap: 1.5rem; }
.web-preview-section { display: flex; flex-direction: column; gap: 1.25rem; min-width: 0; padding-top: 1.5rem; border-top: 1px solid var(--border); }
.web-preview-section>h2 { margin: 0; font-size: 1rem; font-weight: 600; }
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
.search-key-form, .web-order-form, .web-preview-form { display: flex; flex-direction: column; gap: .65rem; min-width: 0; }
.web-preview-form>label { font-weight: 600; }
.web-preview-controls { display: flex; align-items: center; flex-wrap: wrap; gap: .5rem; }
.web-preview-controls .ant-input-number { width: 8rem; max-width: 100%; }
.web-order-form h2 { margin: 0; font-size: 1rem; font-weight: 600; }
.web-order-form ol { list-style: none; padding: 0; margin: 0; }
.web-order-form li { display: flex; align-items: center; gap: .65rem; padding: .4rem 0; border-bottom: 1px solid var(--border); }
.search-order-number { color: var(--muted); min-width: 1rem; text-align: center; }
.search-order-name { display: flex; align-items: center; flex-wrap: wrap; gap: .35rem .5rem; flex: 1; min-width: 0; overflow-wrap: anywhere; }
.search-quota-tag { flex-shrink: 0; border: 1px solid var(--border); border-radius: 4px; padding: 0 .4rem; color: var(--muted); background: var(--surface-subtle); font-size: var(--font-size-small); line-height: 1.7; }
.search-quota-tag.has-key { color: var(--accent); background: var(--accent-soft); }
.search-order-actions { display: flex; flex-shrink: 0; gap: .25rem; }
.search-order-actions .ant-btn { min-width: 36px; min-height: 36px; }
.search-provider-actions, .search-key-actions, .search-key-actions>div { display: flex; align-items: center; flex-wrap: wrap; gap: .5rem; }
.search-key-actions { margin-top: .5rem; }
.search-key-actions>div { margin-left: auto; }
</style>
