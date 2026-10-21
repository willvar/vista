<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Button, Checkbox, Radio, TextArea } from 'antdv-next'
import { CheckOutlined, LeftOutlined, RightOutlined } from '@antdv-next/icons'
import type { QuestionAnswer, QuestionPrompt } from './client/types'
import { useI18n } from 'vue-i18n'

type AnswerDraft = { options: string[]; custom: boolean; text: string }

const props = defineProps<{
  sessionId: string
  runId: string
  questions: QuestionPrompt[]
  busy?: boolean
  submit: (runId: string, answers: QuestionAnswer[]) => Promise<boolean>
}>()

const { t } = useI18n({ useScope: 'global' })
const storageKey = computed(() => `vista-question:${props.sessionId}:${props.runId}`)
const drafts = ref<AnswerDraft[]>([])
const submitting = ref(false)
const activeIndex = ref(0)
const direction = ref(1)
const tabs = ref<HTMLElement | null>(null)
const panel = ref<HTMLElement | null>(null)
let focusAfterSwitch = false
const question = computed(() => props.questions[activeIndex.value])
const panelID = computed(() => `question-panel-${props.runId}-${activeIndex.value}`)

function emptyDrafts() {
  return props.questions.map(() => ({ options: [], custom: false, text: '' }))
}

function readDrafts(): AnswerDraft[] {
  try {
    const raw = sessionStorage.getItem(storageKey.value)
    if (!raw) return emptyDrafts()
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed) || parsed.length !== props.questions.length) return emptyDrafts()
    return props.questions.map((question, index) => {
      const value = parsed[index]
      if (!value || typeof value !== 'object') return { options: [], custom: false, text: '' }
      const candidate = value as { options?: unknown; custom?: unknown; text?: unknown }
      const labels = new Set(question.options.map(option => option.label))
      const options = Array.isArray(candidate.options)
        ? candidate.options.filter((option): option is string => typeof option === 'string' && labels.has(option))
        : []
      const custom = question.custom !== false && candidate.custom === true
      return {
        options: question.multiple ? [...new Set(options)] : custom ? [] : options.slice(0, 1),
        custom,
        text: typeof candidate.text === 'string' ? candidate.text : '',
      }
    })
  } catch { return emptyDrafts() }
}

function readIndex() {
  try {
    const saved = Number(sessionStorage.getItem(storageKey.value + ':index') || '0')
    return Number.isInteger(saved) ? Math.max(0, Math.min(saved, props.questions.length - 1)) : 0
  } catch { return 0 }
}

watch(() => storageKey.value + JSON.stringify(props.questions), () => {
  const index = readIndex()
  drafts.value = readDrafts()
  activeIndex.value = index
  focusAfterSwitch = false
}, { immediate: true })
watch(drafts, value => {
  try { sessionStorage.setItem(storageKey.value, JSON.stringify(value)) } catch { /* Keep answers in memory when storage is unavailable. */ }
}, { deep: true, flush: 'sync' })
watch(activeIndex, value => {
  try { sessionStorage.setItem(storageKey.value + ':index', String(value)) } catch { /* Keep the current question in memory. */ }
}, { flush: 'sync' })

function goTo(index: number, focus = false) {
  if (submitting.value || props.busy || index === activeIndex.value || index < 0 || index >= props.questions.length) return
  direction.value = index > activeIndex.value ? 1 : -1
  focusAfterSwitch = focus
  activeIndex.value = index
}
function focusQuestion() {
  if (focusAfterSwitch) panel.value?.querySelector<HTMLElement>('.question-text')?.focus({ preventScroll: true })
  focusAfterSwitch = false
}
async function navigateTabs(event: KeyboardEvent, index: number) {
  const target = event.key === 'ArrowRight' ? (index + 1) % props.questions.length
    : event.key === 'ArrowLeft' ? (index + props.questions.length - 1) % props.questions.length
      : event.key === 'Home' ? 0 : event.key === 'End' ? props.questions.length - 1 : undefined
  if (target === undefined) return
  event.preventDefault()
  goTo(target)
  await nextTick()
  tabs.value?.querySelector<HTMLButtonElement>(`[data-question-tab="${target}"]`)?.focus()
}

function selected(index: number, label: string) { return drafts.value[index]?.options.includes(label) || false }
function customSelected(index: number) { return !!drafts.value[index]?.custom }
function toggleOption(index: number, label: string, checked: boolean) {
  const question = props.questions[index]
  const draft = drafts.value[index]
  if (!question || !draft) return
  if (question.multiple) {
    const options = checked ? [...draft.options, label] : draft.options.filter(item => item !== label)
    drafts.value = drafts.value.map((item, itemIndex) => itemIndex === index ? { ...item, options: [...new Set(options)] } : item)
    return
  }
  drafts.value = drafts.value.map((item, itemIndex) => itemIndex === index
    ? { ...item, options: checked ? [label] : [], custom: false }
    : item)
}
function toggleCustom(index: number, checked: boolean) {
  const question = props.questions[index]
  if (!question || question.custom === false) return
  drafts.value = drafts.value.map((item, itemIndex) => itemIndex === index
    ? { ...item, custom: checked, ...(checked && !question.multiple ? { options: [] } : {}) }
    : item)
}
function setText(index: number, text: string) {
  drafts.value = drafts.value.map((item, itemIndex) => itemIndex === index ? { ...item, text } : item)
}
function valid(index: number) {
  const draft = drafts.value[index]
  if (!draft) return false
  return (!draft.custom || !!draft.text.trim()) && (draft.options.length > 0 || draft.custom)
}
const canSubmit = computed(() => props.questions.length > 0 && props.questions.every((_, index) => valid(index)))

