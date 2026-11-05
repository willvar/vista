<script setup lang="ts">
import { ref, type AriaAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { focusInput } from './focusInput'
defineOptions({ inheritAttrs: false })
defineProps<{ value?: string | number; allowClear?: boolean; disabled?: boolean; readonly?: boolean; status?: 'error' | 'warning' }>()
const emit = defineEmits<{ 'update:value': [value: string]; change: [event: Event] }>()
const { t } = useI18n()
const input = ref<HTMLInputElement>()
function update(event: Event) { emit('update:value', (event.target as HTMLInputElement).value) }
defineExpose({ focus: (options?: FocusOptions) => input.value?.focus(options), input })
</script>

<template>
  <span v-if="$slots.prefix || allowClear" class="ui-input-affix-wrapper" :class="[$attrs.class, { 'ui-input-disabled': disabled, 'ui-input-error': status === 'error' }]" :style="$attrs.style" @click="focusInput">
    <span v-if="$slots.prefix" class="ui-input-prefix"><slot name="prefix" /></span>
    <input ref="input" v-bind="{ ...$attrs, class: undefined, style: undefined }" class="ui-input" :value="value" :disabled="disabled" :readonly="readonly" :aria-invalid="status === 'error' || ($attrs['aria-invalid'] as AriaAttributes['aria-invalid'])" @input="update" @change="emit('change', $event)" />
    <button v-if="allowClear && value && !disabled && !readonly" type="button" class="ui-input-clear" :aria-label="t('remove')" @click="emit('update:value', ''); input?.focus()">×</button>
  </span>
  <input v-else ref="input" v-bind="$attrs" class="ui-input" :class="{ 'ui-input-error': status === 'error' }" :value="value" :disabled="disabled" :readonly="readonly" :aria-invalid="status === 'error' || ($attrs['aria-invalid'] as AriaAttributes['aria-invalid'])" @input="update" @change="emit('change', $event)" />
</template>
