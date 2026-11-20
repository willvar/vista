<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Dropdown as VDropdown } from './ui'
import { useWorkspace } from './composables/useWorkspace'
import type { ConversationBranch } from '@vista/client'

const props = defineProps<{ sessionId: string }>()
const { client, stateSync, router, busy } = useWorkspace()
const { t } = useI18n({ useScope: 'global' })
const branches = ref<ConversationBranch[]>([])
watch(() => props.sessionId, (id, _, onCleanup) => {
  const controller = new AbortController()
  branches.value = []
  let request = 0
  async function load() {
    const n = ++request
    try {
      const received = await client.request<ConversationBranch[]>(`/sessions/${id}/branches`, { signal: controller.signal })
      if (!controller.signal.aborted && n === request && Array.isArray(received)) branches.value = received
    } catch { /* The active branch is still usable without the picker. */ }
  }
  void load()
  const stop = stateSync.subscribe(['sessions'], async () => { await load() })
  onCleanup(() => { controller.abort(); stop() })
}, { immediate: true })
const label = (branch: ConversationBranch, index: number) => branch.parentId ? t('branchNumber', { number: index }) : t('originalBranch')
const currentLabel = computed(() => {
  const index = branches.value.findIndex(b => b.id === props.sessionId)
  return index >= 0 ? label(branches.value[index]!,index) : t('conversationBranches')
})
const menu = computed(() => ({ items: branches.value.map((b,i) => ({ key:b.id, label:label(b,i), disabled:!b.open || b.id===props.sessionId })),
  onClick: ({key}:{key:string}) => { void router.push(`/sessions/${key}`) } }))
</script>
<template>
  <v-dropdown v-if="branches.length > 1" :menu="menu">
    <v-button variant="text" :aria-label="t('conversationBranches')" :disabled="busy">{{ currentLabel }}</v-button>
  </v-dropdown>
</template>
