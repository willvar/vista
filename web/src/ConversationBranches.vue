<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Dropdown as VDropdown } from './ui'
import type { ConversationBranch } from '@vista/client'

const props = defineProps<{ sessionId: string; branches: readonly ConversationBranch[]; busy: boolean }>()
const emit = defineEmits<{ select: [id: string] }>()
const { t } = useI18n({ useScope: 'global' })
const label = (branch: ConversationBranch, index: number) => branch.parentId ? t('branchNumber', { number: index }) : t('originalBranch')
const currentLabel = computed(() => {
  const index = props.branches.findIndex(b => b.id === props.sessionId)
  return index >= 0 ? label(props.branches[index]!,index) : t('conversationBranches')
})
const menu = computed(() => ({ items: props.branches.map((b,i) => ({ key:b.id, label:label(b,i), disabled:!b.open || b.id===props.sessionId })),
  onClick: ({key}:{key:string}) => emit('select', key) }))
</script>
<template>
  <v-dropdown v-if="branches.length > 1" :menu="menu">
    <v-button variant="text" :aria-label="t('conversationBranches')" :disabled="busy">{{ currentLabel }}</v-button>
  </v-dropdown>
</template>
