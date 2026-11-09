<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Button, Modal, TextArea, Tree } from './ui'
import { IconArrowLeft } from '@tabler/icons-vue'
import type { TreeNode } from './ui/types'
import { useWorkspace } from './composables/useWorkspace'
import { VistaAPIError } from './client/vista'

const props = defineProps<{ open: boolean; skill: { id: string; source: 'system' | 'user' }; revision?: string }>()
const emit = defineEmits<{ 'update:open': [open: boolean]; saved: [] }>()
const { t } = useI18n()
const { client } = useWorkspace()
type Entry = { path: string; directory?: boolean; bytes: number }
type Document = { path: string; kind: 'text' | 'image' | 'binary'; bytes: number; mime?: string; content?: string; sha256?: string; original?: string }
const entries = ref<Entry[]>([])
const documents = reactive<Record<string, Document>>({})
const activePath = ref('SKILL.md')
const document = computed(() => documents[activePath.value])
const dirtyPaths = computed(() => Object.values(documents).filter(file => file.kind === 'text' && file.content !== file.original).map(file => file.path))
const loading = ref(false), initialized = ref(false), saving = ref(false), error = ref(''), conflict = ref(false), discardOpen = ref(false), discardCount = ref(0), mobileFileOpen = ref(false)
const canSave = computed(() => props.skill.source === 'user' && !loading.value && !saving.value && !!document.value?.sha256 && dirtyPaths.value.includes(activePath.value))
const basePath = computed(() => `/skills/${encodeURIComponent(props.skill.id)}`)
const filePath = (name: string) => `${basePath.value}?file=${encodeURIComponent(name)}`
const previewPath = computed(() => '/api' + filePath(activePath.value) + '&preview=1')
let controller = new AbortController(), fileController = new AbortController(), fileGeneration = 0, treeGeneration = 0
let discardAction: (() => void) | undefined

const nodes = computed(() => {
  const root: TreeNode = { value: '', label: '', children: [] }
  const byPath = new Map<string, TreeNode>([['', root]])
  const retained = dirtyPaths.value.filter(path => !entries.value.some(entry => entry.path === path)).map(path => ({ path, bytes: 0 }))
  for (const entry of [...entries.value, ...retained]) {
    const parts = entry.path.split('/')
    for (let index = 0; index < parts.length; index++) {
      const value = parts.slice(0, index + 1).join('/')
      if (byPath.has(value)) continue
      const node: TreeNode = { value, label: parts[index]!, ...(index < parts.length - 1 || ('directory' in entry && entry.directory) ? { children: [] } : { modified: dirtyPaths.value.includes(value) }) }
      byPath.get(parts.slice(0, index).join('/'))!.children!.push(node)
      byPath.set(value, node)
    }
  }
  const sort = (node: TreeNode) => {
    node.children?.sort((a, b) => a.value === 'SKILL.md' ? -1 : b.value === 'SKILL.md' ? 1 : Number(!!b.children) - Number(!!a.children) || a.label.localeCompare(b.label))
    node.children?.forEach(sort)
  }
  sort(root)
  return root.children!
})

