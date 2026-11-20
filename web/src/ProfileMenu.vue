<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton, Modal as VModal, Input as VInput } from './ui'
import { profileHome, profileID } from './client/profileScope'
import { useWorkspace } from './composables/useWorkspace'

type Profile = { id: string; name: string; archived: boolean }
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const open = ref(false)
const profiles = ref<Profile[]>([])
const name = ref('')
const editor = ref<string | null>(null)
const busy = ref(false)
const error = ref('')
let serial = 0
const label = (p: Profile) => p.name || t('defaultProfile')
const current = computed(() => profiles.value.find(p => p.id === profileID))
async function load(signal?: AbortSignal) {
  const version = ++serial
  const result = await client.request<Profile[]>('/profiles', { signal })
  if (version === serial && !signal?.aborted) profiles.value = result
}
watch(open, (visible, _, cleanup) => {
  if (!visible) { editor.value = null; return }
  error.value = ''
  const stop = stateSync.subscribe(['profiles'], async (_, signal) => { await load(signal) })
  const controller = new AbortController()
  if (!profiles.value.length) void load(controller.signal).catch(cause => { if (!controller.signal.aborted) error.value = String(cause) })
  cleanup(() => { controller.abort(); stop() })
})
if (profileID !== 'default') {
  const controller = new AbortController()
  void load(controller.signal).catch(() => {})
  onScopeDispose(() => controller.abort())
}
function edit(id: string, value: string) { editor.value = id; name.value = value; error.value = '' }
async function save(id: string, patch: { name: string } | { archived: boolean }) {
  busy.value = true; error.value = ''
  try {
    const result = await client.request<Profile>(id ? `/profiles/${encodeURIComponent(id)}` : '/profiles', { method: id ? 'PATCH' : 'POST', body: JSON.stringify(patch) })
    serial++
    profiles.value = id ? profiles.value.map(p => p.id === id ? result : p) : [...profiles.value, result]
    editor.value = null
    if (id === profileID && result.archived) location.assign('/')
  } catch (cause) { error.value = String(cause) }
  finally { busy.value = false }
}
</script>

<template>
  <v-button class="profiles-trigger" variant="text" :aria-label="t('profiles')" @click="open = true">{{ current ? label(current) : t('profiles') }}</v-button>
  <v-modal :open="open" :title="t('profiles')" :footer="null" centered width="min(560px, calc(100vw - 2rem))" @cancel="!busy && (open = false)">
    <p v-if="error" role="alert">{{ error }}</p>
    <ul class="profiles-list">
      <li v-for="p in profiles" :key="p.id">
        <div><a v-if="!p.archived" :href="profileHome(p.id)" :aria-current="p.id === profileID ? 'page' : undefined">{{ label(p) }}</a>
          <span v-else>{{ label(p) }} ({{ t('profileArchived') }})</span></div>
        <a v-if="!p.archived" :href="profileHome(p.id)" target="_blank" rel="noopener">{{ t('profileNewTab') }}</a>
        <v-button size="small" variant="text" :disabled="busy" @click="edit(p.id, p.name)">{{ t('editTitle') }}</v-button>
        <v-button v-if="p.id !== 'default'" size="small" variant="text" :disabled="busy" @click="save(p.id, { archived: !p.archived })">{{ t(p.archived ? 'profileRestore' : 'profileArchive') }}</v-button>
      </li>
    </ul>
    <form v-if="editor !== null" class="profile-name-form" @submit.prevent="name.trim() && save(editor, { name })">
      <v-input v-model:value="name" :aria-label="t('profileName')" :disabled="busy" maxlength="200" />
      <v-button type="submit" :disabled="busy || !name.trim()">{{ t('saveChanges') }}</v-button>
      <v-button variant="text" :disabled="busy" @click="editor = null">{{ t('cancel') }}</v-button>
    </form>
    <v-button v-else :disabled="busy" @click="edit('', '')">{{ t('profileNew') }}</v-button>
  </v-modal>
</template>

<style scoped>
.profiles-trigger{max-width:100%;min-width:0;justify-content:flex-start;overflow:hidden;text-overflow:ellipsis}
.profiles-list{list-style:none;margin:0 0 1rem;padding:0;max-height:50vh;overflow:auto}
.profiles-list li,.profile-name-form{display:flex;align-items:center;gap:.65rem;flex-wrap:wrap;padding:.4rem 0}
.profiles-list li>div{min-width:7rem;overflow-wrap:anywhere}
</style>
