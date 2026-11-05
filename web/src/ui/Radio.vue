<script setup lang="ts">
defineOptions({ inheritAttrs: false })
defineProps<{ checked?: boolean; name?: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:checked': [checked: boolean] }>()
</script>

<template>
  <label class="ui-radio-wrapper" :class="[$attrs.class, { 'ui-radio-wrapper-checked': checked }]" :style="$attrs.style">
    <input v-bind="{ ...$attrs, class: undefined, style: undefined }" type="radio" class="ui-radio" :name="name" :checked="checked" :disabled="disabled" @change="emit('update:checked', ($event.target as HTMLInputElement).checked)" />
    <span class="ui-radio-label"><slot /></span>
  </label>
</template>
