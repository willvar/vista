<script setup lang="ts">
import { onScopeDispose, ref } from 'vue'
import { IconHistory } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import type { HistoryVersion, HistoryVersionList, HistoryVersionPreview } from '@vista/client'
import { Alert, Button, Modal } from './ui'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ sessionId: string; canRestore: boolean }>()
const { t, locale } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const shown = ref(false)
const busy = ref(false)
const error = ref('')
const items = ref<HistoryVersion[]>([])
const next = ref<number>()
const preview = ref<(HistoryVersionPreview & { version: number })>()
const reasons = { edit: 'historyFromEdit', withdraw: 'historyFromWithdrawal', restore: 'historyFromRestore' } as const
let scope: AbortController | undefined
onScopeDispose(() => scope?.abort())

function close() {
  if (busy.value) return
  scope?.abort()
  scope = undefined
  shown.value = false
}

function request<T>(suffix: string, task: (result: T) => void, body?: object) {
  const owner = scope
  if (!owner || busy.value) return
  error.value = ''
  busy.value = true
  void client.request<T>(`/sessions/${encodeURIComponent(props.sessionId)}/history-versions${suffix}`, {
    signal: owner.signal, ...(body ? { method: 'POST', body: JSON.stringify(body) } : {}),
  }).then(value => { if (scope === owner && !owner.signal.aborted) task(value) })
    .catch(failure => { if (scope === owner && !owner.signal.aborted) error.value = String(failure) })
    .finally(() => { if (scope === owner) busy.value = false })
}

function list(before?: number) {
  request<HistoryVersionList>(before ? `?before=${before}` : '', page => {
    items.value = before ? [...items.value, ...page.items] : page.items
    next.value = page.nextBefore
  })
}
function open() {
  scope?.abort()
  scope = new AbortController()
  preview.value = undefined
  items.value = []
  next.value = undefined
  shown.value = true
  list()
}
function inspect(version: number) {
  request<HistoryVersionPreview>(`/${version}`, value => { preview.value = { ...value, version } })
}
function restore() {
  const selected = preview.value
  if (!selected || !props.canRestore) return
  request<void>(`/${selected.version}`, () => { busy.value = false; close() }, { throughSequence: selected.throughSequence })
}
function time(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
</script>

<template>
  <Button variant="text" @click="open">
    <template #icon><IconHistory aria-hidden="true" /></template>
    {{ t('restoreHistory') }}
  </Button>
  <Modal :open="shown" centered :width="520" :title="t('restoreHistory')" :footer="null" :closable="!busy" :mask-closable="!busy" :keyboard="!busy" @cancel="close">
    <Alert v-if="error" type="error" :message="error" />
    <div v-if="preview" class="history-versions">
      <p>{{ t('restoreHistoryHint') }}</p>
      <ul v-if="preview.messages?.length" class="history-preview" :aria-label="t('preview')">
        <li v-for="(message, i) in preview.messages" :key="i"><strong>{{ message.role === 'user' ? t('historyUser') : t('historyReply') }}</strong><p>{{ message.content || t('historyNoText') }}</p></li>
      </ul>
      <p v-else>{{ t('historyNoText') }}</p>
      <div class="history-actions">
        <Button :disabled="busy" @click="preview = undefined">{{ t('cancel') }}</Button>
        <Button variant="primary" :disabled="busy || !canRestore" @click="restore">{{ t('restoreHistory') }}</Button>
      </div>
    </div>
    <div v-else class="history-versions">
      <p v-if="busy && !items.length">{{ t('uiLoading') }}</p>
      <ul v-if="items.length">
        <li v-for="item in items" :key="item.version" class="history-version">
          <span>{{ time(item.createdAt) }} · {{ t(reasons[item.reason]) }}</span>
          <Button :disabled="busy" @click="inspect(item.version)">{{ t('preview') }}</Button>
        </li>
      </ul>
      <Button v-if="next" :disabled="busy" @click="list(next)">{{ t('historyMore') }}</Button>
    </div>
  </Modal>
</template>

<style scoped>
.history-versions { min-width: 0; display: grid; gap: 10px; }
.history-versions ul { list-style: none; margin: 0; padding: 0; }
.history-preview { max-height: 44dvh; overflow-y: auto; overflow-wrap: anywhere; }
.history-preview li, .history-version { border-top: 1px solid var(--border); padding: 8px 0; }
.history-preview p { white-space: pre-wrap; margin: 4px 0; }
.history-actions, .history-version { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 8px; }
.history-actions { justify-content: flex-end; }
</style>
