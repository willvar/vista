<script setup lang="ts">
import { Dialog } from '@ark-ui/vue/dialog'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CloseOutlined } from '@antdv-next/icons'
import Button from './Button.vue'
import type { ModalProps } from './types'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<ModalProps>(), { closable: true, keyboard: true, maskClosable: true, width: 520, zIndex: 1000 })
const emit = defineEmits<{ 'update:open': [open: boolean]; cancel: []; ok: [] }>()
const { t } = useI18n()
const content = ref<{ $el: HTMLElement }>()
function initialFocus() {
  if (props.initialFocusEl) return props.initialFocusEl()
  const element = content.value?.$el
  const active = element?.ownerDocument.activeElement
  // Opening focus can run after the user has already started editing.
  if (active instanceof HTMLElement && element?.contains(active)) return active
  return element || null
}
function close() { emit('update:open', false); emit('cancel') }
function exitComplete() { props.afterClose?.() }
</script>

<template>
  <Dialog.Root :open="!!open" :close-on-escape="keyboard" :close-on-interact-outside="maskClosable" lazy-mount :unmount-on-exit="destroyOnHidden"
    :initial-focus-el="initialFocus" @open-change="!$event.open && close()" @exit-complete="exitComplete">
    <Teleport to="body">
      <Dialog.Backdrop class="ui-modal-mask" :class="rootClass" :style="[{ zIndex }, styles?.mask]" />
      <Dialog.Positioner class="ui-modal-wrap" :class="[wrapClassName, rootClass]" :style="{ zIndex }">
        <div v-bind="$attrs" class="ui-modal" :style="[{ width: typeof width === 'number' ? `${width}px` : width }, $attrs.style]">
          <Dialog.Content ref="content" class="ui-modal-container" :style="styles?.container">
            <div v-if="title || $slots.title" class="ui-modal-header"><Dialog.Title class="ui-modal-title"><slot name="title">{{ title }}</slot></Dialog.Title></div>
            <Dialog.Title v-else class="visually-hidden">Vista</Dialog.Title>
            <Dialog.CloseTrigger v-if="closable" as-child><Button variant="text" class="icon-button ui-modal-close" :aria-label="t('close')"><CloseOutlined /></Button></Dialog.CloseTrigger>
            <div class="ui-modal-body" :style="styles?.body"><slot /></div>
            <div v-if="footer !== null" class="ui-modal-footer"><slot name="footer">
              <Button :disabled="cancelButtonProps?.disabled" @click="close">{{ cancelText || t('cancel') }}</Button>
              <Button variant="primary" :danger="okButtonProps?.danger" :disabled="okButtonProps?.disabled" :loading="confirmLoading" @click="emit('ok')">{{ okText || 'OK' }}</Button>
            </slot></div>
          </Dialog.Content>
        </div>
      </Dialog.Positioner>
    </Teleport>
  </Dialog.Root>
</template>