async function submitAnswers() {
  if (!canSubmit.value || submitting.value || props.busy) return
  submitting.value = true
  const submittedStorageKey = storageKey.value
  const answers = drafts.value.map(draft => ({
    options: draft.options,
    ...(draft.custom && draft.text.trim() ? { text: draft.text.trim() } : {}),
  }))
  try {
    if (await props.submit(props.runId, answers)) {
      try {
        sessionStorage.removeItem(submittedStorageKey)
        sessionStorage.removeItem(submittedStorageKey + ':index')
      } catch { /* Ignore storage failures. */ }
    }
  } finally { submitting.value = false }
}
</script>

<template>
  <form class="question-form" :data-run-id="runId" :data-direction="direction" @submit.prevent="submitAnswers">
    <div v-if="questions.length > 1" class="question-navigation">
      <div ref="tabs" class="question-tabs" role="tablist" :aria-label="t('questionNavigation')">
        <button v-for="(item, index) in questions" :id="`question-tab-${runId}-${index}`" :key="index" type="button" role="tab" class="question-tab" :data-question-tab="index" :data-answered="valid(index) || undefined" :aria-selected="activeIndex === index" :aria-controls="`question-panel-${runId}-${index}`" :aria-label="item.header || t('questionNumber', { index: index + 1 })" :tabindex="activeIndex === index ? 0 : -1" :disabled="submitting || busy" @click="goTo(index)" @keydown="navigateTabs($event, index)">
          <span class="question-tab-number" aria-hidden="true"><CheckOutlined v-if="valid(index)" /><template v-else>{{ index + 1 }}</template></span>
          <span class="question-tab-label">{{ item.header || t('questionNumber', { index: index + 1 }) }}</span>
        </button>
      </div>
      <small class="question-position" role="status">{{ activeIndex + 1 }} / {{ questions.length }}</small>
    </div>
    <div class="question-stage">
      <Transition name="question-page" mode="out-in" @after-enter="focusQuestion">
        <div v-if="question" :id="panelID" :key="`${runId}-${activeIndex}`" ref="panel" :role="questions.length > 1 ? 'tabpanel' : undefined" :aria-labelledby="questions.length > 1 ? `question-tab-${runId}-${activeIndex}` : undefined">
          <fieldset class="question-item" :data-question-index="activeIndex" :disabled="submitting || busy">
            <legend class="question-text" tabindex="-1"><span v-if="question.header && questions.length === 1" class="question-header">{{ question.header }}</span>{{ question.question }}</legend>
            <small class="question-selection-hint">{{ t(question.multiple ? 'questionMultiple' : 'questionSingle') }}</small>
            <div class="question-options">
              <component :is="question.multiple ? Checkbox : Radio" v-for="option in question.options" :key="option.label" class="question-option" :name="`question-${runId}-${activeIndex}`" :checked="selected(activeIndex, option.label)" :disabled="submitting || busy" :aria-label="option.label" @update:checked="toggleOption(activeIndex, option.label, !!$event)">
                <span class="question-option-copy"><strong>{{ option.label }}</strong><small v-if="option.description">{{ option.description }}</small></span>
              </component>
              <component :is="question.multiple ? Checkbox : Radio" v-if="question.custom !== false" class="question-option question-custom-option" :name="`question-${runId}-${activeIndex}`" :checked="customSelected(activeIndex)" :disabled="submitting || busy" :aria-label="t('questionCustomOption')" @update:checked="toggleCustom(activeIndex, !!$event)">
                <span class="question-option-copy"><strong>{{ t('questionCustomOption') }}</strong></span>
              </component>
            </div>
            <TextArea v-if="customSelected(activeIndex)" :value="drafts[activeIndex]?.text || ''" :disabled="submitting || busy" :auto-size="{ minRows: 2, maxRows: 6 }" :aria-label="t('questionCustomAnswer', { question: question.question })" :placeholder="t('questionCustomPlaceholder')" @update:value="setText(activeIndex, String($event))" />
          </fieldset>
        </div>
      </Transition>
    </div>
    <div class="question-form-footer">
      <small>{{ t('questionComposerHint') }}</small>
      <div class="question-form-actions">
        <Button v-if="questions.length > 1" html-type="button" type="text" :disabled="activeIndex === 0 || submitting || busy" @click="goTo(activeIndex - 1, true)"><LeftOutlined aria-hidden="true" />{{ t('questionPrevious') }}</Button>
        <Button v-if="activeIndex < questions.length - 1" html-type="button" type="primary" :disabled="submitting || busy" @click="goTo(activeIndex + 1, true)">{{ t('questionNext') }}<RightOutlined aria-hidden="true" /></Button>
        <Button v-else html-type="submit" type="primary" :disabled="!canSubmit || submitting || busy" :loading="submitting">{{ t('questionSubmit') }}</Button>
      </div>
    </div>
  </form>
