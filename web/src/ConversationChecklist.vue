<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconCheck, IconClock, IconAlertCircle, IconLoader2, IconMinus, IconPlayerStop, IconListCheck } from '@tabler/icons-vue'
import { Button, Popover } from './ui'
import type { ChecklistItem } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import { useEscapeDismiss } from './composables/useEscape'
import { checklistItems } from './state/checklist.ts'

const props = defineProps<{ items: ChecklistItem[]; active: boolean; status?: string }>()
const { t } = useI18n()
const { statusLabel } = useWorkspace()
const open = ref(false)
useEscapeDismiss(open)
const available = computed(() => props.active && props.items.length > 0)
watch(available, value => { if (!value) open.value = false })
const icons: Record<string, Component> = {
  done: IconCheck, in_progress: IconLoader2, blocked: IconAlertCircle,
  interrupted: IconPlayerStop, incomplete: IconMinus, pending: IconClock,
}
const items = computed(() => checklistItems(props.items, props.active, props.status))
const completed = computed(() => props.items.filter(item => item.status === 'done').length)
const progress = computed(() => t('traceChecklistProgress', { done: completed.value, total: props.items.length }))
</script>

<template>
  <Popover v-if="available" v-model:open="open" trigger="click" placement="bottomRight" root-class="conversation-checklist-popover"
    :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
    <template #content>
      <section class="conversation-checklist" :aria-label="t('checklist')">
        <div class="conversation-checklist-heading"><strong>{{ t('checklist') }}</strong><small>{{ progress }}</small></div>
        <ul class="checklist" :aria-label="t('checklist')">
          <li v-for="(item, index) in items" :key="index" :class="item.status" :data-checklist-status="item.status">
            <span class="checklist-icon" aria-hidden="true"><component :is="icons[item.status] || IconClock" :class="{ 'ui-icon-spin': item.status === 'in_progress' }" /></span>
            <span>{{ item.text }}</span><small>{{ statusLabel(item.status) }}</small>
          </li>
        </ul>
      </section>
    </template>
    <Button variant="text" class="icon-button header-status-trigger checklist-trigger" :aria-label="t('checklist')" :aria-expanded="open" :title="`${t('checklist')} · ${progress}`"><IconListCheck aria-hidden="true" /></Button>
  </Popover>
</template>

<style scoped>
.conversation-checklist{width:min(26rem,calc(100vw - 3.5rem));min-width:0}
.conversation-checklist-heading{display:flex;align-items:baseline;flex-wrap:wrap;gap:.25rem .75rem;margin-bottom:.4rem;color:var(--muted);font-size:14px;line-height:1.5}
.conversation-checklist-heading strong{color:var(--text);font-weight:500}
.conversation-checklist-heading small{font:inherit}
.checklist{max-height:min(55dvh,24rem);overflow:auto;overscroll-behavior:contain;overflow-wrap:anywhere;font-size:16px;line-height:1.65}
.checklist small{flex:none;color:var(--muted);font-size:14px}
.checklist-icon{display:inline-flex;align-items:center;justify-content:center;align-self:flex-start;flex:none;width:1em;height:1.65em}
@media(max-width:600px){.checklist li{flex-wrap:wrap}.checklist small{margin-inline-start:auto}}
@media(prefers-reduced-motion:reduce){.checklist-icon :deep(svg){animation:none!important}}
</style>
<style>
.conversation-checklist-popover .ui-popover-inner{max-height:calc(100dvh - 2rem);overflow:auto;overscroll-behavior:contain}
</style>
