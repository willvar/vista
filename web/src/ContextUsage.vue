<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Popover } from 'antdv-next'
import { useEscapeDismiss } from './composables/useEscape'
import type { ContextUsage } from './client/types'

const props = defineProps<{ usage: ContextUsage }>()
const { t } = useI18n()
const open = ref(false)
useEscapeDismiss(open)
const number = new Intl.NumberFormat('en-US', { maximumFractionDigits: 1 })
const tokens = computed(() => `${number.format(props.usage.tokens / 1000)}K`)
</script>

<template>
  <Popover v-model:open="open" :trigger="['hover', 'click']" placement="top" root-class="context-usage-popover">
    <template #content><p class="context-usage-hint">{{ t('contextUsageDetail') }}</p></template>
    <button type="button" class="context-usage" :data-tokens="usage.tokens" :aria-label="t('contextUsage', { tokens })" :aria-expanded="open">{{ tokens }}</button>
  </Popover>
</template>

<style scoped>
.context-usage{flex:none;padding:6px 4px;border:0;border-radius:6px;background:transparent;color:var(--muted);font:inherit;font-size:12px;font-variant-numeric:tabular-nums;white-space:nowrap;cursor:pointer}
.context-usage:hover{background:var(--hover);color:var(--text)}
.context-usage:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.context-usage-hint{max-width:min(20rem,calc(100vw - 3rem));margin:0;line-height:1.6;overflow-wrap:anywhere}
@media(pointer:coarse){.context-usage{min-height:44px}}
</style>
