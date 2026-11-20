<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconThinkingHigh } from '@tabler/icons-vue'
import type { VistaClient } from '@vista/client'
import { Button, Modal, ProgressBar } from './ui'

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

type Report = { results: {
  family: 'IPv4' | 'IPv6'; status: keyof typeof labels; port?: number
  attemptedPorts?: number[]
}[] }
type Update = { step?: string; report?: Report }
const steps = [
  { key: 'address', label: 'environmentCheckAddress' },
  { key: 'port', label: 'environmentCheckSelectPort' },
  { key: 'callback', label: 'environmentCheckCallback' },
] as const
const step = ref(0)
const progressLabel = computed(() => {
  const current = steps[step.value - 1]
  return current ? t('environmentCheckStep', { step: step.value, total: steps.length, phase: t(current.label) })
    : t('environmentCheckRunning')
})
const open = ref(false), busy = ref(false), failed = ref(false)
const report = ref<Report>()
let pending: AbortController | undefined
const verified = computed(() => report.value?.results.filter(row => row.status === 'verified') ?? [])
const title = computed(() => busy.value ? 'environmentCheck'
  : verified.value.length ? 'environmentCheckReady' : 'environmentCheckUnconfirmed')
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
  step.value = 0
  failed.value = false
  busy.value = true
  open.value = true
  try {
    for await (const update of props.client.requestStream<Update>('/environment-check', { method: 'POST', signal: request.signal })) {
      if (pending !== request) return
      if (update.report) {
        report.value = update.report
        return
      }
      const next = steps.findIndex(stage => stage.key === update.step) + 1
      if (next > step.value) step.value = next
    }
    if (pending === request) failed.value = true
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
  <Button variant="text" class="icon-button" :aria-label="t('environmentCheck')" :title="t('environmentCheck')" :disabled="busy" @click="run">
    <template #icon><IconThinkingHigh aria-hidden="true" /></template>
  </Button>
  <Modal :open="open" :title="t(title)" centered :z-index="1200" width="min(520px, calc(100vw - 2rem))" @cancel="close">
    <div>
      <div v-if="busy" class="environment-check-progress">
        <p role="status">{{ progressLabel }}</p>
        <ProgressBar :label="progressLabel" :value="Math.max(0, step - 1)" :max="steps.length" />
      </div>
      <p v-else-if="failed" role="status">{{ t('environmentCheckRequestFailed') }}</p>
      <template v-else-if="report">
        <ul>
          <li v-for="row in report.results" :key="row.family">
            <div>{{ row.family }} — {{ row.port ? t('environmentCheckPort', { port: row.port }) : t(labels[row.status]) }}</div>
            <template v-if="!row.port">
              <div v-if="row.attemptedPorts?.length">{{ t('environmentCheckAttemptedPorts', { ports: row.attemptedPorts.join(', ') }) }}</div>
              <div v-else>{{ t('environmentCheckNoPortsAttempted') }}</div>
            </template>
          </li>
        </ul>
        <p v-if="verified.length === 1 && verified[0]?.family === 'IPv6'">{{ t('environmentCheckIPv6Only') }}</p>
      </template>
    </div>
    <template #footer>
      <Button @click="close">{{ t(busy ? 'cancel' : 'close') }}</Button>
    </template>
  </Modal>
</template>

<style scoped>
.environment-check-progress { display: grid; gap: .5rem; }
.environment-check-progress p { margin: 0; }
</style>
