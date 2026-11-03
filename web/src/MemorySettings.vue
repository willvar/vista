<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as AEmpty, Alert as AAlert, Button as AButton, Modal as AModal, TextArea as ATextarea, type TextAreaRef } from 'antdv-next'
import { CloseOutlined, DeleteOutlined, EditOutlined, PlusOutlined, CommentOutlined, BookOutlined, RobotOutlined } from '@antdv-next/icons'
import ConfirmPopover from './ConfirmPopover.vue'
import ListPagination from './ListPagination.vue'
import { useWorkspace } from './composables/useWorkspace'
import { usePagedList } from './composables/usePagedList'
import { VistaAPIError } from './client/vista'
import type { MemoryPage, Memory } from './client/types'

const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const managerOpen = ref(false)
const page = ref(1)
const pageSize = ref(10)
const memoryPath = (current: number) => `/memories?page=${current}&pageSize=${pageSize.value}`
const resource = usePagedList<MemoryPage>({ client, sync: stateSync, topic: 'memories', page, path: memoryPath, enabled: () => managerOpen.value, message })
const { ready, loading } = resource
const operationError = ref('')
const error = computed(() => operationError.value || resource.error.value)
const snapshot = computed(() => resource.data.value ?? { memories: [], total: 0, maxCount: 128, maxBytes: 4096, maxTotalBytes: 32768 })
const busy = ref(false)
const editing = ref<Memory | 'new' | null>(null)
const deleting = ref('')
const deleteTarget = ref<Memory | null>(null)
const deleteError = ref('')
const draft = ref('')
const editor = ref<TextAreaRef>()
let disposed = false
const bytes = computed(() => new TextEncoder().encode(draft.value.trim()).length)
const canSave = computed(() => !!draft.value.trim() && bytes.value <= snapshot.value.maxBytes
  && (editing.value === 'new' || !!editing.value && draft.value.trim() !== editing.value.text))

function setEditor(value: unknown) { editor.value = value as TextAreaRef | undefined }
function preview(text: string) {
  const characters = Array.from(text.replace(/\s+/g, ' ').trim())
  return characters.length > 120 ? `${characters.slice(0, 120).join('')}…` : characters.join('')
}
function closeManager() { if (!busy.value) managerOpen.value = false }
function resetManager() {
  if (managerOpen.value) return
  editing.value = null
  draft.value = ''
  deleting.value = ''
  operationError.value = ''
  deleteError.value = ''
}
function updateDelete(memory: Memory, open: boolean) {
  deleting.value = open ? memory.id : ''
  deleteTarget.value = open ? { ...memory } : null
  deleteError.value = ''
}

function message(failure: unknown) {
  if (failure instanceof VistaAPIError) {
    const keys: Record<string, string> = {
      memory_invalid: 'memoryInvalid', memory_changed: 'memoryChanged',
      memory_exists: 'memoryExists', memory_limit: 'memoryLimit', memory_unavailable: 'memoryUnavailable',
    }
    if (keys[failure.code]) return t(keys[failure.code]!)
  }
  return failure instanceof Error ? failure.message : String(failure)
}

async function startEdit(memory: Memory | 'new') {
  deleting.value = ''
  deleteError.value = ''
  // Preserve the original target even if a push changes or deletes its row.
  editing.value = memory === 'new' ? memory : { ...memory }
  draft.value = memory === 'new' ? '' : memory.text
  operationError.value = ''
  await nextTick()
  editor.value?.focus()
}

async function change(action: 'add' | 'update' | 'remove', memory?: Memory) {
  if (busy.value) return
  busy.value = true
  operationError.value = ''
  deleteError.value = ''
  resource.cancel()
  const input = action === 'add' ? { action, text: draft.value.trim() }
    : { action, id: memory!.id, previousText: memory!.text, ...action === 'update' ? { text: draft.value.trim() } : {} }
  try {
    const targetPage = action === 'add' ? Math.ceil((snapshot.value.total + 1) / pageSize.value) : page.value
    const result = await client.request<MemoryPage>(memoryPath(targetPage), { method: 'POST', body: JSON.stringify(input) })
    if (!disposed) {
      resource.apply(result)
      editing.value = null
      draft.value = ''
      if (action === 'remove') deleting.value = ''
    }
  } catch (failure) {
    if (!disposed) {
      if (action === 'remove') deleteError.value = message(failure)
      else operationError.value = message(failure)
    }
  } finally {
    if (!disposed) busy.value = false
  }
}

