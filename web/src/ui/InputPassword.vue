<script setup lang="ts">
import { PasswordInput } from '@ark-ui/vue/password-input'
import { IconEye, IconEyeOff } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import { focusInput } from './focusInput'
defineOptions({ inheritAttrs: false })
defineProps<{ value?: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const { t } = useI18n()
</script>

<template>
  <PasswordInput.Root class="ui-input-affix-wrapper ui-password" :class="$attrs.class" :style="$attrs.style" :disabled="disabled" :translations="{ visibilityTrigger: visible => t(visible ? 'uiHideValue' : 'uiShowValue') }" @click="focusInput">
    <PasswordInput.Input v-bind="{ ...$attrs, class: undefined, style: undefined }" class="ui-input" :value="value" @input="emit('update:value', ($event.target as HTMLInputElement).value)" />
    <PasswordInput.VisibilityTrigger class="ui-password-toggle"><PasswordInput.Indicator><IconEye /><template #fallback><IconEyeOff /></template></PasswordInput.Indicator></PasswordInput.VisibilityTrigger>
  </PasswordInput.Root>
</template>