async function initialize() {
  controller.abort(); controller = new AbortController(); ++fileGeneration
  const current = controller
  for (const key of Object.keys(documents)) delete documents[key]
  entries.value = []; activePath.value = 'SKILL.md'; mobileFileOpen.value = false; initialized.value = false
  error.value = ''; conflict.value = false; discardOpen.value = false; loading.value = true
  try {
    const result = await client.request<{ content: string; sha256: string; files: string[]; entries?: Entry[] }>(basePath.value, { signal: current.signal })
    if (current !== controller || current.signal.aborted) return
    entries.value = result.entries ?? [{ path: 'SKILL.md', bytes: 0 }, ...result.files.map(path => ({ path, bytes: 0 }))]
    documents['SKILL.md'] = { path: 'SKILL.md', kind: 'text', bytes: new TextEncoder().encode(result.content).length, content: result.content, original: result.content, sha256: result.sha256 }
    initialized.value = true
  } catch (cause) { if (!current.signal.aborted) error.value = String(cause) }
  finally { if (current === controller && !current.signal.aborted) loading.value = false }
}
async function selectFile(name: string, reload = false) {
  if (saving.value) return
  fileController.abort(); fileController = new AbortController()
  activePath.value = name; mobileFileOpen.value = true; error.value = ''; conflict.value = false
  const generation = ++fileGeneration, signal = AbortSignal.any([controller.signal, fileController.signal])
  if (documents[name] && !reload) { loading.value = false; return }
  loading.value = true
  try {
    const file = await client.request<Document>(filePath(name), { signal })
    if (signal.aborted || generation !== fileGeneration) return
    documents[name] = { ...file, original: file.content }
  } catch (cause) { if (!signal.aborted && generation === fileGeneration) error.value = String(cause) }
  finally { if (!signal.aborted && generation === fileGeneration) loading.value = false }
}
async function save() {
  if (!canSave.value) return
  const file = document.value!, content = file.content!, current = controller
  saving.value = true; error.value = ''; conflict.value = false
  try {
    const result = await client.request<{ sha256: string }>(filePath(file.path), { method: 'PUT', body: JSON.stringify({ content, sha256: file.sha256 }), signal: current.signal })
    if (current.signal.aborted) return
    file.original = content; file.sha256 = result.sha256; file.bytes = new TextEncoder().encode(content).length
    emit('saved')
  } catch (cause) {
    if (!current.signal.aborted) { error.value = String(cause); conflict.value = cause instanceof VistaAPIError && cause.status === 409 }
  } finally { if (!current.signal.aborted) saving.value = false }
}
function guardDiscard(action: () => void, count = dirtyPaths.value.length) {
  if (saving.value) return
  if (count) { discardAction = action; discardCount.value = count; discardOpen.value = true } else action()
}
function close() { guardDiscard(() => emit('update:open', false)) }
function discard() { discardOpen.value = false; const action = discardAction; discardAction = undefined; action?.() }
watch(() => props.open, open => { if (open) void initialize(); else { controller.abort(); ++fileGeneration; discardOpen.value = false } }, { immediate: true })
watch(() => props.revision, async (version, previous) => {
  if (!props.open || props.skill.source === 'system' || !previous || version === previous) return
  const current = controller, generation = ++treeGeneration
  try {
    const result = await client.request<{ entries: Entry[] }>(basePath.value + '?tree=1', { signal: current.signal })
    if (!current.signal.aborted && generation === treeGeneration) entries.value = result.entries
  } catch { /* Keep the open document and its draft; deliberate reads report errors. */ }
})
onUnmounted(() => controller.abort())
</script>

