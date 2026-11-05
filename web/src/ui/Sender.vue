<script setup lang="ts">
import { ref } from 'vue'
import TextArea from './TextArea.vue'
import type { TextAreaRef } from './types'
import { focusInput } from './focusInput'
defineOptions({ inheritAttrs: false })
defineProps<{ value: string; disabled?: boolean; loading?: boolean; placeholder?: string; autoSize?: { minRows?: number; maxRows?: number }; onKeyDown?: (event: KeyboardEvent) => unknown }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const input = ref<TextAreaRef>()
defineExpose({ focus: (options?: FocusOptions & { cursor?: 'end' | 'start' }) => input.value?.focus(options) })
</script>

<template>
  <div v-bind="$attrs" class="ui-sender" :class="{ 'ui-sender-disabled': disabled }" @click.self="input?.focus()">
    <div v-if="$slots.header" class="ui-sender-header"><slot name="header" /></div>
    <div class="ui-sender-content" @click="focusInput"><TextArea ref="input" class="ui-sender-input" :value="value" :disabled="disabled" :placeholder="placeholder" :auto-size="autoSize" :aria-label="$attrs['aria-label']" @update:value="emit('update:value', $event)" @keydown="onKeyDown?.($event)" /></div>
    <div class="ui-sender-footer"><slot name="footer" /></div>
  </div>
</template>
