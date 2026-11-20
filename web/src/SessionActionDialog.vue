<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Checkbox as VCheckbox, Modal as VModal } from './ui'
import type { SessionActionPreview } from '@vista/client'

const props = defineProps<{ action: SessionActionPreview | null; busy: boolean }>()
const emit = defineEmits<{ decision: [requestId: string, sessionIds: string[]] }>()
const { t } = useI18n()
const selectedIDs = ref(new Set<string>())
watch(() => props.action?.requestId, () => {
  selectedIDs.value = new Set(props.action?.targets.map(target => target.id))
}, { immediate: true })
const count = computed(() => selectedIDs.value.size)
const deleting = computed(() => props.action?.kind === 'delete')
function decide(accept: boolean) {
  if (props.busy || !props.action || accept && !count.value) return
  emit('decision', props.action.requestId, accept ? [...selectedIDs.value] : [])
}
</script>

<template>
  <v-modal :open="!!action" centered :z-index="1200" width="min(480px, calc(100vw - 2rem))"
    :title="deleting ? t('deleteSessionConfirmTitle') : t('closeSelectedSessionsConfirmTitle', { count })"
    :ok-text="t(deleting ? 'deleteSelected' : 'closeSelected')" :cancel-text="t('cancel')" :confirm-loading="busy"
    :ok-button-props="{ danger: deleting, disabled: busy || !count }" :cancel-button-props="{ disabled: busy }"
    :closable="!busy" :keyboard="!busy" :mask-closable="false" @ok="decide(true)" @cancel="decide(false)">
    <p v-if="deleting">{{ t('deleteSelectedSessionsConfirm', { count }) }} {{ t('agentDeleteSessionsHelp') }}</p>
    <p v-else>{{ t('agentCloseSessionsHelp') }}</p>
    <ul class="session-action-targets">
      <li v-for="target in action?.targets" :key="target.id">
        <v-checkbox :checked="selectedIDs.has(target.id)" :disabled="busy" @update:checked="$event ? selectedIDs.add(target.id) : selectedIDs.delete(target.id)">
          {{ target.title }}
        </v-checkbox>
      </li>
    </ul>
  </v-modal>
</template>

<style scoped>
.session-action-targets { display: grid; gap: .55rem; list-style: none; margin: 1rem 0 0; padding: .25rem; max-height: 35dvh; overflow: auto; overflow-wrap: anywhere; }
</style>
