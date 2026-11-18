<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Tooltip } from './ui'
import { IconPencil, IconArrowBackUp } from '@tabler/icons-vue'
import CopyAction from './ui/CopyAction.vue'
const props = defineProps<{ text: string; fullText?: () => Promise<string>; allowMutation?: boolean; mutationDisabled?: boolean }>()
const emit = defineEmits<{ edit: []; withdraw: [] }>()
const { t } = useI18n()
const items = computed(() => [{ key: 'copy', label: t('copy') }, ...(props.allowMutation ? [{ key: 'edit', label: t('editSentMessage') }, { key: 'withdraw', label: t('withdrawMessage') }] : [])])
</script>

<template>
  <div class="message-actions">
    <template v-for="item in items" :key="item.key">
      <CopyAction v-if="item.key === 'copy'" :text="props.text" :full-text="fullText" />
      <Tooltip v-else-if="item.key === 'edit' || item.key === 'withdraw'" :title="item.label">
        <button type="button" class="reply-action" :aria-label="item.label" :disabled="mutationDisabled" @click="item.key === 'edit' ? emit('edit') : emit('withdraw')"><IconPencil v-if="item.key === 'edit'" /><IconArrowBackUp v-else /></button>
      </Tooltip>
    </template>
  </div>
</template>
