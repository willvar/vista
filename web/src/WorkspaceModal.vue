<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { Modal, type ModalProps } from 'antdv-next'

const props = withDefaults(defineProps<{ open: boolean; keyboard?: boolean; width: NonNullable<ModalProps['width']> }>(), { keyboard: true })
const emit = defineEmits<{ cancel: [] }>()
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
  <Modal :open="visible" centered :width="width" :footer="null" :closable="false" :keyboard="keyboard" :after-close="afterClose"
    :styles="styles" class="workspace-modal" wrap-class-name="workspace-modal-wrap" @cancel="emit('cancel')"><slot /></Modal>
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
.workspace-modal .page-back { display: none; }
@media (max-width: 830px) {
  .workspace-modal-wrap { overflow: hidden; }
  .workspace-modal-wrap::before { display: none; }
  .workspace-modal.ant-modal {
    --workspace-modal-border: 0;
    width: 100% !important;
    max-width: 100%;
    top: 0;
    margin: 0;
    padding: 0;
    vertical-align: top;
  }
  .workspace-modal .ant-modal-container { border-radius: 0; box-shadow: none; }
  .workspace-modal .modal-page {
    height: 100dvh;
    padding: env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left);
  }
  .workspace-modal .modal-page.settings-page {
    height: 100dvh;
    max-height: none;
  }
  .workspace-modal .modal-page > .conversation-header { padding: .4rem .6rem; gap: .5rem; }
  .workspace-modal .page-back { display: inline-flex; flex: none; width: 44px; height: 44px; padding: 0; font-size:22px; }
  .workspace-modal .modal-close.ant-btn { display: none; }
  .workspace-modal .settings-header-action.ant-btn { width: 44px; height: 44px; }
  .workspace-modal .page-heading { flex: 1; min-width: 0; }
  .workspace-modal .page-heading > small { display: none; }
  .workspace-modal .settings-content, .workspace-modal .session-manager-content { overscroll-behavior: contain; }
  .workspace-modal .settings-page .settings-content { width: auto; flex: 1 1 auto; }
}
</style>
