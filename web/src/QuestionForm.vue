<script setup lang="ts">
import { computed, markRaw, nextTick, ref, watch } from 'vue'
import { rankWith, type UISchemaElement } from '@jsonforms/core'
import { Button } from './ui'
import { IconCheck, IconChevronLeft, IconChevronRight } from '@tabler/icons-vue'
import type { QuestionAnswer, QuestionPrompt } from './client/types'
import { useI18n } from 'vue-i18n'
import SchemaForm from './SchemaForm.vue'
import QuestionChoiceControl from './schema/QuestionChoiceControl.vue'
import { questionDefinition, type QuestionData } from './schema/question'

const props = defineProps<{
  sessionId: string
  runId: string
  callId?: string
  questions: QuestionPrompt[]
  busy?: boolean
  embedded?: boolean
  submit: (runId: string, answers: QuestionAnswer[], callId?: string) => Promise<boolean>
}>()
const { t } = useI18n({ useScope: 'global' })
const definition = computed(() => questionDefinition(props.questions))
const fingerprint = computed(() => JSON.stringify(props.questions))
const storageKey = computed(() => `vista-question:${props.sessionId}:${props.runId}${props.callId ? ':' + props.callId : ''}`)
const drafts = ref<QuestionData>(definition.value.empty())
const activeKey = ref('q0')
const submitting = ref(false)
const submissionFailed = ref(false)
const formValid = ref(false)
const direction = ref(1)
const tabs = ref<HTMLElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const stage = ref<HTMLElement | null>(null)
let focusAfterSwitch = false
const entries = computed(() => definition.value.active(drafts.value))
const activeIndex = computed(() => Math.max(0, entries.value.findIndex(entry => entry.key === activeKey.value)))
const question = computed(() => entries.value[activeIndex.value])
const panelID = computed(() => `question-panel-${props.runId}-${question.value?.index ?? 0}`)
const renderers = [{ renderer: markRaw(QuestionChoiceControl), tester: rankWith(5, uischema => !!uischema.options?.question) }]
const uischema = computed<UISchemaElement>(() => ({
  type: 'VerticalLayout', elements: question.value ? [{
    ...question.value.control,
    options: { ...question.value.control.options, showHeader: entries.value.length === 1 },
  }] : [],
}))

function restore() {
  let data = definition.value.empty(), key = 'q0'
  try {
    const raw = sessionStorage.getItem(storageKey.value)
    if (raw) {
      const saved = JSON.parse(raw)
      if (saved && saved.prompt === fingerprint.value && saved.data && typeof saved.data === 'object' && !Array.isArray(saved.data)) {
        data = definition.value.normalize(saved.data)
        if (typeof saved.activeKey === 'string') key = saved.activeKey
      } else if (!props.callId && Array.isArray(saved) && saved.length === props.questions.length) {
        // Only old, identity-less clients use the old positional draft location.
        data = definition.value.normalize(Object.fromEntries(saved.map((draft, index) => [`q${index}`, draft])))
      }
    }
  } catch { /* Keep usable defaults when browser storage is unavailable. */ }
  drafts.value = data
  activeKey.value = definition.value.active(data).some(entry => entry.key === key) ? key : 'q0'
  focusAfterSwitch = false
  submissionFailed.value = false
}
watch(() => storageKey.value + fingerprint.value, restore, { immediate: true })
watch([drafts, activeKey], () => {
  try {
    sessionStorage.setItem(storageKey.value, JSON.stringify({ prompt: fingerprint.value, data: drafts.value, activeKey: activeKey.value }))
  } catch { /* Keep answers in memory when storage is unavailable. */ }
}, { deep: true, flush: 'sync' })
watch(entries, value => {
  if (!value.some(entry => entry.key === activeKey.value)) activeKey.value = value[0]?.key || 'q0'
}, { flush: 'sync' })

