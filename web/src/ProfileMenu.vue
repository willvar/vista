<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { IconExternalLink, IconPencil, IconPlus, IconUser } from '@tabler/icons-vue'
import { Button as VButton, Input as VInput, Popover as VPopover } from './ui'
import { profileHome } from './client/profileScope'
import { useWorkspace } from './composables/useWorkspace'

type Profile = { id: string; name: string }
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync, mobileSessionsOpen, profileId } = useWorkspace()
const pickerOpen = ref(false)
const profiles = ref<Profile[]>([])
const list = ref<HTMLUListElement>()
const newButton = ref<InstanceType<typeof VButton>>()
const nameId = useId()
const name = ref('')
// Empty ID means a new user. An existing ID edits that row in place.
const editor = ref<string | null>(null)
const busy = ref(false)
const saveFailed = ref(false)
const readError = ref(false)
const cleanName = computed(() => name.value.trim())
const encoder = new TextEncoder()
const nameTooLong = computed(() => encoder.encode(cleanName.value).length > 200)
const formError = computed(() => nameTooLong.value ? t('profileNameTooLong') : saveFailed.value ? t('profileSaveFailed') : '')
let scrollBeforeCreate = 0
let composing = false
let compositionEndedAt = -Infinity
let serial = 0
const lifetime = new AbortController()
const label = (p: Profile) => p.name || t('defaultProfile')
const graphemes = new Intl.Segmenter(undefined, { granularity: 'grapheme' })
const initial = (name?: string) => name ? graphemes.segment(name)[Symbol.iterator]().next().value?.segment : undefined
const current = computed(() => profiles.value.find(p => p.id === profileId))
const currentLabel = computed(() => current.value ? label(current.value) : t(profileId === 'default' ? 'defaultProfile' : 'profiles'))
const entries = computed(() => {
  const rows = current.value ? [current.value, ...profiles.value.filter(p => p.id !== profileId)] : profiles.value
  return editor.value === '' ? [...rows, { id: '', name: '' }] : rows
})
const canSwitch = (p: Profile) => editor.value === null && !busy.value && p.id !== profileId

async function load(signal = lifetime.signal) {
  const version = ++serial
  try {
    const result = await client.request<Profile[]>('/profiles', { signal })
    if (version === serial && !signal.aborted) { profiles.value = result; readError.value = false }
  } catch {
    if (version === serial && !signal.aborted) readError.value = true
  }
}
watch(pickerOpen, (open, _, cleanup) => {
  if (!open) { composing = false; return }
  const controller = new AbortController()
  const stop = stateSync.subscribe(['profiles'], async (_, signal) => { await load(signal) })
  if (!profiles.value.length && !stateSync.snapshot()) void load(controller.signal)
  // Closing the mobile drawer hides the popover without abandoning its edit.
  if (editor.value !== null) void nextTick(() => {
    if (pickerOpen.value && editor.value !== null) focusEditor(false)
  })
  cleanup(() => { stop(); controller.abort() })
})
if (profileId !== 'default') void load()
onScopeDispose(() => lifetime.abort())
watch(mobileSessionsOpen, visible => { if (!visible) pickerOpen.value = false })
watch(name, () => { saveFailed.value = false })
function changeOpen(open: boolean) { if (open || editor.value === null) pickerOpen.value = open }

