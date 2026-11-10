<script setup lang="ts">
import { NumberInputIncrementTrigger, NumberInputDecrementTrigger, NumberInputControl, NumberInputInput, NumberInputRoot } from '@ark-ui/vue/number-input'
import { IconChevronUp, IconChevronDown } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
defineOptions({ inheritAttrs: false })
withDefaults(defineProps<{ value?: number | null; min?: number; max?: number; step?: number; precision?: number; clampValueOnBlur?: boolean; disabled?: boolean; id?: string }>(), { clampValueOnBlur: true })
const emit = defineEmits<{ 'update:value': [value: number | null]; change: [value: number | null] }>()
const { t } = useI18n()
function change(details: { value: string; valueAsNumber: number }) {
  const value = Number.isFinite(details.valueAsNumber) ? details.valueAsNumber : null
  emit('update:value', value); emit('change', value)
}
</script>

<template>
  <NumberInputRoot class="ui-input-number" :class="$attrs.class" :style="$attrs.style" :model-value="value == null ? '' : String(value)"
    :min="min" :max="max" :step="step" :clamp-value-on-blur="clampValueOnBlur" :disabled="disabled" :ids="id ? { input: id } : undefined" :format-options="precision == null ? undefined : { useGrouping: false, maximumFractionDigits: precision }"
    :translations="{ incrementLabel: t('uiIncrease'), decrementLabel: t('uiDecrease') }"
    @value-change="change">
    <NumberInputInput v-bind="{ ...$attrs, class: undefined, style: undefined }" class="ui-input-number-input" />
    <NumberInputControl class="ui-number-controls"><NumberInputIncrementTrigger tabindex="-1"><IconChevronUp /></NumberInputIncrementTrigger><NumberInputDecrementTrigger tabindex="-1"><IconChevronDown /></NumberInputDecrementTrigger></NumberInputControl>
  </NumberInputRoot>
</template>
