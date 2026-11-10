<script setup lang="ts">
import { SwitchHiddenInput, SwitchControl, SwitchThumb, SwitchRoot } from '@ark-ui/vue/switch'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ checked?: boolean; disabled?: boolean; loading?: boolean; id?: string }>()
const emit = defineEmits<{ 'update:checked': [checked: boolean] }>()
// Settings can wait for a server acknowledgement. Keep the native input in
// sync even when a click and a remote update leave the controlled value equal.
function syncInput(event: Event) { (event.target as HTMLInputElement).checked = !!props.checked }
</script>

<template>
  <SwitchRoot class="ui-switch" :class="[$attrs.class, { 'ui-switch-checked': checked }]" :style="$attrs.style" :checked="!!checked" :disabled="disabled || loading" :ids="id ? { hiddenInput: id } : undefined" @checked-change="emit('update:checked', $event.checked)">
    <SwitchHiddenInput v-bind="{ ...$attrs, class: undefined, style: undefined }" role="switch" :checked="!!checked" :aria-checked="!!checked" @change="syncInput" />
    <SwitchControl class="ui-switch-track"><SwitchThumb class="ui-switch-handle"><span v-if="loading" class="ui-spinner" /></SwitchThumb></SwitchControl>
  </SwitchRoot>
</template>
