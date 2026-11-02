<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button as AButton } from 'antdv-next'
import { useWorkspace } from './composables/useWorkspace'
import type { FailureContext } from './state/failureAnalysis'

defineProps<{ failure: FailureContext }>()
const { t } = useI18n({ useScope: 'global' })
const { busy, conversation } = useWorkspace()
const { analyzingFailure, canAnalyzeFailure, analyzeFailure } = conversation
</script>

<template>
  <i18n-t keypath="runFailureHelp" tag="p" class="run-failure-help" scope="global">
    <template #analyze>
      <a-button type="link" size="small" :loading="analyzingFailure === failure.runId"
        :disabled="busy || !!analyzingFailure || !canAnalyzeFailure" @click="analyzeFailure(failure)">{{ t('analyzeFailure') }}</a-button>
    </template>
  </i18n-t>
</template>
