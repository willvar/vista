<script setup lang="ts">
import { computed } from 'vue'
import Modal from './Modal.vue'
import Tabs from './Tabs.vue'
import type { ModalProps, TabItem } from './types'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<ModalProps & { activeKey: string; items: TabItem[] }>(), { width: 760, closable: true, keyboard: true, maskClosable: true, zIndex: 1000 })
const emit = defineEmits<{ 'update:open': [open: boolean]; 'update:activeKey': [key: string]; cancel: []; ok: [] }>()
const modalProps = computed(() => {
  const { activeKey, items, ...modal } = props
  return modal
})
</script>

<template>
  <Modal v-bind="{ ...$attrs, ...modalProps }" class="ui-tabbed-modal" @update:open="emit('update:open', $event)" @cancel="emit('cancel')" @ok="emit('ok')">
    <template v-if="$slots['header-actions']" #header-actions><slot name="header-actions" /></template>
    <Tabs :active-key="activeKey" :items="items" :label="title" orientation="vertical" preserve-content @update:active-key="emit('update:activeKey', $event)">
      <template v-for="item in items" :key="item.value" #[item.value]><slot :name="item.value" /></template>
    </Tabs>
    <template v-if="$slots.footer" #footer><slot name="footer" /></template>
  </Modal>
</template>

<style>
.ui-tabbed-modal .ui-modal-container { display: flex; flex-direction: column; height: min(600px, calc(100dvh - 2rem)); overflow: hidden; }
.ui-tabbed-modal .ui-modal-body, .ui-tabbed-modal .ui-tabs { display: flex; flex: 1; min-width: 0; min-height: 0; overflow: hidden; }
.ui-tabbed-modal .ui-tabs { gap: 20px; }
.ui-tabbed-modal .ui-tabs-nav { flex: 0 0 160px; flex-direction: column; align-items: stretch; gap: 4px; margin: 0; padding: 4px 12px 4px 0; border-bottom: 0; border-right: 1px solid var(--border-subtle); overflow-y: auto; overscroll-behavior: contain; }
.ui-tabbed-modal .ui-tabs-tab { padding: 10px 12px; border-radius: 8px; text-align: start; line-height: 1.5; overflow-wrap: anywhere; }
.ui-tabbed-modal .ui-tabs-tab[data-selected], .ui-tabbed-modal .ui-tabs-tab:not(:disabled):hover { color: var(--accent); background: var(--selected); }
.ui-tabbed-modal .ui-tabs-indicator { display: none; }
.ui-tabbed-modal .ui-tabs-panel:not([hidden]) { flex: 1; min-width: 0; min-height: 0; padding-top: 4px; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.ui-tabbed-modal .ui-modal-footer { flex: none; padding-top: 16px; border-top: 1px solid var(--border-subtle); }
.ui-tabbed-modal .ui-tabs-error { margin-inline-start: 6px; color: var(--danger); }
@media (max-width: 600px) {
  .ui-tabbed-modal .ui-modal-container { padding: 16px; }
  .ui-tabbed-modal .ui-tabs { gap: 12px; }
  .ui-tabbed-modal .ui-tabs-nav { flex-basis: 120px; padding-inline-end: 8px; }
  .ui-tabbed-modal .ui-tabs-tab { min-height: 44px; padding: 8px; font-size: var(--font-size-small); }
}
</style>
