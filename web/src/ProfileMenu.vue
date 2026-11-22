<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { IconExternalLink, IconUser, IconUsers } from '@tabler/icons-vue'
import { Button as VButton, Modal as VModal, Input as VInput, Popover as VPopover } from './ui'
import { profileHome, profileID } from './client/profileScope'
import { useWorkspace } from './composables/useWorkspace'

type Profile = { id: string; name: string }
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
const graphemes = new Intl.Segmenter(undefined, { granularity: 'grapheme' })
const initial = (name?: string) => name ? graphemes.segment(name)[Symbol.iterator]().next().value?.segment : undefined
const current = computed(() => profiles.value.find(p => p.id === profileID))
const currentLabel = computed(() => current.value ? label(current.value) : t(profileID === 'default' ? 'defaultProfile' : 'profiles'))
const choices = computed(() => profiles.value.filter(p => p.id !== profileID))
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
async function save(id: string, value: string) {
  busy.value = true; error.value = ''
  try {
    const result = await client.request<Profile>(id ? `/profiles/${encodeURIComponent(id)}` : '/profiles', { method: id ? 'PATCH' : 'POST', body: JSON.stringify({ name: value }) })
    serial++
    profiles.value = id ? profiles.value.map(p => p.id === id ? result : p) : [...profiles.value, result]
    editor.value = null
  } catch (cause) { error.value = String(cause) }
  finally { busy.value = false }
}
</script>

<template>
  <v-popover v-model:open="pickerOpen" placement="topLeft" :positioning="{ overlap: false, flip: false }" root-class="profile-popover" :aria-label="t('profiles')">
    <v-button class="profiles-trigger" variant="text" :aria-label="t('profiles')" :title="currentLabel">
      <span class="profile-avatar" aria-hidden="true">
        <span v-if="current?.name">{{ initial(current.name) }}</span><IconUser v-else />
      </span>
      <span class="profile-name">{{ currentLabel }}</span>
    </v-button>
    <template #content>
      <div class="profile-picker">
        <header class="profile-current">
          <span class="profile-avatar" aria-hidden="true">
            <span v-if="current?.name">{{ initial(current.name) }}</span><IconUser v-else />
          </span>
          <div><small>{{ t('currentProfile') }}</small><strong :title="currentLabel">{{ currentLabel }}</strong></div>
        </header>
        <div v-if="readError" class="profile-read-error profile-picker-message">
          <small role="alert">{{ readError }}</small><v-button variant="text" size="small" @click="load()">{{ t('retry') }}</v-button>
        </div>
        <small v-else-if="!profiles.length" class="profile-picker-message" role="status">{{ t('uiLoading') }}</small>
        <ul v-if="choices.length" class="profile-choices">
          <li v-for="p in choices" :key="p.id" class="profile-choice">
            <a class="profile-switch-link" :href="profileHome(p.id)" @click="pickerOpen = false">
              <span class="profile-avatar" aria-hidden="true">
                <span v-if="p.name">{{ initial(p.name) }}</span><IconUser v-else />
              </span>
              <span class="profile-choice-label">{{ label(p) }}</span>
            </a>
            <a class="profile-tab-link" :href="profileHome(p.id)" target="_blank" rel="noopener"
              :aria-label="t('profileNewTab')" :aria-description="label(p)" :title="t('profileNewTab')" @click="pickerOpen = false"><IconExternalLink aria-hidden="true" /></a>
          </li>
        </ul>
        <footer class="profile-picker-footer">
          <v-button class="profile-manage" variant="text" @click="manage"><IconUsers aria-hidden="true" />{{ t('manageProfiles') }}</v-button>
        </footer>
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
        <div :aria-current="p.id === profileID ? 'true' : undefined">{{ label(p) }}</div>
        <v-button size="small" variant="text" :disabled="busy" @click="edit(p.id, p.name)">{{ t('editTitle') }}</v-button>
      </li>
    </ul>
    <form v-if="editor !== null" class="profile-name-form" @submit.prevent="name.trim() && save(editor, name)">
      <v-input v-model:value="name" :aria-label="t('profileName')" :disabled="busy" maxlength="200" />
      <v-button type="submit" :disabled="busy || !name.trim()">{{ t('saveChanges') }}</v-button>
      <v-button variant="text" :disabled="busy" @click="editor = null">{{ t('cancel') }}</v-button>
    </form>
    <v-button v-else :disabled="busy" @click="edit('', '')">{{ t('profileNew') }}</v-button>
  </v-modal>
</template>

<style scoped>
.profiles-trigger.ui-btn { min-height: 44px; gap: .5rem; padding: .35rem .5rem; }
.profiles-trigger[aria-expanded="true"] { background: var(--hover); }
.profile-avatar {
  display: inline-grid;
  place-items: center;
  flex: none;
  width: 1.625rem;
  height: 1.625rem;
  border-radius: 50%;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: .875rem;
  font-weight: 650;
  line-height: 1;
}
.profile-avatar svg,.profile-choice svg { width: 1rem; height: 1rem; }
.profile-name { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
:global(.profile-popover > .ui-popover-inner) { padding: 0; }
.profile-picker {
  display: flex;
  flex-direction: column;
  width: min(16rem, calc(100vw - 28px));
  max-height: min(26rem, calc(var(--available-height, 100dvh) - 2px));
  overflow: hidden;
  border-radius: inherit;
}
.profile-current {
  display: flex;
  flex: none;
  align-items: center;
  gap: .625rem;
  padding: .65rem .75rem;
  border-bottom: 1px solid var(--border-subtle);
}
.profile-current .profile-avatar { width: 2rem; height: 2rem; }
.profile-current div { min-width: 0; overflow-wrap: anywhere; }
.profile-current small { display: block; color: var(--muted); }
.profile-current strong { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.profile-choices,.profiles-list { list-style: none; }
.profile-choices {
  margin: 0;
  min-height: 0;
  padding: .25rem .375rem;
  overflow-y: auto;
  scrollbar-width: thin;
  overscroll-behavior: contain;
}
.profile-choice { display: flex; gap: .125rem; }
.profile-choice a {
  display: flex;
  align-items: center;
  gap: .5rem;
  min-height: 44px;
  padding: .375rem .5rem;
  border-radius: var(--control-radius);
  color: var(--text);
  text-decoration: none;
}
.profile-choice a:hover,.profile-choice a:focus-visible { background: var(--hover); }
.profile-choice a:focus-visible { outline-offset: -2px; }
.profile-switch-link { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.profile-choice .profile-tab-link { flex: none; justify-content: center; width: 44px; color: var(--muted); }
.profile-picker-footer { flex: none; padding: .25rem .375rem; }
.profile-choices + .profile-picker-footer { border-top: 1px solid var(--border-subtle); }
.profile-manage { width: 100%; min-height: 44px; height: auto; padding-inline: .5rem; justify-content: flex-start; }
.profile-picker-message { padding: .5rem .75rem; }
.profile-read-error { display: flex; align-items: center; flex-wrap: wrap; gap: .3rem; overflow-wrap: anywhere; }
.profiles-list { margin: 0 0 1rem; padding: 0; max-height: 50dvh; overflow: auto; }
.profiles-list li { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: .75rem; padding: .4rem 0; }
.profiles-list li + li { border-top: 1px solid var(--border-subtle); }
.profiles-list li > div { overflow-wrap: anywhere; }
.profiles-list [aria-current="true"] { font-weight: 650; }
.profile-name-form { display: flex; align-items: center; gap: .65rem; flex-wrap: wrap; padding: .4rem 0; }
</style>
