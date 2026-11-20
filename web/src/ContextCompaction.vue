<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconProgressBolt } from '@tabler/icons-vue'
import { Button as VButton, Popover as VPopover } from './ui'
import type { ContextUsage } from './client/types'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ usage: ContextUsage | null; disabled: boolean }>()
const emit = defineEmits<{ compact: [] }>()
const { t } = useI18n({ useScope: 'global' })
const { route, mobileSessionsOpen } = useWorkspace()
const open = ref(false)
const number = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
const tokens = computed(() => props.usage ? number.format(props.usage.tokens) : '')
watch([() => route.fullPath, mobileSessionsOpen], () => { open.value = false })
function compact() {
  if (props.disabled || !open.value) return
  open.value = false
  emit('compact')
}
</script>

<template>
  <v-popover v-model:open="open" placement="bottomRight" :aria-label="t('contextPanelTitle')"
    :styles="{ root: { width: 'min(22rem, calc(100vw - 24px))', maxHeight: 'var(--available-height, calc(100dvh - 2rem))', overflowY: 'auto', overscrollBehavior: 'contain' } }">
    <template #content>
      <div class="context-popover-panel">
        <strong>{{ t('contextPanelTitle') }}</strong>
        <i18n-t v-if="usage" scope="global" keypath="contextUsageDetail" tag="p" class="context-usage-estimate">
          <template #tokens><strong>{{ tokens }}</strong></template>
        </i18n-t>
        <p v-else class="context-usage-estimate">{{ t('contextUsageMissing') }}</p>
        <p>{{ t('compactContextAutomatic') }}</p>
        <p>{{ t('compactContextTradeoff') }}</p>
        <v-button variant="primary" :disabled="disabled" @click="compact">{{ t('compactContext') }}</v-button>
      </div>
    </template>
    <v-button class="icon-button header-status-trigger context-compaction" variant="text" :data-tokens="usage?.tokens"
      :aria-label="t('contextPanelTitle')" :title="t('contextPanelTitle')"><IconProgressBolt aria-hidden="true" /></v-button>
  </v-popover>
</template>

<style scoped>
.context-popover-panel{display:grid;gap:.55rem;overflow-wrap:anywhere}
.context-popover-panel p{margin:0}
.context-popover-panel .ui-btn{justify-self:end}
</style>
