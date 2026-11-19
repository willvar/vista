<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconAdjustmentsHorizontal } from '@tabler/icons-vue'
import { Button as VButton, Popover as VPopover, Select as VSelect } from './ui'
import { useWorkspace } from './composables/useWorkspace'

const { t } = useI18n({ useScope: 'global' })
const { conversation, needsProvider, senderDisabled, route, mobileSessionsOpen } = useWorkspace()
const { selectedModel, selectedReasoning, reasoningChoices, selectedProcessingTier, processingTiers, chooseProcessingTier } = conversation
const open = ref(false)
const effortID = useId()
const tierID = useId()
const hintID = useId()
const badgeID = useId()
const hasReasoningChoices = computed(() => reasoningChoices.value.length > 1)
const hasProcessingTier = computed(() => !!(processingTiers.value.length || selectedProcessingTier.value))
const hasResponseOptions = computed(() => !needsProvider.value && (hasReasoningChoices.value || hasProcessingTier.value))
const tierIsAvailable = computed(() => processingTiers.value.includes(selectedProcessingTier.value))
const effortOptions = computed(() => [
  ...reasoningChoices.value.filter(option => option.value === 'none'),
  ...reasoningChoices.value.filter(option => option.value !== 'none'),
])
const tierOptions = computed(() => [{ value: '', label: t('default') },
  ...processingTiers.value.map(value => ({ value, label: t(`processingTier_${value}`) })),
  ...(selectedProcessingTier.value && !tierIsAvailable.value
    ? [{ value: selectedProcessingTier.value, label: t('processingTierUnavailable'), disabled: true }] : [])])
const tierBadge = computed(() => tierIsAvailable.value && selectedProcessingTier.value && selectedProcessingTier.value !== 'standard'
  ? t(`processingTier_${selectedProcessingTier.value}`) : '')
watch([() => route.fullPath, mobileSessionsOpen, selectedModel, senderDisabled, hasResponseOptions], () => { open.value = false })
</script>

<template>
  <v-popover v-if="hasResponseOptions" v-model:open="open" placement="bottomRight"
    :aria-label="t('responseOptions')" :styles="{ root: { width: 'min(20rem, calc(100vw - 24px))' } }">
    <template #content>
      <div class="response-options-panel">
        <strong>{{ t('responseOptions') }}</strong>
        <div v-if="hasReasoningChoices" class="response-options-row">
          <label :for="effortID">{{ t('reasoningEffort') }}</label>
          <v-select :id="effortID" :value="selectedReasoning" :options="effortOptions"
            :aria-label="t('reasoningEffort')" :disabled="senderDisabled"
            @update:value="selectedReasoning = String($event ?? '')" />
        </div>
        <div v-if="hasProcessingTier" class="response-options-tier">
          <div class="response-options-row">
            <label :for="tierID">{{ t('processingTierLabel') }}</label>
            <v-select :id="tierID" :value="selectedProcessingTier" :options="tierOptions"
              :aria-label="t('processingTierLabel')" :aria-describedby="hintID"
              :disabled="senderDisabled" @update:value="chooseProcessingTier(String($event ?? ''))" />
          </div>
          <p :id="hintID">{{ t('processingTierHint') }}</p>
        </div>
      </div>
    </template>
    <v-button variant="text" class="icon-button header-status-trigger response-options-trigger" :class="{ 'has-tier': tierBadge }"
      :aria-label="t('responseOptions')" :aria-describedby="tierBadge ? badgeID : undefined" :title="t('responseOptions')" :disabled="senderDisabled">
      <IconAdjustmentsHorizontal aria-hidden="true" /><span v-if="tierBadge" :id="badgeID" class="response-options-badge" :title="tierBadge">{{ tierBadge }}</span>
    </v-button>
  </v-popover>
</template>

<style scoped>
.response-options-trigger.ui-btn.has-tier{width:auto;gap:.25rem;padding-inline:.35rem}
.response-options-badge{max-inline-size:6rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--accent);font-size:.75rem}
.response-options-panel{display:grid;gap:.55rem}
.response-options-row{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));align-items:center;gap:.65rem}
.response-options-row label{overflow-wrap:anywhere}
.response-options-row .ui-select{width:100%}
.response-options-tier p{margin:.35rem 0 0;color:var(--muted);font-size:.8rem;overflow-wrap:anywhere}
</style>
