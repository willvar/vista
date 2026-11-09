<script setup lang="ts">
import { computed, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconCheck, IconClock, IconAlertCircle, IconLoader2, IconMinus, IconPlayerStop } from '@tabler/icons-vue'
import type { ChecklistItem } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import { checklistItems } from './state/checklist.ts'

const props = defineProps<{ items: ChecklistItem[]; active: boolean; status?: string }>()
const { t } = useI18n()
const { statusLabel } = useWorkspace()
const icons: Record<string, Component> = {
  done: IconCheck, in_progress: IconLoader2, blocked: IconAlertCircle,
  interrupted: IconPlayerStop, incomplete: IconMinus, pending: IconClock,
}
const items = computed(() => checklistItems(props.items, props.active, props.status))
const completed = computed(() => props.items.filter(item => item.status === 'done').length)
</script>

<template>
  <section v-if="items.length" class="conversation-checklist" :aria-label="t('checklist')">
    <div class="conversation-checklist-heading"><strong>{{ t('checklist') }}</strong><small>{{ t('traceChecklistProgress', { done: completed, total: items.length }) }}</small></div>
    <ul class="checklist" :aria-label="t('checklist')">
      <li v-for="(item, index) in items" :key="index" :class="item.status" :data-checklist-status="item.status">
        <span class="checklist-icon" aria-hidden="true"><component :is="icons[item.status] || IconClock" :class="{ 'ui-icon-spin': item.status === 'in_progress' }" /></span>
        <span>{{ item.text }}</span><small>{{ statusLabel(item.status) }}</small>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.conversation-checklist{flex:none;min-width:0;margin:.5rem 0;padding:.65rem .9rem;border:1px solid var(--border-subtle);border-radius:12px;background:var(--surface-subtle)}
.conversation-checklist-heading{display:flex;align-items:baseline;flex-wrap:wrap;gap:.25rem .75rem;margin-bottom:.25rem;color:var(--muted);font-size:14px;line-height:1.5}
.conversation-checklist-heading strong{color:var(--text);font-weight:500}
.conversation-checklist-heading small{font:inherit}
.checklist{max-height:min(24dvh,12rem);overflow:auto;overscroll-behavior:contain;overflow-wrap:anywhere;font-size:16px;line-height:1.65}
.checklist small{flex:none;color:var(--muted);font-size:14px}
.checklist-icon{display:inline-flex;align-items:center;justify-content:center;align-self:flex-start;flex:none;width:1em;height:1.65em}
@media(max-width:600px){.conversation-checklist{padding:.5rem .65rem}.checklist li{flex-wrap:wrap}.checklist small{margin-inline-start:auto}}
@media(max-height:520px){.conversation-checklist{margin:.25rem 0;padding:.3rem .6rem}.checklist{max-height:16dvh}.conversation-checklist-heading{margin-bottom:0}}
@media(prefers-reduced-motion:reduce){.checklist-icon :deep(svg){animation:none!important}}
</style>
