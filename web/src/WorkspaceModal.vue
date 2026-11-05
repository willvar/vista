<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { Modal, ModalHeader, type ModalProps } from './ui'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  pageClass: string
  closeLabel?: string
  closeDisabled?: boolean
  backLabel: string
  keyboard?: boolean
  width: NonNullable<ModalProps['width']>
}>(), { keyboard: true })
const emit = defineEmits<{ cancel: []; back: [] }>()
const header = ref<InstanceType<typeof ModalHeader>>()
defineExpose({ focusTitle: () => header.value?.focusTitle() })
const visible = ref(props.open)
watch(() => props.open, open => { visible.value = open })
let closing: Promise<void> | undefined
let finishClose: (() => void) | undefined
function afterClose() {
  finishClose?.()
  finishClose = undefined
  closing = undefined
}
// A routed modal must stay mounted until the library finishes its leave motion.
// Switching settings tabs reuses the same overlay and keeps the modal open.
onBeforeRouteLeave((to, from) => {
  if (to.matched.at(-1)?.components?.overlay === from.matched.at(-1)?.components?.overlay) {
    visible.value = props.open
    afterClose()
    return
  }
  if (!visible.value && !closing) return
  if (!closing) closing = new Promise<void>(resolve => { finishClose = resolve })
  visible.value = false
  return closing
})
onUnmounted(afterClose)
// The library's container owns the outer padding, not its body.
const styles = {
  container: { padding: 0, overflow: 'hidden', border: 'var(--workspace-modal-border, 1px solid var(--border))', background: 'var(--surface)', boxShadow: 'var(--modal-shadow)' },
  body: { padding: 0 },
  mask: { backgroundColor: 'var(--scrim)', backdropFilter: 'blur(2px)' },
} satisfies ModalProps['styles']
</script>

<template>
  <Modal :open="visible" centered :width="width" :footer="null" :keyboard="keyboard" :after-close="afterClose"
    :styles="styles" class="workspace-modal" wrap-class-name="workspace-modal-wrap" @cancel="emit('cancel')">
    <template #header />
    <section class="workspace modal-page" :class="pageClass">
      <ModalHeader ref="header" class="workspace-modal-header" :title="title" :close-label="closeLabel" :close-disabled="closeDisabled" :back-label="backLabel" @back="emit('back')">
        <template v-if="$slots['header-actions']" #actions><slot name="header-actions" /></template>
      </ModalHeader>
      <slot />
    </section>
  </Modal>
</template>

<style>
.workspace-modal .modal-page {
  width: 100%;
  height: min(82dvh, 960px, calc(100dvh - 2rem));
  min-height: 0;
  overflow: hidden;
  background: var(--surface);
}
.workspace-modal .modal-page.settings-page {
  height: min(720px, calc(100dvh - 2rem));
  max-height: calc(100dvh - 2rem);
}
.workspace-modal .settings-page .settings-content {
  /* Preserve the content width of the original 800px frame and 172px sidebar. */
  width: 626px;
  flex: 0 0 626px;
}
.workspace-modal .workspace-modal-header { min-height: 72px; margin: 0; padding: 20px 24px; border-bottom: 1px solid var(--border-subtle); }
.workspace-modal .workspace-modal-header > .ui-modal-back { display: none; }
@media (max-width: 830px) {
  .workspace-modal-wrap { overflow: hidden; }
  .workspace-modal-wrap::before { display: none; }
  .workspace-modal.ui-modal {
    --workspace-modal-border: 0;
    width: 100% !important;
    max-width: 100%;
    top: 0;
    margin: 0;
    padding: 0;
    vertical-align: top;
  }
  .workspace-modal .ui-modal-container { border-radius: 0; box-shadow: none; }
  .workspace-modal .modal-page {
    height: 100dvh;
    padding: env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left);
  }
  .workspace-modal .modal-page.settings-page {
    height: 100dvh;
    max-height: none;
  }
  .workspace-modal .workspace-modal-header { padding: .4rem .6rem; gap: .5rem; }
  .workspace-modal .workspace-modal-header > .ui-modal-back { display: inline-flex; }
  .workspace-modal .workspace-modal-header > .ui-modal-header-actions > .ui-modal-close { display: none; }
  .workspace-modal .settings-content, .workspace-modal .session-manager-content { overscroll-behavior: contain; }
  .workspace-modal .settings-page .settings-content { width: auto; flex: 1 1 auto; }
}
@media (max-height: 520px) {
  .workspace-modal .workspace-modal-header { min-height: 0; }
}
</style>
