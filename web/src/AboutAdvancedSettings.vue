<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Modal, Switch, TabbedModal } from './ui'
import UserAgentSettings from './UserAgentSettings.vue'
import CapacitySettings from './CapacitySettings.vue'
import ServerConfigSettings from './ServerConfigSettings.vue'
import type { VistaClient, ServerStatus } from '@vista/client'

const props = defineProps<{ open: boolean; client: VistaClient; status: ServerStatus | null; debugMode: boolean }>()
const emit = defineEmits<{ cancel: []; 'update:debugMode': [enabled: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const tab = ref('user-agent'), saving = ref(false), childOpen = ref(false), discardOpen = ref(false)
const debug = ref(props.debugMode)
const userAgent = ref<InstanceType<typeof UserAgentSettings>>()
const capacity = ref<InstanceType<typeof CapacitySettings>>()
const server = ref<InstanceType<typeof ServerConfigSettings>>()
const editors = computed(() => [
  { key: 'user-agent', label: 'User-Agent', editor: userAgent.value },
  { key: 'capacity', label: t('memorySettings'), editor: capacity.value },
  { key: 'server', label: t('serverConfig'), editor: server.value },
])
const locked = computed(() => editors.value.some(({ key, editor }) => key === tab.value && editor?.dirty && !editor.valid))
const tabs = computed(() => [
  ...editors.value.map(({ key, label, editor }) => ({ value: key, label, invalid: !!editor?.dirty && !editor.valid })),
  { value: 'debug', label: t('debugMode') },
].map(item => ({ ...item, disabled: locked.value && item.value !== tab.value })))
const dirty = computed(() => debug.value !== props.debugMode || editors.value.some(({ editor }) => editor?.dirty))
const ready = computed(() => editors.value.every(({ editor }) => editor?.ready))
const busy = computed(() => saving.value || childOpen.value || discardOpen.value)
watch(() => props.open, open => { if (open) { tab.value = 'user-agent'; debug.value = props.debugMode; discardOpen.value = false } })
watch(() => props.debugMode, (value, previous) => { if (debug.value === previous) debug.value = value })
function close() {
  if (busy.value) return
  if (dirty.value) discardOpen.value = true
  else emit('cancel')
}
function discard() { discardOpen.value = false; emit('cancel') }
async function focusTab(key: string) {
  tab.value = key
  await nextTick()
  const panel = document.querySelector<HTMLElement>('.about-advanced-modal .ui-tabs-panel:not([hidden])')
  const field = panel?.querySelector<HTMLElement>('[aria-invalid="true"]') ?? panel?.querySelector<HTMLElement>('input, button')
  field?.focus()
}
async function save() {
  if (!dirty.value || !ready.value || busy.value) return
  let invalid = editors.value.find(({ editor }) => !editor?.valid)
  if (invalid) { await focusTab(invalid.key); return }
  if (server.value?.dirty && !await server.value.confirmSave()) return
  if (!props.open) return
  invalid = editors.value.find(({ editor }) => !editor?.valid)
  if (invalid) { await focusTab(invalid.key); return }
  saving.value = true
  try {
    // The port change may restart the service. Save the other drafts first.
    for (const section of editors.value.slice(0, 2)) {
      if (section.editor?.dirty && !await section.editor.save()) { await focusTab(section.key); return }
      if (!props.open) return
    }
    if (debug.value !== props.debugMode) emit('update:debugMode', debug.value)
    if (server.value?.dirty && !await server.value.save()) await focusTab('server')
  } finally { saving.value = false }
}
</script>

<template>
  <TabbedModal class="about-advanced-modal" :open="open" :title="t('advancedSettings')" :items="tabs" v-model:active-key="tab"
    :z-index="1100" destroy-on-hidden :close-disabled="busy" :keyboard="!busy" :mask-closable="!busy"
    :ok-text="t('saveChanges')" :confirm-loading="saving" :ok-button-props="{ disabled: !dirty || !ready || busy }"
    :cancel-button-props="{ disabled: busy }" @cancel="close" @ok="save">
    <template #user-agent><UserAgentSettings v-if="open" ref="userAgent" :client="client" :disabled="saving" @submit="save" /></template>
    <template #capacity><CapacitySettings v-if="open" ref="capacity" :client="client" :status="status" :disabled="saving" @submit="save" /></template>
    <template #server><ServerConfigSettings v-if="open" ref="server" :disabled="saving" @modal-open="childOpen = $event" @submit="save" /></template>
    <template #debug>
      <div class="debug-mode-setting general-setting"><div><label for="debug-mode">{{ t('debugMode') }}</label><p id="debug-mode-hint">{{ t('debugModeHint') }}</p></div><Switch id="debug-mode" v-model:checked="debug" :disabled="saving" :aria-label="t('debugMode')" aria-describedby="debug-mode-hint" /></div>
    </template>
  </TabbedModal>
  <Modal :open="discardOpen" :title="t('networkDiscard')" :footer="null" :z-index="1300" @cancel="discardOpen = false">
    <p>{{ t('configEditorDiscard') }}</p>
    <div class="actions"><Button @click="discardOpen = false">{{ t('cancel') }}</Button><Button danger @click="discard">{{ t('networkDiscard') }}</Button></div>
  </Modal>
</template>

<style>
.about-advanced-modal .user-agent-setting, .about-advanced-modal .memory-settings, .about-advanced-modal .server-config-setting { padding: 0 0 12px; border-bottom: 0; }
.about-advanced-modal .debug-mode-setting { padding: 0; border-bottom: 0; }
</style>