function save() {
  if (!canSave.value || !editing.value) return
  void change(editing.value === 'new' ? 'add' : 'update', editing.value === 'new' ? undefined : editing.value)
}
function changePage(current: number, size: number) { pageSize.value = size; page.value = current; deleting.value = '' }
watch(managerOpen, open => { emit('modal-open', open); if (!open) deleting.value = '' }, { flush: 'sync' })
watch(() => snapshot.value.memories, items => { if (deleting.value && !items.some(item => item.id === deleting.value)) deleting.value = '' })
watch(deleting, value => { if (!value) deleteTarget.value = null }, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; emit('modal-open', false) })
</script>

<template>
  <section class="memory-settings general-setting" aria-labelledby="memory-heading">
    <div class="memory-heading">
      <h3 id="memory-heading">{{ t('memories') }}</h3>
      <a-button @click="managerOpen = true"><template #icon><BookOutlined aria-hidden="true" /></template>{{ t('manageMemories') }}</a-button>
    </div>
    <figure class="personalization-figure memory-figure" :aria-label="t('memoryFigureLabel')">
      <div class="memory-flow">
        <div class="personalization-figure-node memory-flow-past"><CommentOutlined aria-hidden="true" /><span>{{ t('memoryPastConversations') }}</span></div>
        <span class="personalization-figure-link memory-flow-retain" aria-hidden="true"></span>
        <div class="personalization-figure-node memory-flow-stored"><BookOutlined aria-hidden="true" /><span>{{ t('memories') }}</span><small>{{ t('memoryDetails') }}</small></div>
        <div class="personalization-figure-node memory-flow-current"><EditOutlined aria-hidden="true" /><span>{{ t('memoryCurrentRequest') }}</span></div>
        <svg class="memory-flow-join" viewBox="0 0 24 100" preserveAspectRatio="none" aria-hidden="true"><path d="M0 25H10V75H0 M10 50H24" /></svg>
        <span class="personalization-figure-link memory-flow-reuse" aria-hidden="true"></span>
        <span class="personalization-figure-link memory-flow-context" aria-hidden="true"></span>
        <div class="personalization-figure-node memory-flow-task"><RobotOutlined aria-hidden="true" /><span>{{ t('memoryCurrentTask') }}</span></div>
      </div>
    </figure>
  </section>

  <a-modal :open="managerOpen" centered :z-index="1200" width="min(760px, calc(100vw - 2rem))"
    :footer="editing || !snapshot.total ? null : undefined" :closable="false" :keyboard="!busy && !deleting" :mask-closable="!busy && !deleting"
    destroy-on-hidden :after-close="resetManager" :styles="{ body: { maxHeight: editing || !snapshot.total ? 'calc(100dvh - 10rem)' : 'calc(100dvh - 15rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeManager">
    <template #title>
      <div class="memory-modal-header">
        <span class="memory-modal-heading">{{ t('manageMemories') }}<small v-if="ready" class="memory-count">{{ snapshot.total }}</small></span>
        <div class="settings-header-actions">
          <template v-if="!editing">
            <a-button type="text" class="settings-header-button" :aria-label="t('addMemory')" :title="t('addMemory')" :disabled="!ready || busy || snapshot.total >= snapshot.maxCount" @click="startEdit('new')"><template #icon><PlusOutlined aria-hidden="true" /></template></a-button>
          </template>
          <a-button type="text" class="settings-header-button" :aria-label="t('close')" :title="t('close')" :disabled="busy" @click="closeManager"><template #icon><CloseOutlined aria-hidden="true" /></template></a-button>
        </div>
      </div>
    </template>
    <template #footer><ListPagination v-if="!editing" :page="page" :page-size="pageSize" :total="snapshot.total" :disabled="busy" @change="changePage" /></template>
    <div class="memory-manager" :aria-busy="loading || busy">
    <a-alert v-if="error" type="error" show-icon role="alert" :message="error" />
    <a-button v-if="resource.error.value && !editing" :disabled="busy || loading" @click="resource.refresh()">{{ t('retry') }}</a-button>
    <p v-if="loading && !ready" role="status">{{ t('memoriesLoading') }}</p>
    <template v-if="ready">
      <a-empty v-if="!snapshot.memories.length && !editing" class="vista-empty-state memory-empty" :description="t('memoriesEmpty')" role="status" />
      <ul v-if="!editing && snapshot.memories.length" class="memory-list">
        <li v-for="memory in snapshot.memories" :key="memory.id">
            <button type="button" class="memory-preview" :disabled="busy || !!editing" @click="startEdit(memory)"><span class="memory-text">{{ preview(memory.text) }}</span></button>
            <div class="memory-row-actions">
              <a-button type="text" :aria-label="t('editMemory', { text: preview(memory.text) })" :title="t('edit')" :disabled="busy || !!editing" @click="startEdit(memory)"><template #icon><EditOutlined /></template></a-button>
              <ConfirmPopover :show="deleting === memory.id" :message="t('removeMemory', { text: deleteTarget?.id === memory.id ? deleteTarget.text : memory.text })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" :error="deleteError" :z-index="1300" popover-class="memory-delete-popover" @update:show="updateDelete(memory, $event)" @confirm="change('remove', deleteTarget || memory)">
                <template #trigger><a-button type="text" danger :aria-label="t('removeMemory', { text: preview(memory.text) })" :title="t('delete')" :disabled="busy || !!editing"><template #icon><DeleteOutlined /></template></a-button></template>
              </ConfirmPopover>
            </div>
        </li>
      </ul>
      <form v-if="editing" class="memory-editor" @submit.prevent="save">
        <a-textarea :ref="setEditor" v-model:value="draft" class="memory-editor-input" :aria-label="t('memoryText')" :placeholder="t('memoryPlaceholder')" :auto-size="false" :disabled="busy" :status="bytes > snapshot.maxBytes ? 'error' : undefined" />
        <div class="memory-editor-actions"><small>{{ t('instructionBytes', { current: bytes, max: snapshot.maxBytes }) }}</small><a-button :disabled="busy" @click="editing = null; operationError = ''">{{ t('cancel') }}</a-button><a-button type="primary" html-type="submit" :loading="busy" :disabled="!canSave || busy">{{ t('saveChanges') }}</a-button></div>
      </form>
    </template>
    </div>
  </a-modal>
