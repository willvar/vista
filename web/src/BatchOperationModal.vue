<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Button, Modal } from './ui'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  phase: 'confirm' | 'running' | 'complete'
  total: number
  completed?: number
  progressLabel?: string
  width?: string | number
  confirmation?: string
  confirmText?: string
  confirmDisabled?: boolean
  danger?: boolean
  failures?: Array<{ id: string; title: string; message: string }>
  failureSummary?: string
  error?: string
}>(), { width: 'min(480px, calc(100vw - 2rem))', failures: () => [] })
const emit = defineEmits<{ cancel: []; confirm: [] }>()
const { t } = useI18n({ useScope: 'global' })
const running = computed(() => props.phase === 'running')
const progressWidth = computed(() => props.completed === undefined ? undefined
  : `${Math.max(0, Math.min(1, props.completed / Math.max(1, props.total))) * 100}%`)
function dismiss() {
  if (!running.value) emit('cancel')
}
</script>

<template>
  <Modal :open="open" :title="title" centered :z-index="1200" :width="width"
    class="batch-operation-modal" :footer="running ? null : undefined"
    :styles="{ container: { display: 'flex', flexDirection: 'column', maxHeight: 'calc(100dvh - 2rem)' }, body: { minHeight: 0, overflowY: 'auto', overscrollBehavior: 'contain' } }"
    :closable="!running" :keyboard="!running" :mask-closable="!running" @cancel="dismiss">
    <slot v-if="phase === 'confirm'" name="confirmation"><p class="batch-operation-confirmation">{{ confirmation }}</p></slot>
    <template v-else>
      <div class="batch-operation-progress" role="status">
        <span>{{ completed === undefined ? progressLabel || t('statusRunning') : t('batchProgress', { completed, total }) }}</span>
        <div v-if="running || completed !== undefined" class="batch-operation-progress-track" :class="{ 'is-indeterminate': completed === undefined }">
          <progress class="visually-hidden" :value="completed" :max="Math.max(1, total)" :aria-label="title"></progress>
          <span class="batch-operation-progress-fill" :style="{ width: progressWidth }" aria-hidden="true"></span>
        </div>
      </div>
      <Alert v-if="phase === 'complete' && (failures.length || error)" type="error" show-icon :message="error || failureSummary" class="batch-operation-failures">
        <template v-if="failures.length" #description><ul><li v-for="failure in failures" :key="failure.id">{{ failure.title }}: {{ failure.message }}</li></ul></template>
      </Alert>
    </template>
    <template #footer>
      <template v-if="phase === 'confirm'">
        <Button @click="dismiss">{{ t('cancel') }}</Button>
        <Button variant="primary" :danger="danger" :disabled="confirmDisabled || !total" @click="emit('confirm')">{{ confirmText }}</Button>
      </template>
      <Button v-else @click="dismiss">{{ t('close') }}</Button>
    </template>
  </Modal>
</template>

<style scoped>
.batch-operation-confirmation { margin: 0; overflow-wrap: anywhere; }
.batch-operation-progress { display: grid; gap: .5rem; color: var(--muted); }
.batch-operation-progress-track { position: relative; height: .5rem; border-radius: 999px; overflow: hidden; background: var(--border-subtle); }
.batch-operation-progress-fill { display: block; height: 100%; width: 0; border-radius: inherit; background: var(--accent); transition: width .2s ease; }
.is-indeterminate > .batch-operation-progress-fill { width: 40%; transform: translateX(75%); transition: none; animation: batch-progress-pending 1.5s ease-in-out infinite; }
@keyframes batch-progress-pending { from { transform: translateX(-100%); } to { transform: translateX(250%); } }
@media (prefers-reduced-motion: reduce) {
  .batch-operation-progress-fill { transition: none; }
  .is-indeterminate > .batch-operation-progress-fill { animation: none; }
}
.batch-operation-failures { margin-top: 1rem; overflow-wrap: anywhere; }
.batch-operation-failures ul { margin: .35rem 0 0; padding-left: 1.25rem; }
</style>
