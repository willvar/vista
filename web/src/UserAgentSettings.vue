<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RuleEffect, type JsonSchema, type UISchemaElement } from '@jsonforms/core'
import { Alert as VAlert, Button as VButton } from './ui'
import SchemaForm from './SchemaForm.vue'
import type { VistaClient } from './client/vista'
import type { UserAgentSettings } from './client/types'
const props = defineProps<{ client: VistaClient }>()
const { t } = useI18n({ useScope: 'global' })
const settings = ref<UserAgentSettings>()
const preset = ref('vista'), custom = ref('')
const loading = ref(true), saving = ref(false), error = ref(''), editorValid = ref(false), editorEpoch = ref(0)
const controller = new AbortController()
onUnmounted(() => controller.abort())
const preview = computed(() => preset.value === 'custom' ? custom.value.trim() : settings.value?.presets.find(item => item.id === preset.value)?.value || '')
const dirty = computed(() => !!settings.value && (preset.value !== settings.value.preset || custom.value.trim() !== settings.value.custom))
const schema = computed<JsonSchema>(() => ({ type: 'object', additionalProperties: false, required: ['preset', 'custom'], properties: {
  preset: { type: 'string', title: t('userAgentPreset'), enum: [...settings.value?.presets.map(item => item.id) ?? [], 'custom'] },
  custom: { type: 'string', title: t('userAgentCustomValue'), maxLength: settings.value?.maxLength ?? 512, pattern: '^[\\x20-\\x7e]*$' },
}, if: { properties: { preset: { const: 'custom' } }, required: ['preset'] }, then: { properties: { custom: { minLength: 1, pattern: '[^ ]' } } } }))
const uischema = computed<UISchemaElement>(() => ({ type: 'VerticalLayout', elements: [
  { type: 'Control', scope: '#/properties/preset', options: { enumLabels: Object.fromEntries([...(settings.value?.presets.map(item => [item.id, item.name]) ?? []), ['custom', t('userAgentCustom')]]) } },
  { type: 'Control', scope: '#/properties/custom', rule: { effect: RuleEffect.SHOW, condition: { scope: '#/properties/preset', schema: { const: 'custom' } } } },
] } as UISchemaElement))
const document = computed<Record<string, unknown>>({ get: () => ({ preset: preset.value, custom: custom.value }), set: value => { preset.value = value.preset as string; custom.value = value.custom as string } })
function receive(value: UserAgentSettings) { settings.value = value; preset.value = value.preset; custom.value = value.custom; editorEpoch.value++ }
async function load() {
  loading.value = true; error.value = ''
  try { const value = await props.client.request<UserAgentSettings>('/user-agent', { signal: controller.signal }); if (!controller.signal.aborted) receive(value) }
  catch (cause) { if (!controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause) }
  finally { loading.value = false }
}
async function save() {
  if (!dirty.value || !editorValid.value || saving.value) return
  saving.value = true; error.value = ''
  try {
    const value = await props.client.request<UserAgentSettings>('/user-agent', { method: 'PUT', body: JSON.stringify({ preset: preset.value, custom: custom.value.trim() }), signal: controller.signal })
    if (!controller.signal.aborted) receive(value)
  } catch (cause) { if (!controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause) }
  finally { saving.value = false }
}
onMounted(load)
</script>
<template>
  <section class="user-agent-setting" aria-labelledby="user-agent-heading" :aria-busy="loading || saving">
    <strong id="user-agent-heading">User-Agent</strong>
    <p>{{ t('userAgentHint') }}</p>
    <p v-if="loading" role="status">{{ t('userAgentLoading') }}</p>
    <form v-else-if="settings" @submit.prevent="save">
      <SchemaForm id="user-agent-config" v-model="document" :schema="schema" :uischema="uischema" :disabled="saving" :reset-key="editorEpoch" @validity="editorValid = $event" />
      <div class="user-agent-preview"><span>{{ t('userAgentPreview') }}</span><code aria-live="polite">User-Agent: {{ preview }}</code></div>
      <v-button class="user-agent-save" type="submit" :loading="saving" :disabled="!dirty || !editorValid">{{ t('saveChanges') }}</v-button>
    </form>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error && !settings && !loading" @click="load">{{ t('retry') }}</v-button>
  </section>
</template>
<style scoped>
.user-agent-setting { padding: 1rem 0; border-bottom: 1px solid var(--border); }
.user-agent-setting p { margin: .5rem 0 .75rem; color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; }
.user-agent-setting form { display: flex; flex-direction: column; gap: .5rem; }
.user-agent-preview { display: flex; flex-direction: column; gap: .5rem; margin: .5rem 0; }
.user-agent-preview > span { color: var(--muted); font-size: var(--font-size-small); }
.user-agent-preview code { display: block; min-width: 0; padding: .75rem; border: 1px solid var(--border); border-radius: .5rem; color: var(--text); background: var(--surface); overflow-wrap: anywhere; white-space: pre-wrap; font: var(--font-size-small)/1.6 var(--font-mono, monospace); }
.user-agent-save { align-self: flex-start; }
.user-agent-setting .ui-alert { margin-top: .75rem; }
.user-agent-setting > .ui-btn { margin-top: .5rem; }
</style>
