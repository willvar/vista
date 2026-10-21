<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, TextArea as ATextarea, type TextAreaRef } from 'antdv-next'
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined, CommentOutlined, BookOutlined, RobotOutlined } from '@antdv-next/icons'
import { useWorkspace } from './composables/useWorkspace'
import { VistaAPIError } from './client/vista'
import type { Memories, Memory } from './client/types'

const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const snapshot = ref<Memories>({ memories: [], maxCount: 128, maxBytes: 4096, maxTotalBytes: 32768 })
const ready = ref(false)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const editing = ref<Memory | 'new' | null>(null)
const draft = ref('')
const editor = ref<TextAreaRef>()
let disposed = false
let generation = 0
let readController: AbortController | undefined
const bytes = computed(() => new TextEncoder().encode(draft.value.trim()).length)
const canSave = computed(() => !!draft.value.trim() && bytes.value <= snapshot.value.maxBytes
  && (editing.value === 'new' || !!editing.value && draft.value.trim() !== editing.value.text))

function setEditor(value: unknown) { editor.value = value as TextAreaRef | undefined }

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

async function load() {
  if (busy.value) return
  readController?.abort()
  const controller = readController = new AbortController()
  const version = ++generation
  loading.value = true
  error.value = ''
  try {
    const result = await client.request<Memories>('/memories', { signal: controller.signal })
    if (!disposed && generation === version) { snapshot.value = result; ready.value = true }
  } catch (failure) {
    if (!disposed && generation === version && !controller.signal.aborted) error.value = message(failure)
  } finally {
    if (!disposed && generation === version) loading.value = false
  }
}

async function startEdit(memory: Memory | 'new') {
  editing.value = memory === 'new' ? memory : { ...memory }
  draft.value = memory === 'new' ? '' : memory.text
  error.value = ''
  await nextTick()
  editor.value?.focus()
}

async function change(action: 'add' | 'update' | 'remove', memory?: Memory) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  readController?.abort()
  const version = ++generation
  loading.value = false
  const input = action === 'add' ? { action, text: draft.value.trim() }
    : { action, id: memory!.id, previousText: memory!.text, ...action === 'update' ? { text: draft.value.trim() } : {} }
  try {
    const result = await client.request<Memories>('/memories', { method: 'POST', body: JSON.stringify(input) })
    if (!disposed && generation === version) {
      snapshot.value = result
      editing.value = null
      draft.value = ''
    }
  } catch (failure) {
    if (!disposed && generation === version) error.value = message(failure)
  } finally {
    if (!disposed && generation === version) busy.value = false
  }
}

function save() {
  if (!canSave.value || !editing.value) return
  void change(editing.value === 'new' ? 'add' : 'update', editing.value === 'new' ? undefined : editing.value)
}
onMounted(load)
onBeforeUnmount(() => { disposed = true; generation++; readController?.abort() })
</script>

<template>
  <section class="memory-settings general-setting" aria-labelledby="memory-heading" :aria-busy="loading || busy">
    <div class="memory-heading">
      <h3 id="memory-heading">{{ t('memories') }}<small v-if="ready" aria-hidden="true">{{ snapshot.memories.length }}</small></h3>
      <div class="memory-actions">
        <a-button type="text" :aria-label="t('refreshMemories')" :title="t('refreshMemories')" :loading="loading" :disabled="busy || !!editing" @click="load"><template #icon><ReloadOutlined aria-hidden="true" /></template></a-button>
        <a-button :disabled="!ready || busy || !!editing || snapshot.memories.length >= snapshot.maxCount" @click="startEdit('new')"><template #icon><PlusOutlined aria-hidden="true" /></template>{{ t('addMemory') }}</a-button>
      </div>
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
    <a-alert v-if="error" type="error" show-icon role="alert" :message="error" />
    <p v-if="loading && !ready" role="status">{{ t('memoriesLoading') }}</p>
    <template v-if="ready">
      <p v-if="!snapshot.memories.length && !editing" class="memory-empty">{{ t('memoriesEmpty') }}</p>
      <ul v-if="snapshot.memories.length" class="memory-list">
        <li v-for="memory in snapshot.memories" :key="memory.id">
          <template v-if="editing && editing !== 'new' && editing.id === memory.id">
            <form class="memory-editor" @submit.prevent="save">
              <a-textarea :ref="setEditor" v-model:value="draft" :aria-label="t('memoryText')" :auto-size="{ minRows: 2, maxRows: 8 }" :disabled="busy" :status="bytes > snapshot.maxBytes ? 'error' : undefined" />
              <div class="memory-editor-actions"><small>{{ t('instructionBytes', { current: bytes, max: snapshot.maxBytes }) }}</small><a-button :disabled="busy" @click="editing = null">{{ t('cancel') }}</a-button><a-button type="primary" html-type="submit" :loading="busy" :disabled="!canSave || busy">{{ t('saveChanges') }}</a-button></div>
            </form>
          </template>
          <template v-else>
            <p class="memory-text">{{ memory.text }}</p>
            <div class="memory-row-actions"><a-button type="text" :aria-label="t('editMemory', { text: memory.text })" :title="t('edit')" :disabled="busy || !!editing" @click="startEdit(memory)"><template #icon><EditOutlined /></template></a-button><a-button type="text" danger :aria-label="t('removeMemory', { text: memory.text })" :title="t('delete')" :disabled="busy || !!editing" @click="change('remove', memory)"><template #icon><DeleteOutlined /></template></a-button></div>
          </template>
        </li>
      </ul>
      <form v-if="editing === 'new'" class="memory-editor" @submit.prevent="save">
        <a-textarea :ref="setEditor" v-model:value="draft" :aria-label="t('memoryText')" :placeholder="t('memoryPlaceholder')" :auto-size="{ minRows: 2, maxRows: 8 }" :disabled="busy" :status="bytes > snapshot.maxBytes ? 'error' : undefined" />
        <div class="memory-editor-actions"><small>{{ t('instructionBytes', { current: bytes, max: snapshot.maxBytes }) }}</small><a-button :disabled="busy" @click="editing = null">{{ t('cancel') }}</a-button><a-button type="primary" html-type="submit" :loading="busy" :disabled="!canSave || busy">{{ t('saveChanges') }}</a-button></div>
      </form>
    </template>
  </section>
</template>

<style scoped>
.memory-settings{min-width:0}.memory-heading,.memory-actions,.memory-row-actions,.memory-editor-actions{display:flex;align-items:center;gap:.5rem}.memory-heading{justify-content:space-between;flex-wrap:wrap}.memory-heading h3{display:flex;align-items:center;gap:.5rem;font-size:16px;margin:0}.memory-heading h3 small{font-weight:400;color:var(--text-secondary)}.memory-list{list-style:none;margin:1rem 0 0;padding:0}.memory-list li{display:flex;align-items:flex-start;gap:.75rem;padding:.8rem 0;border-bottom:1px solid var(--border)}.memory-list li:last-child{border-bottom:0}.memory-list .memory-text{flex:1;min-width:0;white-space:pre-wrap;overflow-wrap:anywhere;margin:.3rem 0;color:var(--text)}.memory-row-actions{flex:none}.memory-empty{color:var(--text-secondary)}.memory-editor{width:100%;margin-top:.75rem}.memory-editor-actions{justify-content:flex-end;flex-wrap:wrap;margin-top:.6rem}.memory-editor-actions small{margin-right:auto;color:var(--text-secondary)}.memory-settings>.ant-alert{margin-top:.75rem}
</style>
