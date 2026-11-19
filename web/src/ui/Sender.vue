<script setup lang="ts">
import { ref, type HTMLAttributes } from 'vue'
import TextArea from './TextArea.vue'
import type { TextAreaRef } from './types'
import { focusInput } from './focusInput'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ value: string; disabled?: boolean; loading?: boolean; placeholder?: string; enterKeyHint?: HTMLAttributes['enterkeyhint']; autoSize?: { minRows?: number; maxRows?: number }; onKeyDown?: (event: KeyboardEvent) => unknown; contentActive?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const input = ref<TextAreaRef>()
function focus(options?: FocusOptions & { cursor?: 'end' | 'start' }) {
  if (!props.contentActive) input.value?.focus(options)
}
defineExpose<TextAreaRef>({ focus, get input() { return input.value?.input } })
</script>

<template>
  <div v-bind="$attrs" class="ui-sender" :class="{ 'ui-sender-disabled': disabled }" @click.self="focus()">
    <div v-if="$slots.header" class="ui-sender-header"><slot name="header" /></div>
    <div v-show="!contentActive" class="ui-sender-content" @click="focusInput"><TextArea ref="input" class="ui-sender-input" :value="value" :disabled="disabled || contentActive" :placeholder="placeholder" :auto-size="autoSize" :aria-label="$attrs['aria-label']" :enterkeyhint="enterKeyHint" @update:value="emit('update:value', $event)" @keydown="onKeyDown?.($event)" /></div>
    <div v-if="$slots.content" v-show="contentActive" class="ui-sender-content ui-sender-alternate"><slot name="content" /></div>
    <div class="ui-sender-footer"><slot name="footer" /></div>
  </div>
</template>
