<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Button, TextArea } from 'antdv-next'
import { DeleteOutlined, EditOutlined, RollbackOutlined, ArrowUpOutlined, PlayCircleOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import type { QueuedInput } from './client/types'
const props = defineProps<{
  items: QueuedInput[]; paused: boolean; busy: Record<string, boolean>
  mutate: (input: QueuedInput, action: 'edit' | 'delete' | 'withdraw', patch?: { content?: string; mode?: 'now' | 'later' }) => Promise<boolean>
}>()
const emit = defineEmits<{ resume: [] }>()
const { t } = useI18n()
const editing = ref('')
const content = ref('')
const allPaused = computed(() => props.items.every(item => item.status === 'paused'))
watch(() => props.items, items => { if (!items.some(item => item.id === editing.value)) editing.value = '' })
async function save(item: QueuedInput) {
  if (!content.value.trim() && !item.imageCount) return
  if (await props.mutate(item, 'edit', { content: content.value })) editing.value = ''
}
</script>
<template>
  <section v-if="items.length" class="pending-inputs" :aria-label="t('pendingInput')">
    <div class="pending-inputs-heading"><span>{{ allPaused ? t('pendingPaused') : t('pendingInput') }} <small>{{ items.length }}</small></span><Button v-if="paused" :aria-label="t('resumePending')" size="small" type="text" @click="emit('resume')"><PlayCircleOutlined />{{ t('resumePending') }}</Button></div>
    <div class="pending-input-list">
      <div v-for="item in items" :key="item.id" class="pending-input" :data-input-id="item.id">
        <div class="pending-input-label"><span>{{ item.status === 'paused' ? t('pendingPausedItem') : item.mode === 'now' ? t('waitingToSteer') : t('waitingToQueue') }}</span><small v-if="item.imageCount">{{ t('imageCount', { count: item.imageCount }) }}</small></div>
        <form v-if="editing === item.id" class="pending-input-edit" @submit.prevent="save(item)">
          <TextArea v-model:value="content" :auto-size="{ minRows: 1, maxRows: 4 }" :aria-label="t('editPending')" :disabled="busy[item.id]" />
          <div class="actions"><Button size="small" html-type="submit" :disabled="busy[item.id] || (!content.trim() && !item.imageCount)">{{ t('saveChanges') }}</Button><Button size="small" type="text" @click="editing = ''">{{ t('cancel') }}</Button></div>
        </form>
        <template v-else>
          <p>{{ item.content || t('attachedImage') }}</p>
          <div class="pending-input-actions">
            <Button size="small" type="text" :title="t('editPending')" :aria-label="t('editPending')" :disabled="busy[item.id] || item.status === 'delivering'" @click="editing = item.id; content = item.content"><EditOutlined /></Button>
            <Button size="small" type="text" :title="t('withdrawPending')" :aria-label="t('withdrawPending')" :disabled="busy[item.id] || item.status === 'delivering'" @click="mutate(item, 'withdraw')"><RollbackOutlined /></Button>
            <Button v-if="item.mode === 'later'" size="small" type="text" :title="t('promotePending')" :aria-label="t('promotePending')" :disabled="busy[item.id] || item.status === 'delivering'" @click="mutate(item, 'edit', { mode: 'now' })"><ArrowUpOutlined /></Button>
            <Button size="small" type="text" :title="t('deletePending')" :aria-label="t('deletePending')" :disabled="busy[item.id] || item.status === 'delivering'" @click="mutate(item, 'delete')"><DeleteOutlined /></Button>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
