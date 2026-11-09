<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconProgressBolt } from '@tabler/icons-vue'
import { Button as VButton, Modal as VModal, Tooltip as VTooltip } from './ui'
import type { ContextUsage } from './client/types'

const props = defineProps<{ usage: ContextUsage | null; disabled: boolean }>()
const emit = defineEmits<{ compact: [] }>()
const { t } = useI18n({ useScope: 'global' })
const confirming = ref(false)
const number = new Intl.NumberFormat('en-US', { maximumFractionDigits: 1 })
const tokens = computed(() => props.usage ? `${number.format(props.usage.tokens / 1000)}K` : '')
watch(() => props.disabled, disabled => { if (disabled) confirming.value = false })
function confirm() {
  if (props.disabled || !confirming.value) return
  confirming.value = false
  emit('compact')
}
</script>

<template>
  <v-tooltip placement="bottomRight">
    <template #title><strong>{{ t('compactContext') }}</strong><p v-if="usage" class="context-usage-hint">{{ t('contextUsageDetail', { tokens }) }}</p></template>
    <span class="context-compaction-trigger" :tabindex="disabled ? 0 : undefined">
      <v-button class="icon-button header-status-trigger context-compaction" variant="text" :disabled="disabled" :data-tokens="usage?.tokens" :aria-label="t('compactContext')" @click="confirming = true"><IconProgressBolt aria-hidden="true" /></v-button>
    </span>
  </v-tooltip>
  <v-modal :open="confirming" centered width="min(480px, calc(100vw - 2rem))" :title="t('compactContextTitle')" :ok-text="t('compactContext')" :ok-button-props="{ disabled }" :styles="{ body: { maxHeight: 'calc(100dvh - 12rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" destroy-on-hidden @cancel="confirming = false" @ok="confirm">
    <div class="context-compaction-confirmation"><p>{{ t('compactContextAutomatic') }}</p><p>{{ t('compactContextTradeoff') }}</p></div>
  </v-modal>
</template>

<style scoped>
.context-compaction-trigger{display:inline-flex;border-radius:8px}
.context-compaction-trigger:focus-visible{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:2px}
.context-usage-hint{max-width:min(20rem,calc(100vw - 3rem));margin:4px 0 0;line-height:1.6;overflow-wrap:anywhere}
.context-compaction-confirmation{line-height:1.7;overflow-wrap:anywhere}
.context-compaction-confirmation p{margin:0 0 12px}
.context-compaction-confirmation p:last-child{margin-bottom:0}
</style>