</template>

<style scoped>
.question-form{--question-slide:12px;display:flex;flex-direction:column;gap:1.2rem;min-width:0;max-width:100%}.question-form[data-direction="-1"]{--question-slide:-12px}
.question-navigation{display:flex;align-items:center;gap:.75rem;min-width:0}.question-tabs{display:flex;gap:.3rem;min-width:0;overflow-x:auto;overscroll-behavior-x:contain}.question-tab{display:flex;align-items:center;gap:.4rem;flex:none;max-width:12rem;padding:.4rem .6rem;border:0;border-radius:8px;background:transparent;color:var(--muted);font:inherit;font-size:14px;cursor:pointer;transition:background-color .14s,color .14s}.question-tab:hover{background:var(--hover);color:var(--text)}.question-tab[aria-selected="true"]{background:var(--accent-soft);color:var(--accent)}.question-tab:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}.question-tab:disabled{cursor:default;opacity:.6}.question-tab-number{display:inline-flex;align-items:center;justify-content:center;flex:none;width:1.3rem;height:1.3rem;border:1px solid currentColor;border-radius:50%;font-size:12px}.question-tab[data-answered] .question-tab-number{border-color:transparent;color:var(--accent)}.question-tab-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.question-position{flex:none;margin-left:auto;color:var(--muted);font-size:13px;font-variant-numeric:tabular-nums;white-space:nowrap}
.question-stage{min-width:0;overflow:hidden;margin:-3px;padding:3px}.question-page-enter-active,.question-page-leave-active{transition:opacity .12s ease,transform .12s ease}.question-page-leave-active{pointer-events:none}.question-page-enter-from{opacity:0;transform:translateX(var(--question-slide))}.question-page-leave-to{opacity:0;transform:translateX(calc(-1 * var(--question-slide)))}
.question-item{min-width:0;margin:0;padding:0;border:0}.question-header{display:block;margin-bottom:.25rem;color:var(--muted);font-size:14px;font-weight:500}.question-text{float:none;display:block;width:100%;max-width:100%;margin:0 0 .35rem;padding:0;color:var(--text);font-size:17px;line-height:1.5;white-space:pre-wrap;overflow-wrap:anywhere}.question-text:focus{outline:none}.question-selection-hint{display:block;margin-bottom:.6rem;font-size:14px;color:var(--muted)}.question-options{display:grid;gap:.5rem;min-width:0}.question-option{display:flex;align-items:flex-start;gap:.25rem;min-width:0;width:100%;margin:0;padding:.65rem .75rem;border:1px solid var(--border);border-radius:9px;background:var(--surface-subtle);cursor:pointer;white-space:normal}.question-option:focus-within{outline:2px solid var(--accent);outline-offset:2px}.question-option:hover{border-color:var(--accent);background:var(--hover)}.question-option.ant-radio-wrapper-checked,.question-option.ant-checkbox-wrapper-checked{border-color:var(--accent);background:var(--hover)}.question-option:deep(.ant-radio),.question-option:deep(.ant-checkbox){flex:none;margin-top:.2rem;align-self:flex-start}.question-option:deep(.ant-radio-label),.question-option:deep(.ant-checkbox-label){min-width:0;flex:1;white-space:normal;overflow-wrap:anywhere}.question-option-copy{display:flex;flex-direction:column;gap:.2rem;min-width:0;overflow-wrap:anywhere}.question-option-copy strong{font-size:16px;font-weight:500;color:var(--text)}.question-option-copy small{font-size:14px;color:var(--muted);line-height:1.5}.question-item>.ant-input,.question-item textarea{width:100%;margin-top:.65rem}.question-form-footer{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:.75rem}.question-form-footer>small{min-width:0;color:var(--muted);font-size:14px}.question-form-actions{display:flex;align-items:center;justify-content:space-between;gap:.5rem;margin-left:auto}.question-form-actions>.ant-btn:last-child{min-width:6.5rem}
@media(max-width:520px){.question-option{padding:.5rem}.question-tab{gap:.3rem;padding:.4rem .45rem}.question-navigation{gap:.5rem}.question-form-footer{align-items:stretch;flex-direction:column}.question-form-actions{width:100%;margin-left:0}.question-form-actions>.ant-btn:only-child{width:100%}}
@media(prefers-reduced-motion:reduce){.question-tab,.question-page-enter-active,.question-page-leave-active{transition:none}.question-page-enter-from,.question-page-leave-to{transform:none}}
</style>
