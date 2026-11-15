<script setup lang="ts">
import { DialogPositioner, DialogBackdrop, DialogContent, DialogRoot, type DialogInteractOutsideEvent } from '@ark-ui/vue/dialog'
import { ref, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import Button from './Button.vue'
import ModalHeader from './ModalHeader.vue'
import type { ModalProps } from './types'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<ModalProps>(), { closable: true, keyboard: true, maskClosable: true, width: 520, zIndex: 1000 })
const emit = defineEmits<{ 'update:open': [open: boolean]; cancel: []; ok: [] }>()
const { t } = useI18n()
const content = ref<{ $el: HTMLElement }>()
// Outside detection is deferred. An interaction that started while locked
// must not inherit a later unlock before Ark delivers its outside callback.
const blockedInteractions = new WeakSet<Event>()
watchEffect(onCleanup => {
  const doc = content.value?.$el.ownerDocument
  if (!props.open || !doc) return
  const controller = new AbortController()
  const options = { capture: true, signal: controller.signal }
  let touch: PointerEvent | undefined
  const remember = (event: Event, blocked = !props.maskClosable) => {
    if (blocked) blockedInteractions.add(event)
  }
  doc.addEventListener('pointerdown', event => {
    remember(event)
    touch = event.pointerType === 'touch' ? event : undefined
  }, options)
  doc.addEventListener('click', (event: MouseEvent) => {
    // For touch Ark reports the click generated after finger release.
    remember(event)
    if (touch && ('pointerId' in event ? event.pointerId === touch.pointerId : event.detail > 0)) {
      if (blockedInteractions.has(touch)) blockedInteractions.add(event)
      touch = undefined
    }
  }, options)
  doc.addEventListener('pointercancel', event => { if (touch?.pointerId === event.pointerId) touch = undefined }, options)
  doc.addEventListener('focusin', event => remember(event), options)
  onCleanup(() => controller.abort())
})
function interactOutside(event: DialogInteractOutsideEvent) {
  if (blockedInteractions.has(event.detail.originalEvent)) event.preventDefault()
}
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
  <DialogRoot :open="!!open" :close-on-escape="keyboard" :close-on-interact-outside="maskClosable" lazy-mount :unmount-on-exit="destroyOnHidden"
    :initial-focus-el="initialFocus" @interact-outside="interactOutside" @open-change="!$event.open && close()" @exit-complete="exitComplete">
    <Teleport to="body">
      <DialogBackdrop class="ui-modal-mask" :class="rootClass" :style="[{ zIndex }, styles?.mask]" />
      <DialogPositioner class="ui-modal-wrap" :class="[wrapClassName, rootClass]" :style="{ zIndex }">
        <div v-bind="$attrs" class="ui-modal" :style="[{ width: typeof width === 'number' ? `${width}px` : width }, $attrs.style]">
          <DialogContent ref="content" class="ui-modal-container" :style="styles?.container">
            <slot v-if="$slots.header" name="header" />
            <ModalHeader v-else :title="title" :closable="closable" :close-label="closeLabel" :close-disabled="closeDisabled">
              <template v-if="$slots.title" #title><slot name="title" /></template>
              <template v-if="$slots['header-actions']" #actions><slot name="header-actions" /></template>
            </ModalHeader>
            <div class="ui-modal-body" :style="styles?.body"><slot /></div>
            <div v-if="footer !== null" class="ui-modal-footer"><slot name="footer">
              <Button :disabled="cancelButtonProps?.disabled" @click="close">{{ cancelText || t('cancel') }}</Button>
              <Button variant="primary" :danger="okButtonProps?.danger" :disabled="okButtonProps?.disabled" :loading="confirmLoading" @click="emit('ok')">{{ okText || 'OK' }}</Button>
            </slot></div>
          </DialogContent>
        </div>
      </DialogPositioner>
    </Teleport>
  </DialogRoot>
</template>
