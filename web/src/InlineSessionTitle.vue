<script setup lang="ts">
import { nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Input as VInput } from './ui'
import type { Session } from './client/types'
import { useWorkspace } from './composables/useWorkspace'
import { isPlainEscape } from './composables/useEscape'

const props = defineProps<{ session: Session }>()
const editing = defineModel<boolean>('editing', { default: false })
const { t } = useI18n({ useScope: 'global' })
const { displaySessionTitle, renameTitle } = useWorkspace()
const input = ref<InstanceType<typeof VInput>>()
const draft = ref('')
const saving = ref(false)
let original = ''
let composing = false
let compositionEndedAt = -Infinity
let disposed = false
onUnmounted(() => { disposed = true })

watch(editing, async active => {
  if (!active) return
  original = displaySessionTitle(props.session)
  draft.value = original
  await nextTick()
  if (editing.value) { input.value?.focus(); input.value?.input?.select() }
}, { immediate: true })

async function save() {
  if (!editing.value || saving.value) return
  const title = draft.value.trim()
  if (!title || title === original) { editing.value = false; return }
  saving.value = true
  try { if (await renameTitle(props.session, title) && !disposed) editing.value = false }
  finally { saving.value = false }
}
function keydown(event: KeyboardEvent) {
  event.stopPropagation()
  if (composing || event.isComposing || event.keyCode === 229 || performance.now() - compositionEndedAt < 200) return
  if (isPlainEscape(event)) { event.preventDefault(); if (!saving.value) editing.value = false }
  else if (event.key === 'Enter') { event.preventDefault(); (event.target as HTMLInputElement).blur() }
}
function compositionStart() { composing = true }
function compositionEnd() { composing = false; compositionEndedAt = performance.now() }
</script>

<template>
  <div class="inline-session-title" :class="{ 'session-rename': editing }">
    <v-input v-if="editing" ref="input" v-model:value="draft" :maxlength="200" :readonly="saving"
      :aria-label="t('sessionTitle')" :aria-busy="saving" @blur="save" @click.stop @keydown="keydown"
      @compositionstart="compositionStart" @compositionend="compositionEnd" />
    <slot v-else />
  </div>
</template>