async function edit(id: string, value: string) {
  if (editor.value !== null || busy.value) return
  if (!id) scrollBeforeCreate = list.value?.scrollTop ?? 0
  name.value = value; saveFailed.value = false; editor.value = id
  composing = false; compositionEndedAt = -Infinity
  await nextTick()
  if (!pickerOpen.value || editor.value !== id) return
  focusEditor(!!id)
}
function focusEditor(selectName: boolean) {
  const input = document.getElementById(nameId) as HTMLInputElement | null
  input?.focus({ preventScroll: true })
  if (selectName) input?.select()
  input?.closest('li')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
}
async function finishEdit(focusID?: string) {
  const id = focusID || editor.value
  editor.value = null; saveFailed.value = false
  await nextTick()
  if (!pickerOpen.value || editor.value !== null) return
  const row = id ? list.value?.querySelector<HTMLLIElement>(`[data-profile-id="${CSS.escape(id)}"]`) : null
  if (row) {
    row.querySelector<HTMLElement>('.profile-rename')?.focus({ preventScroll: true })
    row.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  } else {
    if (list.value) list.value.scrollTop = scrollBeforeCreate
    newButton.value?.$el.focus({ preventScroll: true })
  }
}
function cancelEdit() { if (!busy.value) void finishEdit() }
function inComposition(event: KeyboardEvent) { return composing || event.isComposing || event.keyCode === 229 || performance.now() - compositionEndedAt < 200 }
function escapeEdit(event: KeyboardEvent) {
  if (editor.value === null || inComposition(event)) return
  event.preventDefault(); event.stopPropagation()
  if (!event.repeat) cancelEdit()
}
function editorKeydown(event: KeyboardEvent) { if (event.key === 'Enter' && inComposition(event)) event.preventDefault() }
function compositionStart() { composing = true }
function compositionEnd() { composing = false; compositionEndedAt = performance.now() }
async function save() {
  const id = editor.value; const value = cleanName.value
  if (id === null || busy.value || composing || !value || nameTooLong.value) return
  if (id && profiles.value.find(p => p.id === id)?.name === value) { await finishEdit(id); return }
  busy.value = true; saveFailed.value = false
  let saved: Profile | undefined
  try {
    const result = await client.request<Profile>(id ? `/profiles/${encodeURIComponent(id)}` : '/profiles', {
      method: id ? 'PATCH' : 'POST', body: JSON.stringify({ name: value }),
    })
    serial++
    // A state read may already contain the newly created user.
    profiles.value = profiles.value.some(p => p.id === result.id)
      ? profiles.value.map(p => p.id === result.id ? result : p) : [...profiles.value, result]
    saved = result
  } catch { saveFailed.value = true }
  finally { busy.value = false }
  if (saved && editor.value === id) await finishEdit(saved.id)
}
</script>

