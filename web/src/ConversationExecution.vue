<script setup lang="ts">
import { computed, onScopeDispose, ref } from 'vue'
import { Translation as I18nT, useI18n } from 'vue-i18n'
import { LoadingOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import type { RunClock } from './client/types'

const props = defineProps<{ status: string; clock?: RunClock | null }>()
const { t } = useI18n()
const { localTime } = useWorkspace()
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
  <section class="conversation-execution" :aria-label="t('executionProcess')">
    <div class="conversation-execution-current"><LoadingOutlined class="ui-icon-spin" aria-hidden="true" /><strong role="status" :title="status">{{ status }}</strong><small v-if="elapsed" class="conversation-execution-elapsed">{{ t('traceElapsed', { elapsed }) }}</small></div>
    <p v-if="clock?.lastActivityAt" class="trace-last-activity"><I18nT keypath="traceLastActivity" tag="span" scope="global"><template #activity><time :datetime="clock.lastActivityAt" :title="localTime(clock.lastActivityAt)">{{ localTime(clock.lastActivityAt) }}</time></template></I18nT><small v-if="activityAge">{{ t('traceActivityAge', { elapsed: activityAge }) }}</small></p>
  </section>
</template>

<style scoped>
.conversation-execution{flex:none;min-width:0;padding:.45rem .35rem;color:var(--muted);font-size:14px;line-height:1.5}
.conversation-execution-current{display:flex;align-items:baseline;gap:.5rem;min-width:0}.conversation-execution-current>svg{align-self:center;flex:none}.conversation-execution-current strong{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text-secondary);font-size:15px;font-weight:500}
.conversation-execution-elapsed{flex:none;margin-inline-start:auto;font-size:14px;font-variant-numeric:tabular-nums}
.trace-last-activity{display:flex;flex-wrap:wrap;gap:.25rem .6rem;margin:.2rem 0 0 1.4rem}.trace-last-activity small{font:inherit}
@media(max-height:520px){.conversation-execution{padding:.2rem .35rem}.trace-last-activity{font-size:12px;margin-top:0}}
@media(prefers-reduced-motion:reduce){.ui-icon-spin{animation:none!important}}
</style>