</template>

<style scoped>
.memory-settings,.memory-manager{min-width:0}.memory-heading,.memory-row-actions,.memory-editor-actions,.memory-modal-heading,.memory-modal-header{display:flex;align-items:center;gap:.5rem}.memory-heading{justify-content:space-between;flex-wrap:wrap}.memory-heading h3{font-size:16px;margin:0}.memory-count{font-weight:400;color:var(--text-secondary)}.memory-modal-header{justify-content:space-between}.memory-modal-heading{min-width:0}.memory-list{list-style:none;margin:.5rem 0 0;padding:0}.memory-list li{display:flex;align-items:flex-start;gap:.75rem;padding:.8rem 0;border-bottom:1px solid var(--border)}.memory-list li:last-child{border-bottom:0}.memory-preview{flex:1;min-width:0;text-align:start;border:0;background:transparent;color:var(--text);font:inherit;padding:.3rem;border-radius:6px;cursor:pointer}.memory-preview:disabled{cursor:default}.memory-preview:hover:not(:disabled){background:var(--surface-subtle)}.memory-preview:focus-visible{outline:2px solid var(--accent);outline-offset:2px}.memory-text{display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2;overflow:hidden;overflow-wrap:anywhere;line-height:1.6}.memory-row-actions{flex:none}.memory-editor{width:100%;min-width:0;margin-top:.75rem}.memory-editor-input{height:clamp(80px,40dvh,420px);max-height:calc(100dvh - 15rem);min-height:80px;resize:vertical;line-height:1.65}.memory-editor-actions{justify-content:flex-end;flex-wrap:wrap;margin-top:.6rem}.memory-editor-actions small{margin-right:auto;color:var(--text-secondary)}.memory-manager>.ant-alert{margin-top:.75rem}
</style>
