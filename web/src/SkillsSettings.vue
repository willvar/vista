<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert as AAlert, Button as AButton, Input as AInput, Modal as AModal, Switch as ASwitch, TextArea as ATextarea } from 'antdv-next'
import { DeleteOutlined, EditOutlined, FileMarkdownOutlined, SearchOutlined } from '@antdv-next/icons'
import ConfirmPopover from './ConfirmPopover.vue'
import { useWorkspace } from './composables/useWorkspace'

type Skill = { id: string; name: string; description: string; path: string; disabled: boolean; error?: string }
type SkillDocument = { content: string; sha256: string; files: string[] }
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client } = useWorkspace()
const lifetime = new AbortController()
const skills = ref<Skill[]>([])
const directory = ref('')
const maxSkills = ref(100)
const maxBytes = ref(32768)
const query = ref('')
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const saved = ref(false)
const visibleSkills = computed(() => skills.value.filter(skill => `${skill.name} ${skill.description} ${skill.id}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const editorOpen = ref(false)
const editing = ref<Skill | null>(null)
const documentLoading = ref(false)
const formError = ref('')
const draft = reactive({ content: '', sha256: '' })
const supportingFiles = ref<string[]>([])
const deleting = ref('')
const deleteError = ref('')
let editorController: AbortController | undefined
let editorGeneration = 0
const contentBytes = computed(() => new TextEncoder().encode(draft.content).byteLength)
const canSave = computed(() => !loading.value && !documentLoading.value && !!editing.value && !!draft.sha256 && !!draft.content.trim() && contentBytes.value <= maxBytes.value)
const skillURL = (id: string) => `/skills/${encodeURIComponent(id)}`

async function refresh() {
  const result = await client.request<{ skills: Skill[]; directory: string; maxSkills: number; maxBytes: number }>('/skills', { signal: lifetime.signal })
  if (lifetime.signal.aborted) return
  skills.value = result.skills
  directory.value = result.directory
  maxSkills.value = result.maxSkills
  maxBytes.value = result.maxBytes
}
async function load() {
  loading.value = true
  error.value = ''
  try { await refresh() }
  catch (cause) { if (!lifetime.signal.aborted) error.value = String(cause) }
  finally { if (!lifetime.signal.aborted) loading.value = false }
}
onMounted(load)

async function edit(skill: Skill) {
  if (busy.value) return
  editorController?.abort()
  editorController = new AbortController()
  const controller = editorController
  const generation = ++editorGeneration
  editing.value = skill
  Object.assign(draft, { content: '', sha256: '' })
  supportingFiles.value = []
  formError.value = ''
  documentLoading.value = true
  editorOpen.value = true
  try {
    const result = await client.request<SkillDocument>(skillURL(skill.id), { signal: controller.signal })
    if (controller.signal.aborted || lifetime.signal.aborted || generation !== editorGeneration) return
    Object.assign(draft, { content: result.content, sha256: result.sha256 })
    supportingFiles.value = result.files
  } catch (cause) { if (!controller.signal.aborted && !lifetime.signal.aborted) formError.value = String(cause) }
  finally { if (!controller.signal.aborted && generation === editorGeneration) documentLoading.value = false }
}
function closeEditor() {
  if (busy.value) return
  editorController?.abort()
  ++editorGeneration
  editorOpen.value = false
}
async function save() {
  if (busy.value || !canSave.value || !editing.value) return
  busy.value = true
  formError.value = ''
  error.value = ''
  saved.value = false
  try {
    await client.request(skillURL(editing.value.id), { method: 'PUT', body: JSON.stringify({ content: draft.content, sha256: draft.sha256 }), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    editorOpen.value = false
    saved.value = true
    await refresh().catch(cause => { if (!lifetime.signal.aborted) error.value = String(cause) })
  } catch (cause) { if (!lifetime.signal.aborted) formError.value = String(cause) }
  finally { busy.value = false }
}
async function toggle(skill: Skill, enabled: boolean) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    await client.request(skillURL(skill.id), { method: 'PATCH', body: JSON.stringify({ disabled: !enabled }), signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    skill.disabled = !enabled
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) error.value = String(cause) }
  finally { busy.value = false }
}
async function remove(skill: Skill) {
  if (busy.value) return
  busy.value = true
  deleteError.value = ''
  saved.value = false
  try {
    await client.request(skillURL(skill.id), { method: 'DELETE', signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    skills.value = skills.value.filter(item => item.id !== skill.id)
    deleting.value = ''
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) deleteError.value = String(cause) }
  finally { busy.value = false }
}
watch(() => editorOpen.value || !!deleting.value, open => emit('modal-open', open), { flush: 'sync' })
onUnmounted(() => {
  lifetime.abort()
  editorController?.abort()
  emit('modal-open', false)
})
defineExpose({ headerAction: computed(() => ({ label: 'refresh', run: load, disabled: loading.value || busy.value, loading: loading.value })) })
</script>

<template>
  <div class="settings-content skills-settings" :aria-busy="loading">
    <div class="skills-toolbar"><p class="skills-hint">{{ t('skillsSettingsHint') }}</p></div>
    <div v-if="directory" class="skills-directory"><p class="skills-hint">{{ t('skillsDirectory') }}</p><code>{{ directory }}</code><p class="skills-hint">{{ t('skillsDirectoryHint') }}</p></div>
    <a-alert v-if="error" type="error" role="alert" :message="error" />
    <a-button v-if="error" :disabled="busy || loading" @click="load">{{ t('retry') }}</a-button>
    <a-input v-if="skills.length" v-model:value="query" allow-clear :placeholder="t('skillsSearch')" :aria-label="t('skillsSearch')"><template #prefix><SearchOutlined aria-hidden="true" /></template></a-input>
    <p v-if="loading" class="skills-hint" role="status">{{ t('skillsLoading') }}</p>
    <p v-else-if="!skills.length && !error" class="skills-empty">{{ t('skillsEmpty') }}</p>
    <p v-else-if="!visibleSkills.length && !error" class="skills-empty">{{ t('skillsNoMatch') }}</p>
    <p v-if="saved" class="skills-saved" role="status">{{ t('skillsSaved') }}</p>
    <article v-for="skill in visibleSkills" :key="skill.id" class="skill-card" :data-skill="skill.id">
      <div class="skill-heading"><div class="skill-name"><FileMarkdownOutlined aria-hidden="true" /><strong>{{ skill.name }}</strong><span v-if="skill.disabled" class="skill-badge">{{ t('skillDisabled') }}</span></div><a-switch :checked="!skill.disabled" :disabled="busy || !!skill.error" :aria-label="t('skillEnable', { name: skill.name })" @update:checked="toggle(skill, !!$event)" /></div>
      <p v-if="skill.description" class="skill-description">{{ skill.description }}</p>
      <small class="skill-path">{{ skill.id }}/SKILL.md</small>
      <p v-if="skill.error" class="skill-error" role="alert">{{ skill.error }}</p>
      <div class="skill-actions"><a-button size="small" :disabled="busy" :aria-label="t('skillEditName', { name: skill.name })" @click="edit(skill)"><EditOutlined aria-hidden="true" />{{ t('edit') }}</a-button><ConfirmPopover :show="deleting === skill.id" :message="t('skillDeleteConfirm', { name: skill.name })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" :error="deleteError" popover-class="skill-delete-popover" @update:show="deleting = $event ? skill.id : ''; deleteError = ''" @confirm="remove(skill)"><template #trigger><a-button type="text" size="small" danger :disabled="busy" :aria-label="t('skillDeleteName', { name: skill.name })"><DeleteOutlined /></a-button></template></ConfirmPopover></div>
    </article>
    <p v-if="skills.length >= maxSkills" class="skills-hint">{{ t('skillsLimit', { count: maxSkills }) }}</p>
  </div>

  <a-modal :open="editorOpen" centered :z-index="1200" width="min(800px, calc(100vw - 2rem))" :title="t('skillEdit')" :footer="null" :closable="!busy" :keyboard="!busy" :mask-closable="!busy" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto', overscrollBehavior: 'contain' } }" @cancel="closeEditor">
    <form v-if="editorOpen" class="skill-form" @submit.prevent="save">
      <a-alert v-if="formError" type="error" role="alert" :message="formError" />
      <p v-if="editing" class="skill-path">{{ editing.path }}</p>
      <p v-if="documentLoading" role="status" class="skills-hint">{{ t('skillsLoading') }}</p>
      <label for="skill-content">SKILL.md<a-textarea id="skill-content" v-model:value="draft.content" class="skill-content-input" :auto-size="false" :disabled="busy || documentLoading || !draft.sha256" aria-label="SKILL.md" aria-describedby="skill-content-hint" :status="contentBytes > maxBytes ? 'error' : undefined" /></label>
      <p id="skill-content-hint" class="skills-hint">{{ t('skillContentHint') }}</p>
      <details v-if="supportingFiles.length" class="skill-supporting-files"><summary>{{ t('skillSupportingFiles', { count: supportingFiles.length }) }}</summary><ul><li v-for="path in supportingFiles" :key="path">{{ path }}</li></ul><small class="skills-hint">{{ t('skillSupportingFilesHint') }}</small></details>
      <div class="skill-form-footer"><small class="skills-hint" :class="{ 'skill-error': contentBytes > maxBytes }">{{ t('instructionBytes', { current: contentBytes, max: maxBytes }) }}</small><div class="skill-form-actions"><a-button :disabled="busy" @click="closeEditor">{{ t('cancel') }}</a-button><a-button html-type="submit" type="primary" :loading="busy" :disabled="busy || !canSave">{{ t('saveChanges') }}</a-button></div></div>
    </form>
  </a-modal>
</template>

<style scoped>
.skills-settings { display: flex; flex-direction: column; gap: 1rem; }
.skills-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.skills-hint { color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; margin: 0; }
.skills-empty { color: var(--muted); text-align: center; padding: 2rem 1rem; }
.skills-saved { color: var(--accent); margin: 0; font-size: var(--font-size-body); }
.skill-card { min-width: 0; border: 1px solid var(--border); border-radius: 12px; padding: 1rem; }
.skill-heading, .skill-name { display: flex; align-items: center; gap: .6rem; }
.skill-heading { justify-content: space-between; }
.skill-heading > button { flex: none; }
.skill-name { min-width: 0; flex-wrap: wrap; overflow-wrap: anywhere; }
.skill-badge { color: var(--muted); font-size: var(--font-size-small); padding: .1rem .4rem; background: var(--surface-subtle); border-radius: 5px; }
.skill-description { color: var(--muted); margin: .6rem 0; font-size: var(--font-size-body); line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.skill-path, .skills-directory code { display: block; color: var(--muted); font: var(--font-size-small)/1.5 var(--font-mono, monospace); overflow-wrap: anywhere; }
.skills-directory { display: flex; flex-direction: column; gap: .35rem; }
.skill-error { color: var(--danger, #dc4446); overflow-wrap: anywhere; }
p.skill-error { font-size: var(--font-size-body); }
.skill-actions { display: flex; flex-wrap: wrap; gap: .5rem; }
.skill-actions { margin-top: .8rem; }
.skill-form { display: flex; flex-direction: column; gap: 1rem; }
.skill-form label { display: flex; flex-direction: column; gap: .4rem; min-width: 0; }
.skill-content-input { height: clamp(180px, 38dvh, 480px); min-height: 120px; resize: vertical; font: var(--font-size-body)/1.65 var(--font-mono, monospace); tab-size: 2; }
.skill-supporting-files { color: var(--muted); font-size: var(--font-size-body); }
.skill-supporting-files summary { cursor: pointer; }
.skill-supporting-files ul { max-height: 10rem; overflow: auto; padding-left: 1.2rem; overflow-wrap: anywhere; }
.skill-form-footer { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: .8rem; }
.skill-form-actions { display: flex; gap: .5rem; margin-left: auto; }
@media (max-width: 480px) {
  .skills-toolbar { flex-direction: column; gap: .7rem; }
  .skill-card { padding: .8rem; }
}
</style>
