<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { VistaClient } from './client/vista'
import { Button, Modal } from './ui'

const props = defineProps<{ client: VistaClient }>()
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const labels = {
  verified: 'environmentCheckVerified',
  no_listener: 'environmentCheckNoListener',
  no_address: 'environmentCheckNoAddress',
  no_callback: 'environmentCheckNoCallback',
  probe_unavailable: 'environmentCheckProbeUnavailable',
} as const

type Report = { port: number; results: { family: 'IPv4' | 'IPv6'; status: keyof typeof labels }[] }
const open = ref(false), busy = ref(false), failed = ref(false)
const report = ref<Report>()
let pending: AbortController | undefined
const verified = computed(() => report.value?.results.filter(row => row.status === 'verified') ?? [])
const title = computed(() => busy.value ? 'environmentCheck'
  : verified.value.length ? 'environmentCheckReady'
  : report.value?.port === 0 ? 'environmentCheckUnavailable' : 'environmentCheckUnconfirmed')
watch(open, value => emit('modal-open', value), { flush: 'sync' })

function close() {
  pending?.abort()
  pending = undefined
  busy.value = false
  open.value = false
}
onBeforeUnmount(close)

async function run() {
  if (pending) return
  const request = pending = new AbortController()
  report.value = undefined
  failed.value = false
  busy.value = true
  open.value = true
  try {
    const result = await props.client.request<Report>('/environment-check', { method: 'POST', signal: request.signal })
    if (pending === request) report.value = result
  } catch {
    if (pending === request) failed.value = true
  } finally {
    if (pending === request) {
      pending = undefined
      busy.value = false
    }
  }
}
</script>

<template>
  <div class="general-setting environment-check-setting">
    <p id="environment-check-hint">{{ t('environmentCheckHint') }}</p>
    <Button :disabled="busy" aria-describedby="environment-check-hint" @click="run">{{ t('environmentCheck') }}</Button>
  </div>
  <Modal :open="open" :title="t(title)" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))" :footer="null" @cancel="close">
    <div :aria-busy="busy">
      <p v-if="busy" role="status">{{ t('environmentCheckRunning') }}</p>
      <p v-else-if="failed" role="status">{{ t('environmentCheckRequestFailed') }}</p>
      <template v-else-if="report">
        <p v-if="report.port">{{ t('environmentCheckPort', { port: report.port }) }}</p>
        <ul v-if="report.port">
          <li v-for="row in report.results" :key="row.family">{{ row.family }} — {{ t(labels[row.status]) }}</li>
        </ul>
        <p v-else>{{ t('environmentCheckNoListener') }}</p>
        <p v-if="verified.length === 1 && verified[0]?.family === 'IPv6'">{{ t('environmentCheckIPv6Only') }}</p>
      </template>
    </div>
    <div class="actions"><Button @click="close">{{ t(busy ? 'cancel' : 'close') }}</Button></div>
  </Modal>
</template>

<style scoped>
.about-settings-content > .environment-check-setting { padding: 0 0 12px; border-bottom: 0; }
</style>
