<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { IconChevronLeft, IconChevronRight } from '@tabler/icons-vue'
import type { BranchNavigation, BranchTarget } from './state/conversationBranches'

defineProps<{ navigation: BranchNavigation; busy: boolean }>()
const emit = defineEmits<{ select: [target: BranchTarget] }>()
const { t } = useI18n()
</script>

<template>
  <div class="message-actions branch-navigation" role="group" :aria-label="t('conversationBranches')">
    <button type="button" class="reply-action" :aria-label="t('previousBranch')" :title="t('previousBranch')"
      :disabled="busy || !navigation.previous" @click="navigation.previous && emit('select', navigation.previous)"><IconChevronLeft aria-hidden="true" /></button>
    <span>{{ navigation.position }} / {{ navigation.total }}</span>
    <button type="button" class="reply-action" :aria-label="t('nextBranch')" :title="t('nextBranch')"
      :disabled="busy || !navigation.next" @click="navigation.next && emit('select', navigation.next)"><IconChevronRight aria-hidden="true" /></button>
  </div>
</template>

<style scoped>
.branch-navigation{gap:0;color:var(--muted);font-size:.78rem;font-variant-numeric:tabular-nums;white-space:nowrap}
</style>
