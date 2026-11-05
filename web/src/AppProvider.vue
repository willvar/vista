<script setup lang="ts">
import { LocaleProvider } from '@ark-ui/vue/locale'
import { useInteractionModality } from '@ark-ui/vue/interaction'
import { onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
const { locale } = useI18n({ useScope: 'global' })
const modality = useInteractionModality()
watch(modality, value => {
  document.documentElement.dataset.interactionModality = value ?? ''
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => document.documentElement.removeAttribute('data-interaction-modality'))
</script>

<template>
  <LocaleProvider :locale="locale"><slot /></LocaleProvider>
</template>
