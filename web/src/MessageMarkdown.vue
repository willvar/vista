<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { renderMarkdown } from './ui/markdown'
const props = defineProps<{ content: string; role: string }>()
const emit = defineEmits<{ failure: [error: unknown] }>()
const { t } = useI18n()
const rendered = computed(() => {
  try { return { html: renderMarkdown(props.content), failed: false } }
  catch (error) { emit('failure', error); return { html: '', failed: true } }
})
</script>

<template>
  <template v-if="rendered.failed"><small role="status">{{ t('messagePlainTextFallback') }}</small><div class="markdown message-plain-text" :data-quote-role="role">{{ content }}</div></template>
  <div v-else class="markdown" :data-quote-role="role" v-html="rendered.html"></div>
</template>

<style scoped>.message-plain-text{white-space:pre-wrap;overflow-wrap:anywhere}</style>
