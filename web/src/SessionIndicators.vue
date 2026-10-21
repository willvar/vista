<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { Session } from './client/types'
import { sessionHasUnread } from './state/workspace'

defineProps<{ session: Session; workerAvailable?: boolean }>()
const { t } = useI18n({ useScope: 'global' })
</script>

<template>
  <small v-if="sessionHasUnread(session) || session.pendingReply || session.inputQueuePaused && session.pendingInputCount
    || session.open && workerAvailable === false || session.latestRunStatus === 'failed' || session.running" class="session-state">
    <span v-if="sessionHasUnread(session)" class="session-indicator unread"><span class="session-status-dot"></span>{{ t('unreadReply') }}</span>
    <span v-if="session.pendingReply" class="session-indicator waiting"><span class="session-status-dot"></span>{{ t('awaitingAnswer') }}</span>
    <span v-if="session.inputQueuePaused && session.pendingInputCount" class="session-indicator waiting"><span class="session-status-dot"></span>{{ t('queuePaused') }}</span>
    <span v-if="session.open && workerAvailable === false" class="session-indicator unavailable"><span class="session-status-dot"></span>{{ t('workerUnavailable') }}</span>
    <span v-if="session.latestRunStatus === 'failed'" class="session-indicator failed"><span class="session-status-dot"></span>{{ t('executionFailed') }}</span>
    <span v-if="session.running && workerAvailable !== false" class="session-indicator running"><span class="session-status-dot"></span>{{ t(session.executing ? 'executing' : 'backgroundRunning') }}</span>
  </small>
</template>