<template>
  <Modal :open="open" :title="t(skill.source === 'system' ? 'skillView' : 'skillEdit')" :footer="null" :z-index="1200" width="min(1080px, calc(100vw - 2rem))" root-class="skill-editor-modal" :close-disabled="saving" :keyboard="!saving && !discardOpen" :mask-closable="!saving && !discardOpen" @cancel="close">
    <Alert v-if="error && !initialized" type="error" role="alert" :message="error" />
    <Button v-if="error && !initialized" @click="initialize">{{ t('retry') }}</Button>
    <p v-if="loading && !initialized" role="status">{{ t('skillsLoading') }}</p>
    <div class="skill-browser" :class="{ 'skill-file-open': mobileFileOpen }">
      <nav class="skill-file-tree" :aria-label="t('skillFiles')"><Tree :nodes="nodes" :value="activePath" :label="t('skillFiles')" @update:value="selectFile" /></nav>
      <section class="skill-file-content">
        <div class="skill-file-heading"><Button class="skill-files-back" variant="text" @click="mobileFileOpen = false"><IconArrowLeft aria-hidden="true" />{{ t('skillFiles') }}</Button><strong>{{ activePath }}<span v-if="dirtyPaths.includes(activePath)" class="skill-dirty-mark"> ●</span></strong></div>
        <Alert v-if="error && initialized" type="error" role="alert" :message="error" />
        <Button v-if="error && initialized && !document" @click="selectFile(activePath)">{{ t('retry') }}</Button>
        <Button v-if="conflict" @click="guardDiscard(() => selectFile(activePath, true), dirtyPaths.includes(activePath) ? 1 : 0)">{{ t('skillReloadFile') }}</Button>
        <p v-if="loading && initialized" role="status">{{ t('skillsLoading') }}</p>
        <template v-else-if="document">
          <TextArea v-if="document.kind === 'text'" v-model:value="document.content" class="skill-content-input" :auto-size="false" :aria-label="activePath" :readonly="skill.source === 'system'" :disabled="saving" />
          <img v-else-if="document.kind === 'image'" class="skill-image-preview" :src="previewPath" :alt="activePath" />
          <p v-else class="skills-hint">{{ t('skillBinaryFile') }}</p>
          <small class="skill-file-info">{{ document.bytes.toLocaleString() }} {{ t('skillBytes') }}</small>
          <p v-if="activePath === 'SKILL.md' && skill.source !== 'system'" class="skills-hint">{{ t('skillContentHint') }}</p>
        </template>
      </section>
    </div>
    <div class="skill-form-footer"><small v-if="dirtyPaths.length" class="skills-hint">{{ t('skillUnsavedFiles', { count: dirtyPaths.length }) }}</small><div class="skill-form-actions"><Button :disabled="saving" @click="close">{{ t('close') }}</Button><Button v-if="skill.source !== 'system'" variant="primary" :loading="saving" :disabled="!canSave" @click="save">{{ t('saveChanges') }}</Button></div></div>
  </Modal>
  <Modal :open="discardOpen" :title="t('skillDiscardTitle')" :z-index="1300" :ok-text="t('skillDiscard')" :cancel-text="t('cancel')" :ok-button-props="{ danger: true }" @cancel="discardOpen = false" @ok="discard"><p>{{ t('skillDiscardHint', { count: discardCount }) }}</p></Modal>
</template>

<style scoped>
.skill-browser{display:grid;grid-template-columns:minmax(170px,240px) minmax(0,1fr);gap:1.2rem;height:min(65dvh,640px);min-height:160px}
.skill-file-tree{min-width:0;overflow:auto;overscroll-behavior:contain;padding-inline-end:.7rem;border-inline-end:1px solid var(--border-subtle)}
.skill-file-content{display:flex;flex-direction:column;gap:.65rem;min-width:0;min-height:0;overflow:auto}
.skill-file-heading{display:flex;align-items:center;gap:.5rem;min-width:0;flex-wrap:wrap}.skill-file-heading strong{font-size:var(--font-size-body);font-weight:500;overflow-wrap:anywhere}
.skill-content-input{flex:1;min-height:120px!important;height:100%;resize:none;font:var(--font-size-body)/1.65 var(--font-mono,monospace);tab-size:2}
.skill-file-info{color:var(--muted)}
.skill-image-preview{min-height:0;max-width:100%;object-fit:contain;flex:1}
.skill-files-back{display:none}.skill-dirty-mark{color:var(--accent)}
.skills-hint{color:var(--muted);font-size:var(--font-size-body);line-height:1.6;margin:0}
.skill-form-footer{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:.8rem;margin-top:1rem}.skill-form-actions{display:flex;gap:.5rem;margin-inline-start:auto}
@media(max-width:830px){.skill-browser{display:block;height:60dvh}.skill-file-tree{height:100%;padding:0;border:0}.skill-file-content{display:none;height:100%}.skill-file-open .skill-file-tree{display:none}.skill-file-open .skill-file-content{display:flex}.skill-files-back{display:inline-flex}.skill-form-footer{gap:.5rem}}
@media(max-height:520px){.skill-browser{height:48dvh}.skill-form-footer{margin-top:.5rem}.skill-content-input{min-height:70px!important}}
</style>
