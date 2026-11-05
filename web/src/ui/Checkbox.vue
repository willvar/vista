<script setup lang="ts">
import { Checkbox } from '@ark-ui/vue/checkbox'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ checked?: boolean; indeterminate?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:checked': [checked: boolean]; change: [event: { target: { checked: boolean } }] }>()
function change(details: { checked: boolean | 'indeterminate' }) {
  const checked = details.checked === true
  emit('update:checked', checked); emit('change', { target: { checked } })
}
function syncInput(event: Event) { (event.target as HTMLInputElement).checked = !!props.checked && !props.indeterminate }
</script>

<template>
  <Checkbox.Root v-bind="$attrs" class="ui-checkbox-wrapper" :class="{ 'ui-checkbox-wrapper-checked': checked }" :checked="indeterminate ? 'indeterminate' : !!checked" :disabled="disabled" @checked-change="change">
    <Checkbox.HiddenInput v-bind="$attrs['aria-label'] ? { 'aria-label': $attrs['aria-label'] as string, 'aria-labelledby': undefined } : {}" :checked="!!checked && !indeterminate" @change="syncInput" />
    <Checkbox.Control class="ui-checkbox"><Checkbox.Indicator><svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3 8 3 3 7-7" fill="none" stroke="currentColor" stroke-width="2" /></svg></Checkbox.Indicator><span v-if="indeterminate" class="ui-checkbox-indeterminate" /></Checkbox.Control>
    <Checkbox.Label class="ui-checkbox-label"><slot /></Checkbox.Label>
  </Checkbox.Root>
</template>
