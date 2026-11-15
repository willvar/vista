<script setup lang="ts">
import { computed, inject } from 'vue'
import { rendererProps, useJsonFormsControl } from '@jsonforms/vue'
import type { ControlElement } from '@jsonforms/core'
import { useI18n } from 'vue-i18n'
import { Checkbox, Radio, TextArea } from '../ui'
import type { QuestionPrompt } from '../client/types'
import { emptyQuestionDraft, optionValue, type QuestionDraft } from './question'

const props = defineProps(rendererProps<ControlElement>())
const { control, handleChange } = useJsonFormsControl(props)
const { t } = useI18n({ useScope: 'global' })
const prefix = inject('vista-schema-editor-id', 'question')
const prompt = computed(() => control.value.uischema.options!.question as QuestionPrompt)
const index = computed(() => Number(control.value.uischema.options!.questionIndex))
const draft = computed(() => control.value.data as QuestionDraft || emptyQuestionDraft())
const error = computed(() => draft.value.custom || draft.value.options.length ? control.value.errors : '')
function update(value: Partial<QuestionDraft>) {
  if (control.value.enabled) handleChange(control.value.path, { ...draft.value, ...value })
}
function toggleOption(at: number, checked: boolean) {
  const value = optionValue(prompt.value, at)
  if (prompt.value.multiple) update({ options: checked ? [...new Set([...draft.value.options, value])] : draft.value.options.filter(option => option !== value) })
  else update({ options: checked ? [value] : [], custom: false })
}
function toggleCustom(checked: boolean) {
  update({ custom: checked, ...(checked && !prompt.value.multiple ? { options: [] } : {}) })
}
</script>
<template>
  <fieldset v-if="control.visible" class="question-item" :data-question-index="index" :data-question-id="prompt.id" :disabled="!control.enabled" :aria-describedby="error ? `${prefix}-${index}-error` : undefined">
    <legend class="question-text" tabindex="-1"><span v-if="prompt.header && control.uischema.options?.showHeader" class="question-header">{{ prompt.header }}</span>{{ prompt.question }}</legend>
    <small class="question-selection-hint">{{ t(prompt.multiple ? 'questionMultiple' : 'questionSingle') }}</small>
    <div class="question-options">
      <component :is="prompt.multiple ? Checkbox : Radio" v-for="(option, at) in prompt.options" :key="option.id || option.label" class="question-option" :name="`${prefix}-${index}`" :checked="draft.options.includes(optionValue(prompt, at))" :disabled="!control.enabled" :aria-label="option.label" @update:checked="toggleOption(at, !!$event)">
        <span class="question-option-copy"><strong>{{ option.label }}</strong><small v-if="option.description">{{ option.description }}</small></span>
      </component>
      <component :is="prompt.multiple ? Checkbox : Radio" v-if="prompt.custom !== false" class="question-option question-custom-option" :name="`${prefix}-${index}`" :checked="draft.custom" :disabled="!control.enabled" :aria-label="t('questionCustomOption')" @update:checked="toggleCustom(!!$event)">
        <span class="question-option-copy"><strong>{{ t('questionCustomOption') }}</strong></span>
      </component>
    </div>
    <TextArea v-if="draft.custom" :value="draft.text" :disabled="!control.enabled" :auto-size="{ minRows: 2, maxRows: 6 }" :aria-label="t('questionCustomAnswer', { question: prompt.question })" :placeholder="t('questionCustomPlaceholder')" :aria-invalid="!!error || undefined" @update:value="update({ text: String($event) })" />
    <small v-if="draft.custom && control.uischema.options?.hasDependents" class="question-follow-up">{{ t('questionCustomFollowUp') }}</small>
    <small v-if="error" :id="`${prefix}-${index}-error`" class="question-error" role="status">{{ error }}</small>
  </fieldset>
</template>
<style scoped>
.question-item{min-width:0;margin:0;padding:0;border:0}.question-header{display:block;margin-bottom:.25rem;color:var(--muted);font-size:14px;font-weight:500}.question-text{float:none;display:block;width:100%;max-width:100%;margin:0 0 .35rem;padding:0;color:var(--text);font-size:17px;line-height:1.5;white-space:pre-wrap;overflow-wrap:anywhere}.question-text:focus{outline:none}.question-selection-hint{display:block;margin-bottom:.6rem;font-size:14px;color:var(--muted)}.question-options{display:grid;gap:.5rem;min-width:0}.question-option{display:flex;align-items:flex-start;gap:.25rem;min-width:0;width:100%;margin:0;padding:.65rem .75rem;border:1px solid var(--border);border-radius:9px;background:var(--surface-subtle);cursor:pointer;white-space:normal}.question-option:focus-within{outline:var(--focus-outline, 2px solid var(--accent));outline-offset:2px}.question-option:hover{border-color:var(--accent);background:var(--hover)}.question-option.ui-radio-wrapper-checked,.question-option.ui-checkbox-wrapper-checked{border-color:var(--accent);background:var(--hover)}.question-option:deep(.ui-radio),.question-option:deep(.ui-checkbox){flex:none;margin-top:.2rem;align-self:flex-start}.question-option:deep(.ui-radio-label),.question-option:deep(.ui-checkbox-label){min-width:0;flex:1;white-space:normal;overflow-wrap:anywhere}.question-option-copy{display:flex;flex-direction:column;gap:.2rem;min-width:0;overflow-wrap:anywhere}.question-option-copy strong{font-size:16px;font-weight:500;color:var(--text)}.question-option-copy small{font-size:14px;color:var(--muted);line-height:1.5}.question-item>.ui-input,.question-item textarea{width:100%;margin-top:.65rem}.question-follow-up,.question-error{display:block;margin-top:.5rem;line-height:1.5;overflow-wrap:anywhere}.question-follow-up{color:var(--muted)}.question-error{color:var(--danger)}
@media(max-width:520px){.question-option{padding:.5rem}}
</style>
