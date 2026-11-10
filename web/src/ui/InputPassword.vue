<script setup lang="ts">
import { PasswordInputVisibilityTrigger, PasswordInputIndicator, PasswordInputInput, PasswordInputRoot } from '@ark-ui/vue/password-input'
import { IconEye, IconEyeOff } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import { focusInput } from './focusInput'
defineOptions({ inheritAttrs: false })
defineProps<{ value?: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const { t } = useI18n()
</script>

<template>
  <PasswordInputRoot class="ui-input-affix-wrapper ui-password" :class="$attrs.class" :style="$attrs.style" :disabled="disabled" :translations="{ visibilityTrigger: visible => t(visible ? 'uiHideValue' : 'uiShowValue') }" @click="focusInput">
    <PasswordInputInput v-bind="{ ...$attrs, class: undefined, style: undefined }" class="ui-input" :value="value" @input="emit('update:value', ($event.target as HTMLInputElement).value)" />
    <PasswordInputVisibilityTrigger class="ui-password-toggle"><PasswordInputIndicator><IconEye /><template #fallback><IconEyeOff /></template></PasswordInputIndicator></PasswordInputVisibilityTrigger>
  </PasswordInputRoot>
</template>
