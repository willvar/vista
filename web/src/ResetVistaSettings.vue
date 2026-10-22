<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Modal as AModal } from 'antdv-next'
import { useWorkspace } from './composables/useWorkspace'
import { clearVistaBrowserData } from './state/reset'

const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const open = ref(false)
const busy = ref(false)
const error = ref('')
watch(open, value => emit('modal-open', value), { flush: 'sync' })
onUnmounted(() => emit('modal-open', false))
function confirm() {
  error.value = ''
  open.value = true
}
function cancel() {
  if (!busy.value) open.value = false
}
async function reset() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const result = await client.request<{ reset: boolean; address: string }>('/reset', { method: 'POST', body: JSON.stringify({ confirm: 'reset-all' }) })
    clearVistaBrowserData()
    document.cookie = `vista-reset-pending=${encodeURIComponent(result.address)}; Path=/; Max-Age=300; SameSite=Strict`
    window.location.replace(result.address + '/')
  } catch (failure) {
    error.value = String(failure)
    busy.value = false
  }
}
</script>

<template>
  <div class="reset-vista-setting general-setting">
    <div><h4>{{ t('resetVista') }}</h4><p>{{ t('resetVistaHint') }}</p></div>
    <a-button danger @click="confirm">{{ t('resetVista') }}</a-button>
  </div>
  <a-modal :open="open" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))"
    class="reset-vista-modal" :title="t('resetVistaTitle')" :ok-text="t('resetVistaConfirm')" :cancel-text="t('cancel')"
    :confirm-loading="busy" :ok-button-props="{ danger: true }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="!busy"
    :styles="{ body: { maxHeight: 'calc(100dvh - 12rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }"
    @ok="reset" @cancel="cancel">
    <div class="reset-vista-confirmation" :aria-busy="busy">
      <p>{{ t('resetVistaWarning') }}</p>
      <ul><li>{{ t('resetVistaConversations') }}</li><li>{{ t('resetVistaConnections') }}</li><li>{{ t('resetVistaPersonalization') }}</li><li>{{ t('resetVistaBrowser') }}</li></ul>
      <p>{{ t('resetVistaAfter') }}</p>
      <a-alert v-if="busy" type="info" role="status" :message="t('resetVistaProgress')" />
      <a-alert v-if="error" type="error" role="alert" :message="error" />
    </div>
  </a-modal>
</template>

<style>
.about-settings-content > .reset-vista-setting.general-setting {
  flex-direction: row;
  align-items: center;
  gap: 16px;
  padding: 16px 0;
  border-top: 0;
  border-bottom: 1px solid var(--border);
}
.reset-vista-setting > div { flex: 1; min-width: 0; }
.reset-vista-setting h4 { margin: 0; font-size: var(--font-size-body); font-weight: 550; }
.reset-vista-setting > .ant-btn { flex: none; }
.reset-vista-confirmation { line-height: 1.7; overflow-wrap: anywhere; }
.reset-vista-confirmation > p { margin: 0 0 12px; }
.reset-vista-confirmation ul { margin: 0 0 16px; padding-inline-start: 1.4em; }
.reset-vista-confirmation li + li { margin-top: 6px; }
.reset-vista-confirmation .ant-alert + .ant-alert { margin-top: 12px; }
</style>
