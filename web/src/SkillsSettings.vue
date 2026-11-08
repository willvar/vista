<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as VEmpty, Alert as VAlert, Button as VButton, Input as VInput, Switch as VSwitch } from './ui'
import { DeleteOutlined, EditOutlined, EyeOutlined, FileMarkdownOutlined, SearchOutlined } from '@antdv-next/icons'
import ConfirmPopover from './ConfirmPopover.vue'
import SkillEditor from './SkillEditor.vue'
import { useWorkspace } from './composables/useWorkspace'
import { useStateQuery } from './composables/useStateQuery'

type Skill = { id: string; name: string; description: string; path: string; source: 'system' | 'user'; disabled: boolean; error?: string }
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
const { client, stateSync } = useWorkspace()
const lifetime = new AbortController()
const resource = useStateQuery<{ skills: Skill[]; directory: string; version?: string }>(client, stateSync, '/skills', ['skills'])
const { loading, error } = resource
const skills = computed(() => resource.data.value?.skills ?? [])
const directory = computed(() => resource.data.value?.directory ?? '')
const query = ref('')
const busy = ref(false)
const saved = ref(false)
const visibleSkills = computed(() => skills.value.filter(skill => `${skill.name} ${skill.description} ${skill.id}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const hasUserSkills = computed(() => skills.value.some(skill => skill.source !== 'system'))
const editorOpen = ref(false)
const editing = ref<Skill | null>(null)
const deleting = ref('')
const deleteError = ref('')
const skillURL = (id: string) => `/skills/${encodeURIComponent(id)}`


function edit(skill: Skill) { if (!busy.value) { editing.value = skill; editorOpen.value = true } }
async function toggle(skill: Skill, enabled: boolean) {
  if (busy.value || skill.source === 'system') return
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
  if (busy.value || skill.source === 'system') return
  busy.value = true
  deleteError.value = ''
  saved.value = false
  try {
    await client.request(skillURL(skill.id), { method: 'DELETE', signal: lifetime.signal })
    if (lifetime.signal.aborted) return
    deleting.value = ''
    saved.value = true
  } catch (cause) { if (!lifetime.signal.aborted) deleteError.value = String(cause) }
  finally { busy.value = false }
}
watch(() => editorOpen.value || !!deleting.value, open => emit('modal-open', open), { flush: 'sync' })
onUnmounted(() => {
  lifetime.abort()
  emit('modal-open', false)
})
defineExpose({ imported: () => { saved.value = true } })
</script>

<template>
  <div class="settings-content skills-settings" :aria-busy="loading">
    <div class="skills-toolbar"><p class="skills-hint">{{ t('skillsSettingsHint') }}</p></div>
    <div v-if="directory" class="skills-directory"><p class="skills-hint">{{ t('skillsDirectory') }}</p><code>{{ directory }}</code><p class="skills-hint">{{ t('skillsDirectoryHint') }}</p></div>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-button v-if="error" :disabled="busy || loading" @click="resource.refresh">{{ t('retry') }}</v-button>
    <v-input v-if="skills.length" v-model:value="query" allow-clear :placeholder="t('skillsSearch')" :aria-label="t('skillsSearch')"><template #prefix><SearchOutlined aria-hidden="true" /></template></v-input>
    <p v-if="loading" class="skills-hint" role="status">{{ t('skillsLoading') }}</p>
    <v-empty v-else-if="!hasUserSkills && !query && !error" class="vista-empty-state skills-empty" :description="t('skillsEmpty')" role="status" />
    <v-empty v-else-if="!visibleSkills.length && !error" class="vista-empty-state skills-empty" :description="t('skillsNoMatch')" role="status" />
    <p v-if="saved" class="skills-saved" role="status">{{ t('skillsSaved') }}</p>
    <article v-for="skill in visibleSkills" :key="skill.id" class="skill-card" :data-skill="skill.id">
      <div class="skill-heading"><div class="skill-name"><FileMarkdownOutlined aria-hidden="true" /><strong>{{ skill.name }}</strong><span class="skill-badge">{{ t(skill.source === 'system' ? 'skillSystem' : 'skillUser') }}</span><span v-if="skill.disabled" class="skill-badge">{{ t('skillDisabled') }}</span></div><v-switch v-if="skill.source !== 'system'" :checked="!skill.disabled" :disabled="busy || !!skill.error" :aria-label="t('skillEnable', { name: skill.name })" @update:checked="toggle(skill, !!$event)" /></div>
      <p v-if="skill.description" class="skill-description">{{ skill.description }}</p>
      <small class="skill-path">{{ skill.source === 'system' ? t('skillSystemHint') : `${skill.id}/SKILL.md` }}</small>
      <p v-if="skill.error" class="skill-error" role="alert">{{ skill.error }}</p>
      <div class="skill-actions"><v-button :disabled="busy" :aria-label="t(skill.source === 'system' ? 'skillViewName' : 'skillEditName', { name: skill.name })" @click="edit(skill)"><component :is="skill.source === 'system' ? EyeOutlined : EditOutlined" aria-hidden="true" />{{ t(skill.source === 'system' ? 'skillView' : 'edit') }}</v-button><ConfirmPopover v-if="skill.source !== 'system'" :show="deleting === skill.id" :message="t('skillDeleteConfirm', { name: skill.name })" :cancel-text="t('cancel')" :confirm-text="t('delete')" :busy="busy" :error="deleteError" popover-class="skill-delete-popover" @update:show="deleting = $event ? skill.id : ''; deleteError = ''" @confirm="remove(skill)"><template #trigger><v-button class="icon-button" variant="text" danger :disabled="busy" :aria-label="t('skillDeleteName', { name: skill.name })"><DeleteOutlined /></v-button></template></ConfirmPopover></div>
    </article>
  </div>

  <SkillEditor v-if="editing" :key="editing.id" v-model:open="editorOpen" :skill="editing" :revision="resource.data.value?.version" @saved="saved = true" />
</template>

<style scoped>
.skills-settings { display: flex; flex-direction: column; gap: 1rem; }
.skills-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.skills-hint { color: var(--muted); font-size: var(--font-size-body); line-height: 1.6; margin: 0; }
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
@media (max-width: 480px) {
  .skills-toolbar { flex-direction: column; gap: .7rem; }
  .skill-card { padding: .8rem; }
}
</style>