<template>
  <v-popover :open="pickerOpen" :close-on-escape="editor === null" :close-on-interact-outside="editor === null"
    placement="topLeft" :positioning="{ overlap: false, flip: false }" root-class="profile-popover" :aria-label="t('profiles')" @update:open="changeOpen">
    <v-button class="profiles-trigger" variant="text" :aria-label="t('profiles')" :title="currentLabel">
      <span class="profile-avatar" aria-hidden="true">
        <span v-if="current?.name">{{ initial(current.name) }}</span><IconUser v-else />
      </span>
      <span class="profile-name">{{ currentLabel }}</span>
    </v-button>
    <template #content>
      <div class="profile-picker" @keydown.esc="escapeEdit">
        <header class="profile-picker-header">
          <strong>{{ t('profiles') }}</strong>
          <small>{{ t('profilePickerHint') }}</small>
        </header>
        <ul ref="list" class="profile-choices" :aria-busy="!profiles.length && !readError">
          <li v-if="readError" class="profile-picker-message profile-read-error">
            <small role="alert">{{ t('profileLoadFailed') }}</small><v-button variant="text" @click="load()">{{ t('retry') }}</v-button>
          </li>
          <li v-else-if="!profiles.length" class="profile-picker-message" role="status">{{ t('uiLoading') }}</li>
          <li v-for="p in entries" :key="p.id" :data-profile-id="p.id" :aria-current="p.id === profileId ? 'true' : undefined"
            class="profile-choice" :class="{ 'profile-current': p.id === profileId }">
            <form v-if="editor === p.id" class="profile-name-form" @submit.prevent="save">
              <label :for="nameId" :class="{ 'visually-hidden': !!p.id }">{{ t('profileName') }}</label>
              <v-input :id="nameId" v-model:value="name" :readonly="busy" :status="formError ? 'error' : undefined"
                :aria-describedby="formError ? nameId + '-error' : undefined" maxlength="200" @keydown="editorKeydown"
                @compositionstart="compositionStart" @compositionend="compositionEnd" />
              <p v-if="formError" :id="nameId + '-error'" class="profile-form-error" role="alert">{{ formError }}</p>
              <div class="profile-edit-actions">
                <v-button :disabled="busy" variant="text" @click="cancelEdit">{{ t('cancel') }}</v-button>
                <v-button type="submit" variant="primary" :disabled="!cleanName || nameTooLong" :loading="busy">{{ t(p.id ? 'saveChanges' : 'create') }}</v-button>
              </div>
            </form>
            <template v-else>
              <component :is="canSwitch(p) ? RouterLink : 'div'" class="profile-switch-link" :to="canSwitch(p) ? profileHome(p.id) : undefined">
                <span class="profile-avatar" aria-hidden="true">
                  <span v-if="p.name">{{ initial(p.name) }}</span><IconUser v-else />
                </span>
                <span class="profile-choice-label">
                  <span>{{ label(p) }}</span><small v-if="p.id === profileId">{{ t('currentProfile') }}</small>
                </span>
              </component>
              <a v-if="canSwitch(p)" class="profile-tab-link" :href="profileHome(p.id)" target="_blank" rel="noopener"
                :aria-label="t('profileNewTab')" :aria-description="label(p)" :title="t('profileNewTab')" @click="changeOpen(false)"><IconExternalLink aria-hidden="true" /></a>
              <v-button class="profile-rename" variant="text" :disabled="editor !== null || busy" :aria-label="t('editTitle')" :aria-description="label(p)"
                :title="t('editTitle')" @click="edit(p.id, label(p))"><IconPencil aria-hidden="true" /></v-button>
            </template>
          </li>
        </ul>
        <footer v-if="editor !== ''" class="profile-picker-footer">
          <v-button ref="newButton" class="profile-new" variant="text" :disabled="editor !== null || busy || !profiles.length" @click="edit('', '')">
            <IconPlus aria-hidden="true" />{{ t('profileNew') }}
          </v-button>
        </footer>
      </div>
    </template>
  </v-popover>
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
  width: min(20rem, calc(100vw - 28px));
  max-height: min(28rem, calc(var(--available-height, 100dvh) - 2px));
  overflow: hidden;
  border-radius: inherit;
}
.profile-picker-header { flex: none; padding: .6rem .75rem; border-bottom: 1px solid var(--border-subtle); }
.profile-picker-header small { display: block; color: var(--muted); font-size: .8em; overflow-wrap: anywhere; }
.profile-choices {
  list-style: none;
  margin: 0;
  min-height: 0;
  padding: .25rem .375rem;
  overflow-y: auto;
  scrollbar-width: thin;
  overscroll-behavior: contain;
}
.profile-choice { display: flex; gap: .125rem; border-radius: var(--control-radius); }
.profile-choice + .profile-choice { margin-block-start: .375rem; }
.profile-choice .profile-switch-link,.profile-choice .profile-tab-link {
  display: flex;
  align-items: center;
  min-height: 44px;
  padding: .3rem .5rem;
  border-radius: var(--control-radius);
  color: var(--text);
  text-decoration: none;
}
.profile-choice a:hover,.profile-choice a:focus-visible { background: var(--hover); }
.profile-choice a:focus-visible { outline-offset: -2px; }
.profile-switch-link { flex: 1; gap: .5rem; min-width: 0; overflow-wrap: anywhere; }
.profile-choice-label { min-width: 0; }
.profile-choice-label small { display: block; color: var(--muted); }
.profile-current { background: var(--surface-subtle); }
.profile-current .profile-choice-label > span { font-weight: 650; }
.profile-choice .profile-tab-link,.profile-rename.ui-btn { flex: none; justify-content: center; width: 44px; height: auto; min-height: 44px; padding: 0; color: var(--muted); }
.profile-picker-footer { flex: none; padding: .25rem .375rem; border-top: 1px solid var(--border-subtle); }
.profile-new { width: 100%; min-height: 44px; height: auto; padding-inline: .5rem; justify-content: flex-start; }
.profile-picker-message { padding: .5rem .4rem; }
.profile-read-error { display: flex; align-items: center; flex-wrap: wrap; gap: .3rem; overflow-wrap: anywhere; }
.profile-name-form { flex: 1; min-width: 0; padding: .4rem; }
.profile-name-form label { display: block; font-size: .875rem; margin-bottom: .25rem; }
.profile-name-form :deep(.ui-input),.profile-edit-actions .ui-btn { min-height: 44px; }
.profile-form-error { margin: .3rem 0 0; color: var(--danger); font-size: .875rem; }
.profile-edit-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: .3rem; margin-top: .35rem; }
@media (max-height: 385px) {
  .profile-picker-header { padding-block: .35rem; }
}
</style>
