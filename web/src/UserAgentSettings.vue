<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Input as AInput, Select as ASelect } from 'antdv-next'
import type { VistaClient } from './client/vista'
import type { UserAgentSettings } from './client/types'

const props = defineProps<{ client: VistaClient }>()
const { t } = useI18n({ useScope: 'global' })
const settings = ref<UserAgentSettings>()
const preset = ref('vista')
const custom = ref('')
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const controller = new AbortController()
onUnmounted(() => controller.abort())

const options = computed(() => [
  ...settings.value?.presets.map(item => ({ value: item.id, label: item.name })) || [],
  { value: 'custom', label: t('userAgentCustom') },
])
const preview = computed(() => preset.value === 'custom' ? custom.value.trim() : settings.value?.presets.find(item => item.id === preset.value)?.value || '')
const valid = computed(() => preset.value !== 'custom' || !!custom.value.trim() && custom.value.length <= (settings.value?.maxLength || 512) && /^[\x20-\x7e]+$/.test(custom.value))
const dirty = computed(() => !!settings.value && (preset.value !== settings.value.preset || custom.value.trim() !== settings.value.custom))

function receive(value: UserAgentSettings) {
  settings.value = value
  preset.value = value.preset
  custom.value = value.custom
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const value = await props.client.request<UserAgentSettings>('/user-agent', { signal: controller.signal })
    if (!controller.signal.aborted) receive(value)
  } catch (cause) {
    if (!controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause)
  } finally { loading.value = false }
}

async function save() {
  if (!dirty.value || !valid.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const value = await props.client.request<UserAgentSettings>('/user-agent', {
      method: 'PUT', body: JSON.stringify({ preset: preset.value, custom: custom.value.trim() }), signal: controller.signal,
    })
    if (!controller.signal.aborted) receive(value)
  } catch (cause) {
    if (!controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause)
  } finally { saving.value = false }
}
onMounted(load)
</script>

<template>
  <section class="user-agent-setting" aria-labelledby="user-agent-heading" :aria-busy="loading || saving">
    <strong id="user-agent-heading">User-Agent</strong>
    <p class="user-agent-hint">{{ t('userAgentHint') }}</p>
    <p v-if="loading" role="status">{{ t('userAgentLoading') }}</p>
    <form v-else-if="settings" @submit.prevent="save">
      <label for="user-agent-preset">{{ t('userAgentPreset') }}</label>
      <a-select id="user-agent-preset" v-model:value="preset" :options="options" :disabled="saving" :aria-label="t('userAgentPreset')" />
      <template v-if="preset === 'custom'">
        <label for="user-agent-custom">{{ t('userAgentCustomValue') }}</label>
        <a-input id="user-agent-custom" v-model:value="custom" :disabled="saving" :maxlength="settings.maxLength" :status="!valid && custom ? 'error' : undefined" :aria-invalid="!valid" :aria-describedby="!valid && custom ? 'user-agent-invalid' : undefined" autocomplete="off" spellcheck="false" placeholder="MyClient/1.0" />
        <small v-if="!valid && custom" id="user-agent-invalid" class="user-agent-invalid">{{ t('userAgentInvalid', { max: settings.maxLength }) }}</small>
      </template>
      <div class="user-agent-preview">
        <span>{{ t('userAgentPreview') }}</span>
        <code aria-live="polite">User-Agent: {{ preview }}</code>
      </div>
      <a-button class="user-agent-save" html-type="submit" :loading="saving" :disabled="!dirty || !valid">{{ t('saveChanges') }}</a-button>
    </form>
    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <a-button v-if="error && !settings && !loading" @click="load">{{ t('retry') }}</a-button>
  </section>
</template>

<style scoped>
.user-agent-setting { padding: 1rem 0; border-bottom: 1px solid var(--border); }
.user-agent-setting p { margin: .5rem 0 .75rem; color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; }
.user-agent-setting form { display: flex; flex-direction: column; gap: .5rem; }
.user-agent-setting label { font-size: var(--font-size-body); }
.user-agent-setting .ant-select { width: 100%; }
.user-agent-setting .ant-input { font-family: var(--font-mono, monospace); }
.user-agent-preview { display: flex; flex-direction: column; gap: .5rem; margin: .5rem 0; }
.user-agent-preview>span { color: var(--muted); font-size: var(--font-size-small); }
.user-agent-preview code { display: block; min-width: 0; padding: .75rem; border: 1px solid var(--border); border-radius: .5rem; color: var(--text); background: var(--surface); overflow-wrap: anywhere; white-space: pre-wrap; font: var(--font-size-small)/1.6 var(--font-mono, monospace); }
.user-agent-save { align-self: flex-start; }
.user-agent-invalid { color: var(--danger); }
.user-agent-setting .ant-alert { margin-top: .75rem; }
.user-agent-setting>.ant-btn { margin-top: .5rem; }
</style>
