<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { Translation as I18nT, useI18n } from 'vue-i18n'
import { IconLoader2, IconAlertCircle, IconPlayerStop, IconChevronUp } from '@tabler/icons-vue'
import { Popover } from './ui'
import MessageActions from './MessageActions.vue'
import FailureHelp from './FailureHelp.vue'
import { useEscapeDismiss } from './composables/useEscape'
import { useWorkspace } from './composables/useWorkspace'
import type { CompactionStatus, RunClock } from './client/types'

const props = defineProps<{ status: string; clock?: RunClock | null; compacting?: boolean; manual?: boolean; compaction?: CompactionStatus | null }>()
const { t } = useI18n()
const { localTime } = useWorkspace()
const detailsOpen = ref(false)
useEscapeDismiss(detailsOpen)
watch(() => props.compaction?.sequence, () => { detailsOpen.value = false })
const terminal = computed(() => !props.compacting && ['failed', 'interrupted'].includes(props.compaction?.phase || ''))
const interrupted = computed(() => props.compaction?.phase === 'interrupted')
const label = computed(() => terminal.value ? t(interrupted.value ? 'contextInterrupted' : 'contextUnavailable')
  : props.compacting ? t('contextCompacting') : props.status)
const errorText = computed(() => props.compaction?.error || label.value)
const now = ref(Date.now())
const timer = setInterval(() => { now.value = Date.now() }, 1000)
onScopeDispose(() => clearInterval(timer))
function duration(start?: string) {
  const time = Date.parse(start || '')
  if (!Number.isFinite(time)) return ''
  const seconds = Math.max(0, Math.floor((now.value - time) / 1000))
  return seconds >= 3600 ? `${Math.floor(seconds / 3600)}:${String(Math.floor(seconds / 60) % 60).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
    : `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}
const elapsed = computed(() => duration(props.clock?.startedAt))
const activityAge = computed(() => duration(props.clock?.lastActivityAt))
</script>

<template>
  <section class="conversation-execution" :class="{ 'compaction-progress': compacting, 'compaction-terminal': terminal }" :aria-label="t('executionProcess')">
    <div class="conversation-execution-current">
      <IconPlayerStop v-if="terminal && interrupted" aria-hidden="true" /><IconAlertCircle v-else-if="terminal" aria-hidden="true" /><IconLoader2 v-else class="ui-icon-spin" aria-hidden="true" />
      <strong role="status" :title="label">{{ label }}</strong>
      <small v-if="elapsed" class="conversation-execution-elapsed">{{ t('traceElapsed', { elapsed }) }}</small>
      <Popover v-if="terminal && compaction" v-model:open="detailsOpen" trigger="click" placement="topLeft" root-class="compaction-details-popover" :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
        <template #content><div class="compaction-error-detail">
          <pre>{{ errorText }}</pre><MessageActions :text="errorText" />
          <FailureHelp :failure="{ kind: 'compaction', runId: compaction.runId, sequence: compaction.sequence, error: errorText }" />
        </div></template>
        <button type="button" class="compaction-details" :aria-label="t('contextDetails')" :aria-expanded="detailsOpen">{{ t('contextDetails') }}<IconChevronUp aria-hidden="true" /></button>
      </Popover>
    </div>
    <p v-if="compacting" class="compaction-description">{{ t(manual ? 'manualCompactionProgress' : 'autoCompactionProgress') }}</p>
    <p v-else-if="terminal" class="compaction-description">{{ t('compactionRetryHint') }}</p>
    <p v-if="clock?.lastActivityAt" class="trace-last-activity"><I18nT keypath="traceLastActivity" tag="span" scope="global"><template #activity><time :datetime="clock.lastActivityAt" :title="localTime(clock.lastActivityAt)">{{ localTime(clock.lastActivityAt) }}</time></template></I18nT><small v-if="activityAge">{{ t('traceActivityAge', { elapsed: activityAge }) }}</small></p>
  </section>
</template>

<style scoped>
.conversation-execution{flex:none;min-width:0;padding:.45rem .35rem;color:var(--muted);font-size:14px;line-height:1.5}
.conversation-execution-current{display:flex;align-items:baseline;gap:.5rem;min-width:0}.conversation-execution-current>svg{align-self:center;flex:none}.conversation-execution-current strong{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text-secondary);font-size:15px;font-weight:500}
.conversation-execution-elapsed{flex:none;margin-inline-start:auto;font-size:14px;font-variant-numeric:tabular-nums}
.trace-last-activity{display:flex;flex-wrap:wrap;gap:.25rem .6rem;margin:.2rem 0 0 1.4rem}.trace-last-activity small{font:inherit}
.compaction-terminal .conversation-execution-current>svg{color:var(--warning)}
.compaction-description{margin:.2rem 0 0 1.4rem;font-size:13px;overflow-wrap:anywhere}
.compaction-details{display:inline-flex;align-items:center;gap:.25rem;flex:none;margin-inline-start:auto;padding:.2rem .35rem;border:0;border-radius:6px;background:transparent;color:var(--muted);font:inherit;font-size:12px;cursor:pointer}
.compaction-details:hover{background:var(--hover);color:var(--text)}
.compaction-details:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:2px}
.compaction-error-detail{max-width:26rem;font-size:14px;line-height:1.6;overflow-wrap:anywhere}
.compaction-error-detail pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:13px}
@media(pointer:coarse){.compaction-details{min-height:44px}}
@media(max-height:520px){.conversation-execution{padding:.2rem .35rem}.trace-last-activity{font-size:12px;margin-top:0}.compaction-description{font-size:12px;margin-top:0}}
@media(prefers-reduced-motion:reduce){.ui-icon-spin{animation:none!important}}
</style>
<style>
.compaction-details-popover .ui-popover-inner{max-height:calc(100dvh - 2rem);overflow:auto;overscroll-behavior:contain}
</style>
