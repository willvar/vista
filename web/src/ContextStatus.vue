<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Popover } from 'antdv-next'
import { LoadingOutlined, CheckCircleOutlined, ExclamationCircleOutlined, UpOutlined, CodeOutlined } from '@antdv-next/icons'
import { useEscapeDismiss } from './composables/useEscape'
import MessageActions from './MessageActions.vue'
import type { CompactionStatus } from './client/types'
const props = withDefaults(defineProps<{
  status: CompactionStatus | null
  compacting: boolean
  manual: boolean
  cancelDisabled: boolean
  hasJobs?: boolean
  jobCount?: number
}>(), { hasJobs: false, jobCount: 0 })
const emit = defineEmits<{ cancel: []; openProcesses: [] }>()
const { t } = useI18n()
const open = ref(false)
useEscapeDismiss(open)
const phase = computed(() => props.compacting ? 'compacting' : props.status?.phase)
const statusLabel = computed(() => phase.value ? t(phase.value === 'compacting' ? 'contextCompacting' : phase.value === 'compacted' ? 'contextCompactionDone' : 'contextUnavailable') : '')
</script>
<template>
  <div v-if="status || compacting || hasJobs" class="context-status-panel" :class="[phase, { 'compaction-progress': compacting }]">
    <Popover v-if="status || compacting" v-model:open="open" trigger="click" placement="topLeft" root-class="context-status-popover"
      :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
      <template #content>
        <div class="context-status-detail" role="status">
          <p v-if="phase"><strong>{{ statusLabel }}</strong></p>
          <p v-if="compacting">{{ t(manual ? 'manualCompactionProgress' : 'autoCompactionProgress') }}</p>
          <p v-else-if="phase === 'compacted'">{{ t(status?.manual ? 'manualCompactionDone' : 'autoCompactionDone') }}</p>
          <template v-else-if="status?.error"><pre class="compaction-error">{{ status.error }}</pre><MessageActions :text="status.error" /><p>{{ t('runFailureHelp') }}</p></template>
        </div>
      </template>
      <button type="button" class="context-status" :aria-label="statusLabel" :aria-expanded="open" :title="t('contextDetails')">
        <span class="context-icon" aria-hidden="true"><LoadingOutlined v-if="compacting" /><CheckCircleOutlined v-else-if="phase === 'compacted'" /><ExclamationCircleOutlined v-else-if="phase === 'failed'" /></span>
        <span class="context-copy" role="status" aria-live="polite" aria-atomic="true">
          <span class="context-summary">
            <span v-if="phase" class="context-title">{{ statusLabel }}</span>
          </span>
          <span v-if="compacting" class="context-description">{{ t(manual ? 'manualCompactionProgress' : 'autoCompactionProgress') }}</span>
        </span>
        <UpOutlined class="context-disclosure" aria-hidden="true" />
      </button>
    </Popover>
    <button v-if="hasJobs" type="button" class="background-processes-trigger" :aria-label="t('backgroundProcessCount', { count: jobCount })" @click="emit('openProcesses')"><CodeOutlined aria-hidden="true" />{{ t('backgroundProcessCount', { count: jobCount }) }}</button>
    <Button v-if="compacting && manual" class="compaction-cancel" :aria-label="t('cancel')" :disabled="cancelDisabled" @click="emit('cancel')">{{ t('cancel') }}</Button>
  </div>
</template>
<style scoped>
.context-status-panel{display:flex;align-items:center;gap:.75rem;padding:.35rem .65rem;border-bottom:1px solid var(--border);border-radius:15px 15px 0 0;background:var(--surface-subtle)}
.context-status{display:flex;align-items:center;gap:.65rem;flex:1;min-width:0;padding:.4rem;border:0;border-radius:8px;background:transparent;color:var(--muted);font:inherit;text-align:left;cursor:pointer}
.context-status:hover{background:var(--hover)}
.background-processes-trigger{display:flex;align-items:center;gap:.4rem;flex:none;padding:.4rem;border:0;border-radius:8px;background:transparent;color:var(--muted);font:inherit;font-size:12px;cursor:pointer}
.background-processes-trigger:hover{background:var(--hover);color:var(--text)}
.background-processes-trigger:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.context-status:focus-visible,.compaction-cancel.ant-btn:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.context-icon{display:grid;place-items:center;flex:none;width:20px;height:20px;font-size:16px}
.compacting .context-icon{width:34px;height:34px;border-radius:10px;background:var(--accent-soft);color:var(--accent);font-size:18px}
.compacted .context-icon{color:var(--success)}
.failed .context-icon{color:var(--warning)}
.context-copy{flex:1;min-width:0;line-height:1.5;overflow-wrap:anywhere}
.context-summary{display:flex;align-items:baseline;flex-wrap:wrap;gap:.15rem .8rem}
.context-title{color:var(--text);font-size:13px;font-weight:500}
.compacting .context-title{font-size:14px;font-weight:600}
.context-description{display:block;margin-top:.2rem;font-size:13px}
.context-disclosure{flex:none;font-size:10px;opacity:.7}
.compaction-cancel.ant-btn{flex:none;height:32px;padding-inline:.75rem;border-color:var(--border-strong);border-radius:8px;background:transparent;color:var(--text-secondary);box-shadow:none}
.compaction-cancel.ant-btn:not(:disabled):hover{border-color:var(--border-strong);background:var(--hover);color:var(--text)}
.compaction-cancel.ant-btn:disabled{opacity:.45}
.context-status-detail{max-width:22rem;overflow-wrap:anywhere;font-size:14px;line-height:1.6}
.context-status-detail p{margin:.4rem 0 0}
.compaction-error{white-space:pre-wrap;overflow-wrap:anywhere;font-size:13px;max-width:28rem}
@media(max-width:600px){.context-status-panel{gap:.4rem;padding:.35rem .45rem;flex-wrap:wrap}.context-status{gap:.5rem}.background-processes-trigger{margin-inline-start:auto}}
@media(pointer:coarse){.context-status,.background-processes-trigger{min-height:44px}.compaction-cancel.ant-btn{min-height:44px}}
@media(prefers-reduced-motion:reduce){.context-icon :deep(svg){animation:none!important}}
@media(max-height:520px){.context-status-panel{padding-block:.2rem}.compacting .context-icon{width:28px;height:28px;border-radius:8px;font-size:16px}.context-description{margin-top:.1rem;font-size:12px}}
</style>
<style>
.context-status-popover .ant-popover-inner{max-height:calc(100dvh - 2rem);overflow:auto;overscroll-behavior:contain}
</style>
