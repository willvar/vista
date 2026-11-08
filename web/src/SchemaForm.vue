<script setup lang="ts">
import { computed, provide, ref, useSlots, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { createAjv, isBooleanControl, isIntegerControl, isNumberControl, isObjectControl, isStringControl, rankWith, schemaTypeIs, uiTypeIs, type JsonFormsI18nState, type JsonSchema, type UISchemaElement } from '@jsonforms/core'
import { JsonForms, type JsonFormsChangeEvent } from '@jsonforms/vue'
import { Alert as VAlert, Button as VButton } from './ui'
import ConfirmPopover from './ConfirmPopover.vue'
import ScalarControl from './schema/ScalarControl.vue'
import ArrayControl from './schema/ArrayControl.vue'
import ObjectControl from './schema/ObjectControl.vue'
import LayoutRenderer from './schema/LayoutRenderer.vue'
import { additionalErrors, validationMessage, type FormIssue } from './schema/validation'
const props = defineProps<{ id: string; modelValue: Record<string, unknown>; schema: JsonSchema; uischema?: UISchemaElement; disabled?: boolean; resetKey?: string | number; validate?: (value: Record<string, unknown>) => FormIssue[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: Record<string, unknown>]; validity: [valid: boolean]; dirty: [dirty: boolean] }>()
const { t, locale } = useI18n({ useScope: 'global' })
provide('vista-schema-editor-id', props.id)
provide('vista-schema-form-slots', useSlots())
const copy = (value: Record<string, unknown>) => JSON.parse(JSON.stringify(value)) as Record<string, unknown>
// Key order is irrelevant to equality, but arrays keep their priority/order.
const signature = (value: unknown) => JSON.stringify(value, (_, item: unknown) => item && typeof item === 'object' && !Array.isArray(item)
  ? Object.fromEntries(Object.keys(item).sort().map(key => [key, (item as Record<string, unknown>)[key]])) : item)
const data = ref(copy(props.modelValue))
const baseline = ref(signature(props.modelValue))
const conflict = ref(false), discardOpen = ref(false)
let emitting = false
const pendingEchoes: string[] = []
const ajv = createAjv({ useDefaults: false, coerceTypes: false, removeAdditional: false, strictNumbers: true, logger: false })
const validate = computed(() => { try { return ajv.compile(props.schema) } catch { return undefined } })
const customErrors = computed(() => additionalErrors(props.validate?.(data.value) ?? []))
const i18n = computed<JsonFormsI18nState>(() => ({ locale: locale.value, translateError: error => validationMessage(error, t) }))
const valid = computed(() => !conflict.value && !!validate.value?.(data.value) && customErrors.value.length === 0)
const dirty = computed(() => signature(data.value) !== baseline.value)
const renderers = Object.freeze([
  { renderer: ScalarControl, tester: rankWith(1, (uischema, schema, context) => isStringControl(uischema, schema, context) || isIntegerControl(uischema, schema, context) || isNumberControl(uischema, schema, context) || isBooleanControl(uischema, schema, context)) },
  { renderer: ArrayControl, tester: rankWith(2, schemaTypeIs('array')) },
  { renderer: ObjectControl, tester: rankWith(2, isObjectControl) },
  ...['VerticalLayout', 'HorizontalLayout', 'Group'].map(type => ({ renderer: LayoutRenderer, tester: rankWith(1, uiTypeIs(type)) })),
])
function formChange(event: JsonFormsChangeEvent) {
  if (!event.data || typeof event.data !== 'object' || Array.isArray(event.data) || signature(event.data) === signature(data.value)) return
  data.value = event.data
  if (!valid.value || props.disabled) return
  pendingEchoes.push(signature(data.value)!)
  emitting = true
  try { emit('update:modelValue', copy(data.value)) } finally { emitting = false }
}
function reset(value = props.modelValue) {
  pendingEchoes.length = 0
  data.value = copy(value); baseline.value = signature(value); conflict.value = false; discardOpen.value = false
}
watch(() => signature(props.modelValue), value => {
  // Vue can echo an earlier valid edit after the user has already entered the
  // next field. Acknowledge own updates rather than flagging a remote conflict.
  const echo = pendingEchoes.lastIndexOf(value!)
  if (echo >= 0) { pendingEchoes.splice(0, echo + 1); return }
  if (emitting || value === signature(data.value)) return
  if (dirty.value) conflict.value = true
  else reset()
}, { flush: 'sync' })
watch(() => props.resetKey, () => reset())
watch(valid, value => emit('validity', value), { immediate: true, flush: 'sync' })
watch(dirty, value => emit('dirty', value), { immediate: true, flush: 'sync' })
defineExpose({ reset, dirty, valid })
</script>
<template>
  <div class="schema-form" :data-editor="id" :data-config-form="id">
    <JsonForms :data="data" :schema="schema" :uischema="uischema" :renderers="renderers" :ajv="ajv" :i18n="i18n" :additional-errors="customErrors" :readonly="disabled" @change="formChange" />

    <v-alert v-if="!validate" type="error" role="alert" :message="t('configFormUnavailable')" />
    <v-alert v-if="conflict" type="warning" role="alert" :message="t('configEditorChanged')" />
    <ConfirmPopover v-if="conflict" :show="discardOpen" :message="t('configEditorDiscard')" :cancel-text="t('cancel')" :confirm-text="t('configEditorReload')" :busy="disabled" popover-class="schema-form-discard"
      @update:show="discardOpen = $event" @confirm="reset()"><template #trigger><v-button type="button" :disabled="disabled">{{ t('configEditorReload') }}</v-button></template></ConfirmPopover>
  </div>
</template>
<style scoped>
.schema-form { display: flex; flex-direction: column; gap: .65rem; min-width: 0; }
</style>
