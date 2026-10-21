<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { Alert, Popconfirm } from 'antdv-next'
const props = defineProps<{ show: boolean; message: string; cancelText: string; confirmText: string; busy?: boolean; error?: string; popoverClass: string }>()
const emit = defineEmits<{ 'update:show': [show: boolean]; confirm: [] }>()
const confirming = ref(false)
function confirm() { confirming.value = true; emit('confirm') }
function changeOpen(show: boolean) { if (show || !confirming.value) emit('update:show', show) }
watch(() => props.busy, busy => { if (!busy) confirming.value = false })
function close() { emit('update:show', false) }
function onEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  event.preventDefault()
  event.stopPropagation()
  close()
}
watch(() => props.show, show => {
  window.removeEventListener('keydown', onEscape, true)
  if (!show) confirming.value = false
  if (show) window.addEventListener('keydown', onEscape, true)
})
onUnmounted(() => window.removeEventListener('keydown', onEscape, true))
</script>
<template>
  <Popconfirm :open="show" placement="top" :z-index="1200"
    :root-class="['confirmation-popover', popoverClass].join(' ')"
    :styles="{ root: { maxWidth: 'min(360px, calc(100vw - 2rem))' } }"
    :title="confirmText" :cancel-text="cancelText" :ok-text="confirmText"
    :ok-button-props="{ danger: true, loading: busy, disabled: busy }"
    @confirm="confirm" @open-change="changeOpen">
    <template #description><section class="confirmation-content" role="alertdialog" aria-modal="false" :aria-label="message">
      <p class="confirmation-message">{{ message }}</p>
      <Alert v-if="error" type="error" :message="error" show-icon />
    </section></template>
    <slot name="trigger" />
  </Popconfirm>
</template>
