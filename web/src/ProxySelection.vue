<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import SchemaForm from './SchemaForm.vue'
import type { ProxyProfile } from './client/network'
const props = defineProps<{ id: string; modelValue: string; profiles: ProxyProfile[]; disabled?: boolean; resetKey?: string | number }>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; validity: [valid: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const value = computed<Record<string, unknown>>({ get: () => ({ proxyId: props.modelValue }), set: value => emit('update:modelValue', String(value.proxyId || '')) })
const schema = computed(() => ({ type: 'object' as const, additionalProperties: false, required: ['proxyId'], properties: {
  proxyId: { type: 'string' as const, title: t('networkProxy'), enum: ['', ...props.profiles.map(profile => profile.id)] },
} }))
const ui = computed(() => ({ type: 'Control' as const, scope: '#/properties/proxyId', options: { showSearch: true, allowClear: true, placeholder: t(props.profiles.length ? 'networkUnconfigured' : 'networkNoProfilesPlaceholder'),
  enumLabels: Object.fromEntries(props.profiles.map(profile => [profile.id, profile.name])),
} }))
</script>
<template>
  <div class="proxy-selection" :class="{ 'proxy-selection-empty': !profiles.length }">
    <SchemaForm :id="id" v-model="value" :schema="schema" :uischema="ui" :disabled="disabled || !profiles.length" :reset-key="resetKey" @validity="emit('validity', $event)" />
  </div>
</template>
<style scoped>
.proxy-selection { display: flex; flex-direction: column; align-items: stretch; gap: .35rem; min-width: 0; }
.proxy-selection :deep(.ui-select-clear) { inset-inline-end: 8px; }
.proxy-selection-empty :deep(.ui-select-arrow),
.proxy-selection :deep(.ui-select-selector:has(.ui-select-clear) .ui-select-arrow) { display: none; }
</style>
