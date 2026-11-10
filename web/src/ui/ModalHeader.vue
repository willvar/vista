<script setup lang="ts">
import { DialogCloseTrigger, DialogTitle } from '@ark-ui/vue/dialog'
import { IconArrowLeft, IconX } from '@tabler/icons-vue'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Button from './Button.vue'

withDefaults(defineProps<{
  title?: string
  closable?: boolean
  closeLabel?: string
  closeDisabled?: boolean
  backLabel?: string
}>(), { closable: true })
const emit = defineEmits<{ back: [] }>()
const { t } = useI18n()
const heading = ref<{ $el: HTMLElement }>()
defineExpose({ focusTitle: () => heading.value?.$el.focus({ preventScroll: true }) })
</script>

<template>
  <header class="ui-modal-header">
    <Button v-if="backLabel" variant="text" class="icon-button ui-modal-back" :aria-label="backLabel" :title="backLabel" :disabled="closeDisabled" @click="emit('back')"><IconArrowLeft aria-hidden="true" /></Button>
    <DialogTitle ref="heading" class="ui-modal-title" :class="{ 'visually-hidden': !title && !$slots.title }" tabindex="-1"><slot name="title">{{ title || 'Vista' }}</slot></DialogTitle>
    <div v-if="closable || $slots.actions" class="ui-modal-header-actions">
      <slot name="actions" />
      <DialogCloseTrigger v-if="closable" as-child>
        <Button variant="text" class="icon-button ui-modal-close" :aria-label="closeLabel || t('close')" :title="closeLabel || t('close')" :disabled="closeDisabled"><IconX aria-hidden="true" /></Button>
      </DialogCloseTrigger>
    </div>
  </header>
</template>
