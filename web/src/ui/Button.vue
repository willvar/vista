<script setup lang="ts">
const props = withDefaults(defineProps<{
  href?: string
  variant?: 'default' | 'primary' | 'text' | 'link'
  type?: 'button' | 'submit' | 'reset'
  danger?: boolean
  disabled?: boolean
  loading?: boolean
  shape?: 'circle' | 'round'
}>(), { variant: 'default', type: 'button' })
const emit = defineEmits<{ click: [event: MouseEvent] }>()
function click(event: MouseEvent) {
  if (props.disabled || props.loading) { event.preventDefault(); event.stopPropagation(); return }
  emit('click', event)
}
</script>

<template>
  <component :is="href ? 'a' : 'button'" :type="href ? undefined : type" :href="disabled || loading ? undefined : href" class="ui-btn" :class="[`ui-btn-${variant}`, { 'ui-btn-dangerous': danger, 'ui-btn-loading': loading, 'ui-btn-circle': shape === 'circle' }]"
    :disabled="href ? undefined : disabled || loading" :aria-disabled="href && (disabled || loading) || undefined" :tabindex="href && (disabled || loading) ? -1 : undefined" :aria-busy="loading || undefined" @click="click">
    <span v-if="loading" class="ui-spinner" aria-hidden="true" /><slot v-else name="icon" /><slot />
  </component>
</template>
