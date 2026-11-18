<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GenerationCard } from './state/generationCards'
import { Spin as VSpin } from './ui'
const props = defineProps<{ sessionId: string; card: GenerationCard }>()
const { t } = useI18n()
const url = computed(() => props.card.hash ? `/api/sessions/${encodeURIComponent(props.sessionId)}/resources/${props.card.hash}` : '')
const statusKey = computed(() => ({ started: props.card.kind === 'video' ? 'generationVideoRunning' : 'generationImageRunning', succeeded: 'generationReady', failed: 'generationFailed', stopped: 'generationStopped', canceled: 'generationCanceled' })[props.card.status])
</script>

<template>
  <div class="generation-card" :data-generation-kind="card.kind" :data-generation-status="card.status" role="status" aria-live="polite">
    <span><v-spin v-if="card.status === 'started'" small /> {{ t(statusKey) }}</span>
    <template v-if="card.status === 'succeeded' && url">
      <img v-if="card.kind === 'image'" :src="url" :alt="t('generationImage')" loading="lazy" />
      <a v-else :href="url" target="_blank" rel="noreferrer">{{ t('generationOpenVideo') }}</a>
      <details><summary>{{ t('generationResourceID') }}</summary><code>{{ card.hash }}</code></details>
    </template>
  </div>
</template>

<style scoped>
.generation-card{max-width:400px;margin-block:8px;padding:12px 14px;display:grid;gap:8px;border:1px solid var(--border);border-radius:10px;font-size:var(--font-size-small);overflow-wrap:anywhere}.generation-card img{display:block;max-width:100%;max-height:320px;object-fit:contain}.generation-card[data-generation-status=started]{min-height:72px;align-content:center}
</style>
