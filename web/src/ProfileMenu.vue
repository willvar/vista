<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconChevronUp, IconExternalLink, IconUser, IconUsers } from '@tabler/icons-vue'
import { Button as VButton, Modal as VModal, Input as VInput, Popover as VPopover } from './ui'
import { profileHome, profileID } from './client/profileScope'
import { useWorkspace } from './composables/useWorkspace'

type Profile = { id: string; name: string; archived: boolean }
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync, mobileSessionsOpen } = useWorkspace()
const pickerOpen = ref(false)
const managing = ref(false)
const profiles = ref<Profile[]>([])
const name = ref('')
const editor = ref<string | null>(null)
const busy = ref(false)
const error = ref('')
const readError = ref('')
let serial = 0
const label = (p: Profile) => p.name || t('defaultProfile')
const current = computed(() => profiles.value.find(p => p.id === profileID))
const currentLabel = computed(() => current.value ? label(current.value) : t(profileID === 'default' ? 'defaultProfile' : 'profiles'))
const choices = computed(() => profiles.value.filter(p => !p.archived && p.id !== profileID))
const lifetime = new AbortController()
async function load(signal = lifetime.signal) {
  const version = ++serial
  try {
    const result = await client.request<Profile[]>('/profiles', { signal })
    if (version === serial && !signal.aborted) { profiles.value = result; readError.value = '' }
  } catch (cause) {
    if (version === serial && !signal.aborted) readError.value = String(cause)
  }
}
// The picker and its settings dialog share one subscription while in use.
watch(() => pickerOpen.value || managing.value, (visible, _, cleanup) => {
  if (!visible) return
  const controller = new AbortController()
  const stop = stateSync.subscribe(['profiles'], async (_, signal) => { await load(signal) })
  if (!profiles.value.length && !stateSync.snapshot()) void load(controller.signal)
  cleanup(() => { stop(); controller.abort() })
})
if (profileID !== 'default') void load()
onScopeDispose(() => lifetime.abort())
watch(mobileSessionsOpen, visible => { if (!visible) pickerOpen.value = false })
watch(managing, visible => { if (!visible) editor.value = null })
function manage() { pickerOpen.value = false; error.value = ''; managing.value = true }
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
  <v-popover v-model:open="pickerOpen" placement="topLeft" :aria-label="t('profiles')">
    <v-button class="profiles-trigger" variant="text" :aria-label="t('profiles')" :title="currentLabel">
      <span class="profile-avatar" aria-hidden="true"><IconUser /></span>
      <span class="profile-name">{{ currentLabel }}</span>
      <IconChevronUp class="profile-chevron" aria-hidden="true" />
    </v-button>
    <template #content>
      <div class="profile-picker">
        <header class="profile-current">
          <span class="profile-avatar" aria-hidden="true"><IconUser /></span>
          <div><small>{{ t('profiles') }}</small><strong>{{ currentLabel }}</strong></div>
        </header>
        <div v-if="readError" class="profile-read-error">
          <small role="alert">{{ readError }}</small><v-button variant="text" size="small" @click="load()">{{ t('retry') }}</v-button>
        </div>
        <small v-else-if="!profiles.length" role="status">{{ t('uiLoading') }}</small>
        <ul v-if="choices.length" class="profile-choices">
          <li v-for="p in choices" :key="p.id" class="profile-choice">
            <a class="profile-switch-link" :href="profileHome(p.id)" @click="pickerOpen = false">
              <IconUser aria-hidden="true" /><span>{{ label(p) }}</span>
            </a>
            <a class="profile-tab-link" :href="profileHome(p.id)" target="_blank" rel="noopener"
              :aria-label="t('profileNewTab')" :title="t('profileNewTab')" @click="pickerOpen = false"><IconExternalLink aria-hidden="true" /></a>
          </li>
        </ul>
        <v-button class="profile-manage" variant="text" @click="manage"><IconUsers aria-hidden="true" />{{ t('manageProfiles') }}</v-button>
      </div>
    </template>
  </v-popover>
  <v-modal :open="managing" :title="t('manageProfiles')" :footer="null" centered width="min(560px, calc(100vw - 2rem))"
    :mask-closable="!busy" :keyboard="!busy" :close-disabled="busy" @cancel="!busy && (managing = false)">
    <p v-if="error" role="alert">{{ error }}</p>
    <div v-if="readError" class="profile-read-error">
      <small role="alert">{{ readError }}</small><v-button variant="text" size="small" @click="load()">{{ t('retry') }}</v-button>
    </div>
    <ul class="profiles-list">
      <li v-for="p in profiles" :key="p.id">
        <div :aria-current="p.id === profileID ? 'true' : undefined">
          {{ label(p) }}<span v-if="p.archived"> ({{ t('profileArchived') }})</span>
        </div>
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
.profiles-trigger{min-width:0;gap:.45rem;padding-inline:.5rem}
.profile-avatar{display:inline-grid;place-items:center;flex:none;width:1.75rem;height:1.75rem;border-radius:50%;background:var(--surface-subtle);color:var(--accent)}
.profile-avatar svg,.profile-choice svg,.profile-chevron{width:1rem;height:1rem;flex:none}
.profile-name{min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}
.profile-picker{width:min(16rem,calc(100vw - 50px));max-height:min(26rem,calc(var(--available-height,100dvh) - 26px));overflow:auto;overscroll-behavior:contain}
.profile-current{display:flex;align-items:center;gap:.65rem;padding:.25rem .4rem .65rem;border-bottom:1px solid var(--border-subtle)}
.profile-current div{min-width:0;overflow-wrap:anywhere}
.profile-current small,.profile-current strong{display:block}
.profile-current small{color:var(--muted)}
.profile-choices,.profiles-list{list-style:none;padding:0}
.profile-choices{margin:.35rem 0}
.profile-choice{display:flex;align-items:stretch;gap:.2rem}
.profile-choice a{display:flex;align-items:center;gap:.45rem;min-height:36px;padding:.25rem .4rem;border-radius:var(--control-radius);color:var(--text);text-decoration:none}
.profile-choice a:hover{background:var(--surface-subtle)}
.profile-switch-link{flex:1;min-width:0;overflow-wrap:anywhere}
.profile-tab-link{flex:none;justify-content:center;min-width:34px}
.profile-manage{width:100%;justify-content:flex-start;margin-top:.3rem}
.profile-read-error{display:flex;align-items:center;flex-wrap:wrap;gap:.3rem;overflow-wrap:anywhere}
.profiles-list{margin:0 0 1rem;max-height:50dvh;overflow:auto}
.profiles-list li,.profile-name-form{display:flex;align-items:center;gap:.65rem;flex-wrap:wrap;padding:.4rem 0}
.profiles-list li>div{min-width:7rem;overflow-wrap:anywhere}
</style>