function goTo(index: number, focus = false) {
  if (submitting.value || props.busy || index === activeIndex.value || index < 0 || index >= entries.value.length) return
  direction.value = index > activeIndex.value ? 1 : -1
  focusAfterSwitch = focus
  activeKey.value = entries.value[index]!.key
}
function focusQuestion() {
  if (props.embedded && stage.value) stage.value.scrollTop = 0
  if (focusAfterSwitch) panel.value?.querySelector<HTMLElement>('.question-text')?.focus({ preventScroll: true })
  focusAfterSwitch = false
}
async function navigateTabs(event: KeyboardEvent, index: number) {
  const count = entries.value.length
  const target = event.key === 'ArrowRight' ? (index + 1) % count
    : event.key === 'ArrowLeft' ? (index + count - 1) % count
      : event.key === 'Home' ? 0 : event.key === 'End' ? count - 1 : undefined
  if (target === undefined) return
  event.preventDefault()
  goTo(target)
  await nextTick()
  tabs.value?.querySelector<HTMLButtonElement>(`[data-question-tab="${target}"]`)?.focus()
}
function valid(index: number) {
  const entry = entries.value[index]
  return !!entry?.validate(drafts.value[entry.key])
}
const canSubmit = computed(() => formValid.value && entries.value.length > 0 && entries.value.every((_, index) => valid(index)))
async function submitAnswers() {
  if (!canSubmit.value || submitting.value || props.busy) return
  submitting.value = true
  submissionFailed.value = false
  const submittedStorageKey = storageKey.value
  try {
    if (await props.submit(props.runId, definition.value.answers(drafts.value), props.callId)) {
      try {
        sessionStorage.removeItem(submittedStorageKey)
        sessionStorage.removeItem(submittedStorageKey + ':index')
      } catch { /* Ignore storage failures. */ }
    } else submissionFailed.value = true
  } catch { submissionFailed.value = true }
  finally { submitting.value = false }
}
</script>
<template>
  <form class="question-form" :class="{ 'question-form-embedded': embedded }" :aria-label="t('questionAnswerMode')" :data-run-id="runId" :data-call-id="callId" :data-direction="direction" @submit.prevent="submitAnswers">
    <div v-if="entries.length > 1" class="question-navigation">
      <div ref="tabs" class="question-tabs" role="tablist" :aria-label="t('questionNavigation')">
        <button v-for="(item, index) in entries" :id="`question-tab-${runId}-${item.index}`" :key="item.key" type="button" role="tab" class="question-tab" :data-question-tab="index" :data-answered="valid(index) || undefined" :aria-selected="activeIndex === index" :aria-controls="`question-panel-${runId}-${item.index}`" :aria-label="item.prompt.header || t('questionNumber', { index: index + 1 })" :tabindex="activeIndex === index ? 0 : -1" :disabled="submitting || busy" @click="goTo(index)" @keydown="navigateTabs($event, index)">
          <span class="question-tab-number" aria-hidden="true"><IconCheck v-if="valid(index)" /><template v-else>{{ index + 1 }}</template></span>
          <span class="question-tab-label">{{ item.prompt.header || t('questionNumber', { index: index + 1 }) }}</span>
        </button>
      </div>
      <small class="question-position" role="status">{{ activeIndex + 1 }} / {{ entries.length }}</small>
    </div>
    <div ref="stage" class="question-stage">
      <Transition name="question-page" mode="out-in" @after-enter="focusQuestion">
        <div v-if="question" :id="panelID" :key="`${runId}-${question.key}`" ref="panel" :role="entries.length > 1 ? 'tabpanel' : undefined" :aria-labelledby="entries.length > 1 ? `question-tab-${runId}-${question.index}` : undefined">
          <SchemaForm :id="`question-${sessionId}-${runId}-${callId || 'legacy'}`" v-model="drafts" :schema="definition.schema" :uischema="uischema" :renderers="renderers" :normalize-draft="definition.normalize" draft-updates :disabled="submitting || busy" @validity="formValid = $event" />
        </div>
      </Transition>
    </div>
    <p v-if="submissionFailed" class="question-submit-error" role="alert">{{ t('questionSubmitFailed') }}</p>
    <div class="question-form-footer">
      <small v-if="!embedded">{{ t('questionComposerHint') }}</small>
      <div class="question-form-actions">
        <Button v-if="entries.length > 1" type="button" variant="text" :disabled="activeIndex === 0 || submitting || busy" @click="goTo(activeIndex - 1, true)"><IconChevronLeft aria-hidden="true" />{{ t('questionPrevious') }}</Button>
        <Button v-if="activeIndex < entries.length - 1" type="button" variant="primary" :disabled="submitting || busy" @click="goTo(activeIndex + 1, true)">{{ t('questionNext') }}<IconChevronRight aria-hidden="true" /></Button>
        <Button v-else type="submit" variant="primary" :disabled="!canSubmit || submitting || busy" :loading="submitting">{{ t('questionSubmit') }}</Button>
      </div>
    </div>
  </form>
