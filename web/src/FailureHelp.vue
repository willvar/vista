<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button as VButton } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import type { FailureContext } from './state/failureAnalysis'

defineProps<{ failure: FailureContext }>()
const { t } = useI18n({ useScope: 'global' })
const { busy, conversation } = useWorkspace()
const { analyzingFailure, canAnalyzeFailure, analyzeFailure, canRetryFailure, retryFailure } = conversation
</script>

<template>
  <p v-if="canRetryFailure(failure)" class="run-failure-retry"><v-button variant="link" @click="retryFailure(failure)">{{ t('retryTask') }}</v-button></p>
  <i18n-t keypath="runFailureHelp" tag="p" class="run-failure-help" scope="global">
    <template #analyze>
      <v-button variant="link" :loading="analyzingFailure === failure.runId"
        :disabled="busy || !!analyzingFailure || !canAnalyzeFailure" @click="analyzeFailure(failure)">{{ t('analyzeFailure') }}</v-button>
    </template>
  </i18n-t>
</template>
