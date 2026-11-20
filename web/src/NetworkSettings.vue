<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Alert as VAlert, Button as VButton, Empty as VEmpty, Modal as VModal } from './ui'
import ProxySelection from './ProxySelection.vue'
import NetworkProfileEditor from './NetworkProfileEditor.vue'
import { useNetworkConfig } from './composables/useNetworkConfig'
import { useWorkspace } from './composables/useWorkspace'
import { VistaAPIError } from '@vista/client'
import type { NetworkCheck, NetworkReference, ProxyProfile } from '@vista/client'
import { networkInput } from './client/network'
const { t } = useI18n({ useScope: 'global' })
const workspace = useWorkspace(), router = useRouter(), route = useRoute(), state = useNetworkConfig(workspace)
const emit = defineEmits<{ 'modal-open': [open: boolean] }>()
const globalProxy = ref(''), baseGlobal = ref(''), globalValid = ref(true), globalEpoch = ref(0)
const saving = ref(false), error = ref('')
const editor = ref<ProxyProfile | null>(), editorDirty = ref(false), editorRevision = ref(''), editorError = ref('')
const deleteTarget = ref<ProxyProfile>(), references = ref<NetworkReference[]>([]), deleteError = ref('')
const discardOpen = ref(false)
let discardResult: ((value: boolean) => void) | undefined
const profiles = computed(() => state.value.value?.config.profiles || [])
const globalDirty = computed(() => globalProxy.value !== baseGlobal.value)
const globalConflict = computed(() => globalDirty.value && state.value.value?.config.globalProxy !== baseGlobal.value)
const editorConflict = computed(() => editor.value !== undefined && !!state.value.value && editorRevision.value !== state.value.value.revision)
const dirty = computed(() => globalDirty.value || editorDirty.value)
watch(state.value, value => {
  if (!value) return
  if (!globalDirty.value) { globalProxy.value = value.config.globalProxy; baseGlobal.value = value.config.globalProxy; globalEpoch.value++ }
}, { immediate: true })
watch([editor, deleteTarget, discardOpen], () => emit('modal-open', editor.value !== undefined || !!deleteTarget.value || discardOpen.value))
function askDiscard() { return new Promise<boolean>(resolve => { discardResult = resolve; discardOpen.value = true }) }
function finishDiscard(value: boolean) { discardOpen.value = false; discardResult?.(value); discardResult = undefined }
async function leave() { if (saving.value) return false; return !dirty.value || await askDiscard() }
onBeforeRouteLeave(leave); onBeforeRouteUpdate(leave)
async function saveGlobal() {
  const current = state.value.value; if (!current || !globalValid.value || globalConflict.value || saving.value) return
  saving.value = true; error.value = ''
  try {
    await state.save({ ...current.config, globalProxy: globalProxy.value }, current.revision)
    baseGlobal.value = state.value.value!.config.globalProxy; globalProxy.value = baseGlobal.value; globalEpoch.value++
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : String(cause); if (cause instanceof VistaAPIError && cause.status === 409) await state.read() }
  finally { saving.value = false }
}
function openProfile(profile?: ProxyProfile) {
  if (saving.value || !state.value.value) return
  editor.value = profile || null; editorDirty.value = false; editorRevision.value = state.value.value.revision; editorError.value = ''
}
async function closeEditor() { if (saving.value || editorDirty.value && !await askDiscard()) return; editor.value = undefined; editorDirty.value = false; editorError.value = '' }
async function saveProfile(profile: ProxyProfile, reuse: boolean) {
  const current = state.value.value; if (!current || editorConflict.value || saving.value) return
  saving.value = true; editorError.value = ''
  try { await state.save({ ...current.config, profiles: [...current.config.profiles.filter(value => value.id !== profile.id), profile] }, editorRevision.value, reuse ? [profile.id] : []); editor.value = undefined; editorDirty.value = false }
  catch (cause) { editorError.value = cause instanceof Error ? cause.message : String(cause); if (cause instanceof VistaAPIError && cause.status === 409) await state.read() }
  finally { saving.value = false }
}
async function reload() {
  if (dirty.value && !await askDiscard()) return
  const current = state.value.value; if (!current) return
  editor.value = undefined; editorDirty.value = false; globalProxy.value = current.config.globalProxy; baseGlobal.value = current.config.globalProxy; globalEpoch.value++
}
async function removeProfile() {
  const current = state.value.value, profile = deleteTarget.value; if (!current || !profile || saving.value) return
  saving.value = true; deleteError.value = ''; references.value = []
  try { await state.save({ ...current.config, profiles: current.config.profiles.filter(value => value.id !== profile.id) }, current.revision); deleteTarget.value = undefined }
  catch (cause) {
    if (cause instanceof VistaAPIError && cause.code === 'proxy_in_use' && Array.isArray(cause.details?.references)) references.value = cause.details.references as NetworkReference[]
    else deleteError.value = cause instanceof Error ? cause.message : String(cause)
  } finally { saving.value = false }
}
function openReference(reference: NetworkReference) {
  deleteTarget.value = undefined
  if (reference.kind === 'global') document.getElementById('network-global')?.scrollIntoView({ block: 'start' })
  else void router.push({ name: 'settings', query: { ...route.query, connection: reference.key } })
}
async function checkProfile(profile: ProxyProfile, reuse: boolean, target: string, signal: AbortSignal) {
  const current = state.value.value; if (!current) throw new Error(t('networkUnavailable'))
  const config = networkInput({ ...current.config, profiles: [...current.config.profiles.filter(value => value.id !== profile.id), profile] })
  return workspace.client.request<NetworkCheck>('/network-check', { method: 'POST', signal, body: JSON.stringify({ config, revision: current.revision, profileId: profile.id, reuseCredentials: reuse ? [profile.id] : [], target }) })
}
onBeforeUnmount(() => { finishDiscard(false); emit('modal-open', false) })
defineExpose({ headerAction: computed(() => ({ label: 'networkAdd', run: () => openProfile(), disabled: saving.value || !state.value.value, loading: false })) })
</script>
<template>
  <div class="settings-content network-settings" :aria-busy="state.loading.value || saving">
    <v-alert v-if="state.error.value || error" type="error" role="alert" :message="state.error.value || error" />
    <p v-if="!state.value.value && !state.error.value" role="status">{{ t('networkLoading') }}</p>
    <v-button v-if="state.error.value" @click="state.read()">{{ t('retry') }}</v-button>
    <template v-if="state.value.value">
      <section id="network-global">
        <h3>{{ t('networkGlobal') }}</h3><p>{{ t('networkGlobalHint') }}</p>
        <ProxySelection id="global-proxy" v-model="globalProxy" :profiles="profiles" :disabled="saving" :reset-key="globalEpoch" @validity="globalValid = $event" />
        <v-alert v-if="globalConflict" type="warning" role="alert" :message="t('networkConflict')" />
        <div class="network-actions"><v-button variant="primary" :loading="saving" :disabled="!globalDirty || !globalValid || globalConflict" @click="saveGlobal">{{ t('saveChanges') }}</v-button></div>
      </section>
      <section>
        <h3>{{ t('networkProfiles') }}</h3>
        <v-empty v-if="!profiles.length"><template #description>{{ t('networkNoProfiles') }}</template></v-empty>
        <div v-for="profile in profiles" :key="profile.id" class="network-profile">
          <div><strong>{{ profile.name }}</strong><small>{{ profile.protocol.toUpperCase() }} · {{ profile.server }} · {{ profile.port }}</small></div>
          <div class="network-actions"><v-button :disabled="saving" @click="openProfile(profile)">{{ t('edit') }}</v-button><v-button :disabled="saving" @click="openProfile(profile)">{{ t('networkCheck') }}</v-button><v-button danger :disabled="saving" @click="deleteTarget = profile; references = []; deleteError = ''">{{ t('delete') }}</v-button></div>
        </div>
      </section>
      <v-button v-if="globalConflict" @click="reload">{{ t('networkReload') }}</v-button>
    </template>
  </div>
  <v-modal :open="editor !== undefined" :title="editor ? t('networkProfileEdit') : t('networkAdd')" width="min(720px, calc(100vw - 2rem))" centered :footer="null" :close-disabled="saving" :keyboard="!saving && !discardOpen" :mask-closable="!saving && !discardOpen" :z-index="1200" destroy-on-hidden :styles="{ body: { maxHeight: 'calc(100dvh - 10rem)', overflowY: 'auto' } }" @cancel="closeEditor">
    <template v-if="editor !== undefined">
      <v-alert v-if="editorConflict" type="warning" role="alert" :message="t('networkConflict')" />
      <v-button v-if="editorConflict" @click="reload">{{ t('networkReload') }}</v-button>
      <NetworkProfileEditor :key="editor?.id || 'new'" :profile="editor || undefined" :busy="saving" :stale="editorConflict" :error="editorError" :check="checkProfile" @save="saveProfile" @cancel="closeEditor" @dirty="editorDirty = $event" />
    </template>
  </v-modal>
  <v-modal :open="!!deleteTarget" :title="t(references.length ? 'networkUsed' : 'delete')" :footer="null" :z-index="1250" :close-disabled="saving" :keyboard="!saving" :mask-closable="!saving" @cancel="!saving && (deleteTarget = undefined)">
    <p v-if="!references.length">{{ deleteTarget?.name }}</p><p v-else>{{ t('networkRemoveSelection') }}</p>
    <ul v-if="references.length" class="network-references"><li v-for="(reference, index) in references" :key="index"><span>{{ reference.kind === 'global' ? t('networkGlobal') : `${t('modelConnections')}: ${reference.name}` }}</span><v-button @click="openReference(reference)">{{ t('networkGoTo') }}</v-button></li></ul>
    <v-alert v-if="deleteError" type="error" role="alert" :message="deleteError" />
    <div class="network-actions"><v-button :disabled="saving" @click="deleteTarget = undefined">{{ t('cancel') }}</v-button><v-button v-if="!references.length" danger :loading="saving" @click="removeProfile">{{ t('delete') }}</v-button></div>
  </v-modal>
  <v-modal :open="discardOpen" :title="t('networkDiscard')" :footer="null" :z-index="1400" @cancel="finishDiscard(false)"><p>{{ t('configEditorDiscard') }}</p><div class="network-actions"><v-button @click="finishDiscard(false)">{{ t('cancel') }}</v-button><v-button danger @click="finishDiscard(true)">{{ t('networkDiscard') }}</v-button></div></v-modal>
</template>
<style scoped>
.network-settings { min-width: 0; }
.network-settings section { border-bottom: 1px solid var(--border); padding: 1rem 0; }
.network-settings h3 { margin: 0 0 .5rem; font-size: 1rem; }
.network-settings p { color: var(--muted); line-height: 1.6; }
.network-profile { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
.network-profile { padding: .75rem 0; border-top: 1px solid var(--border); min-width: 0; }
.network-profile > div:first-child { min-width: 0; overflow-wrap: anywhere; }
.network-profile small { display: block; margin-top: .25rem; color: var(--muted); overflow-wrap: anywhere; }
.network-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: .5rem; margin-top: .75rem; }
.network-profile .network-actions { margin: 0; flex-shrink: 0; }
.network-references { display: flex; flex-direction: column; gap: .75rem; padding: 0; list-style: none; }
.network-references li { display: flex; justify-content: space-between; align-items: center; gap: .5rem; overflow-wrap: anywhere; }
@media (max-width: 830px) { .network-profile { align-items: stretch; flex-direction: column; } .network-profile .network-actions { justify-content: flex-start; } }
</style>
