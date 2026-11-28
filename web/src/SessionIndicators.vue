<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { sessionListActivity, type Session } from '@vista/client'

const props = withDefaults(defineProps<{ session: Session; workerAvailable?: boolean; links?: boolean }>(), { links: true, workerAvailable: undefined })
const emit = defineEmits<{ open: [id: string] }>()
const { t } = useI18n({ useScope: 'global' })
const badges = computed(() => {
  const { session, workerAvailable, links } = props
  const state = sessionListActivity(session)
  const target = (id?: string) => links && session.branchGroup ? id : undefined
  const running = session.branchGroup || workerAvailable !== false ? state.executingId || state.runningId : undefined
  return [
    { key: 'unreadReply', kind: 'unread', show: !!state.unreadId, target: target(state.unreadId) },
    { key: 'awaitingAnswer', kind: 'waiting', show: !!state.awaitingId, target: target(state.awaitingId) },
    { key: 'queuePaused', kind: 'waiting', show: !!state.pausedId, target: target(state.pausedId) },
    { key: 'workerUnavailable', kind: 'unavailable', show: session.open && workerAvailable === false, target: undefined },
    { key: 'executionFailed', kind: 'failed', show: !!state.failedId, target: target(state.failedId) },
    { key: state.executingId ? 'executing' : 'backgroundRunning', kind: 'running', show: !!running, target: target(running) },
  ].filter(badge => badge.show)
})
function open(event: Event, target?: string) {
  if (!target) return
  event.stopPropagation()
  emit('open', target)
}
</script>

<template>
  <small v-if="badges.length" class="session-state">
    <component v-for="badge in badges" :key="badge.key" :is="badge.target ? 'button' : 'span'"
      :type="badge.target ? 'button' : undefined" class="session-indicator" :class="badge.kind"
      @click="open($event, badge.target)" @keydown="badge.target && $event.stopPropagation()">
      <span class="session-status-dot" aria-hidden="true"></span>{{ t(badge.key) }}
    </component>
  </small>
</template>

<style scoped>
button.session-indicator{font:inherit;padding:0 2px;min-height:24px;border:0;background:none;cursor:pointer;border-radius:4px}
button.session-indicator:focus-visible{outline:var(--focus-outline, 2px solid var(--accent))}
</style>
