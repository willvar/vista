<script setup lang="ts">
import { NumberInput } from '@ark-ui/vue/number-input'
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
  <NumberInput.Root class="ui-input-number" :class="$attrs.class" :style="$attrs.style" :model-value="value == null ? '' : String(value)"
    :min="min" :max="max" :step="step" :clamp-value-on-blur="clampValueOnBlur" :disabled="disabled" :ids="id ? { input: id } : undefined" :format-options="precision == null ? undefined : { useGrouping: false, maximumFractionDigits: precision }"
    :translations="{ incrementLabel: t('uiIncrease'), decrementLabel: t('uiDecrease') }"
    @value-change="change">
    <NumberInput.Input v-bind="{ ...$attrs, class: undefined, style: undefined }" class="ui-input-number-input" />
    <NumberInput.Control class="ui-number-controls"><NumberInput.IncrementTrigger tabindex="-1"><IconChevronUp /></NumberInput.IncrementTrigger><NumberInput.DecrementTrigger tabindex="-1"><IconChevronDown /></NumberInput.DecrementTrigger></NumberInput.Control>
  </NumberInput.Root>
</template>
