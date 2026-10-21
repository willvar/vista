<script setup lang="ts">
import { reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from 'antdv-next'
import { useWorkspace } from './composables/useWorkspace'
import { useFullOutput } from './composables/useFullOutput'
import { renderMarkdown } from './ui/markdown'
import PlainTextOutput from './PlainTextOutput.vue'

const props = withDefaults(defineProps<{ path: string; snapshot: string; expanded?: boolean; markdown?: boolean }>(), { expanded: true, markdown: false })
const { t } = useI18n()
const { client } = useWorkspace()
const source = reactive({ get path() { return props.path }, get snapshot() { return props.snapshot }, get enabled() { return props.expanded } })
const { output, loading, failure, retry } = useFullOutput(client, source)
</script>

<template>
  <small v-if="loading && !output" class="trace-output-loading" role="status">{{ t('outputLoading') }}</small>
  <div v-if="failure" class="trace-output-load-error"><small role="alert">{{ t('outputLoadFailed') }}</small><Button size="small" @click="retry">{{ t('retry') }}</Button></div>
  <slot v-if="failure && !output" name="fallback" />
  <slot v-if="output" :content="output.content">
    <div v-if="markdown" class="markdown trace-report-body" v-html="renderMarkdown(output.content)"></div>
    <PlainTextOutput v-else :content="output.content" />
  </slot>
</template>

<style scoped>
.trace-output-loading,.trace-output-load-error{font-size:15px;color:var(--text-secondary)}.trace-output-load-error{display:flex;align-items:center;gap:.5rem;margin:.35rem 0}
</style>