</template>
<style scoped>
.question-form{--question-slide:12px;display:flex;flex-direction:column;gap:1.2rem;min-width:0;max-width:100%}.question-form[data-direction="-1"]{--question-slide:-12px}
.question-navigation{display:flex;align-items:center;gap:.75rem;min-width:0}.question-tabs{display:flex;gap:.3rem;min-width:0;overflow-x:auto;overscroll-behavior-x:contain}.question-tab{display:flex;align-items:center;gap:.4rem;flex:none;max-width:12rem;padding:.4rem .6rem;border:0;border-radius:8px;background:transparent;color:var(--muted);font:inherit;font-size:14px;cursor:pointer;transition:background-color .14s,color .14s}.question-tab:hover{background:var(--hover);color:var(--text)}.question-tab[aria-selected="true"]{background:var(--accent-soft);color:var(--accent)}.question-tab:focus-visible{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:2px}.question-tab:disabled{cursor:default;opacity:.6}.question-tab-number{display:inline-flex;align-items:center;justify-content:center;flex:none;width:1.3rem;height:1.3rem;border:1px solid currentColor;border-radius:50%;font-size:12px}.question-tab[data-answered] .question-tab-number{border-color:transparent;color:var(--accent)}.question-tab-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.question-position{flex:none;margin-left:auto;color:var(--muted);font-size:13px;font-variant-numeric:tabular-nums;white-space:nowrap}
.question-stage{min-width:0;overflow:hidden;margin:-3px;padding:3px}.question-page-enter-active,.question-page-leave-active{transition:opacity .12s ease,transform .12s ease}.question-page-leave-active{pointer-events:none}.question-page-enter-from{opacity:0;transform:translateX(var(--question-slide))}.question-page-leave-to{opacity:0;transform:translateX(calc(-1 * var(--question-slide)))}
.question-form-footer{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:.75rem}.question-form-footer>small{min-width:0;color:var(--muted);font-size:14px}.question-form-actions{display:flex;align-items:center;justify-content:space-between;gap:.5rem;margin-left:auto}.question-form-actions>.ui-btn:last-child{min-width:6.5rem}
@media(max-width:520px){.question-tab{gap:.3rem;padding:.4rem .45rem}.question-navigation{gap:.5rem}.question-form-footer{align-items:stretch;flex-direction:column}.question-form-actions{width:100%;margin-left:0}.question-form-actions>.ui-btn:only-child{width:100%}}
@media(prefers-reduced-motion:reduce){.question-tab,.question-page-enter-active,.question-page-leave-active{transition:none}.question-page-enter-from,.question-page-leave-to{transform:none}}
.question-submit-error{flex:none;margin:0;color:var(--danger);font-size:14px;line-height:1.5;overflow-wrap:anywhere}
.question-form-embedded{flex:1 1 auto;min-height:0;gap:.65rem;overflow:hidden}
.question-form-embedded .question-navigation,.question-form-embedded .question-form-footer{flex:none}
.question-form-embedded .question-stage{flex:1 1 auto;min-height:0;overflow:auto;overscroll-behavior:contain;scrollbar-gutter:stable}
@media(max-height:520px){.question-form-embedded{gap:.35rem}.question-form-embedded .question-form-footer{gap:.35rem}}
</style>
