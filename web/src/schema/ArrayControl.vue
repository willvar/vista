<script setup lang="ts">
import { computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { DispatchRenderer, rendererProps, useJsonFormsControl } from '@jsonforms/vue'
import { Generate, composePaths, createDefaultValue, type ControlElement, type JsonSchema, type UISchemaElement } from '@jsonforms/core'
import Button from '../ui/Button.vue'
import Input from '../ui/Input.vue'
import TextArea from '../ui/TextArea.vue'
import Select from '../ui/Select.vue'
const props = defineProps(rendererProps<ControlElement>())
const { control, handleChange } = useJsonFormsControl(props)
const { t } = useI18n({ useScope: 'global' })
const prefix = inject('vista-schema-editor-id', 'configuration')
const values = computed<unknown[]>(() => Array.isArray(control.value.data) ? control.value.data : [])
const itemSchema = computed(() => (Array.isArray(control.value.schema.items) ? {} : control.value.schema.items ?? {}) as JsonSchema)
const choices = computed(() => (itemSchema.value.enum ?? []).map(value => ({ value: String(value), label: String(control.value.uischema.options?.enumLabels?.[String(value)] ?? value) })))
const itemUI = computed<UISchemaElement>(() => control.value.uischema.options?.detail ?? Generate.uiSchema(itemSchema.value, 'VerticalLayout', undefined, control.value.rootSchema))
const reorderOnly = computed(() => control.value.uischema.options?.reorderOnly === true)
const displayValue = (value: unknown) => String(control.value.uischema.options?.enumLabels?.[String(value)] ?? value)
const canAdd = computed(() => control.value.enabled && (control.value.schema.maxItems === undefined || values.value.length < control.value.schema.maxItems))
function update(next: unknown[]) { handleChange(control.value.path, next) }
function edit(index: number, value: unknown) { const next = [...values.value]; next[index] = value; update(next) }
function add() { const value = itemSchema.value.enum?.find(item => !values.value.includes(item)) ?? itemSchema.value.default ?? (itemSchema.value.type === 'object' ? createDefaultValue(itemSchema.value, control.value.rootSchema) : ''); update([...values.value, value]) }
function move(index: number, offset: number) { const next = [...values.value]; const target = index + offset; if (target < 0 || target >= next.length) return; [next[index], next[target]] = [next[target], next[index]]; update(next) }
function label(index: number) { return t('configEditorItem', { name: control.value.label, index: index + 1 }) }
</script>
<template>
  <fieldset v-if="control.visible" class="schema-array" :class="{ 'schema-array-invalid': !!control.errors }" :data-field-path="control.path">
    <legend>{{ control.label }}</legend>
    <small v-if="control.uischema.options?.description" class="schema-array-description">{{ control.uischema.options.description }}</small>
    <div v-for="(value, index) in values" :key="index" class="schema-array-row">
      <span v-if="reorderOnly" class="schema-order-item"><span class="schema-order-number" aria-hidden="true">{{ index + 1 }}</span>{{ displayValue(value) }}</span>
      <DispatchRenderer v-else-if="itemSchema.type === 'object'" :schema="itemSchema" :uischema="itemUI" :path="composePaths(control.path, String(index))" :enabled="control.enabled" :renderers="control.renderers" :cells="control.cells" />
      <Select v-else-if="choices.length" :id="`${prefix}-${control.path}-${index}`" :value="String(value)" :options="choices" :disabled="!control.enabled" :aria-label="label(index)" @update:value="edit(index, itemSchema.enum?.find(option => String(option) === $event))" />
      <TextArea v-else-if="typeof value === 'string' && (control.uischema.options?.multi || /[\r\n]/.test(value))" :value="value" :rows="2" :disabled="!control.enabled" :aria-label="label(index)" @update:value="edit(index, $event)" />
      <Input v-else :value="typeof value === 'string' ? value : JSON.stringify(value)" :disabled="!control.enabled" :aria-label="label(index)" @update:value="edit(index, $event)" />
      <div class="schema-array-actions">
        <Button type="button" :disabled="!control.enabled || index === 0" :aria-label="t('configEditorMoveUp', { index: index + 1 })" @click="move(index, -1)">↑</Button>
        <Button type="button" :disabled="!control.enabled || index === values.length - 1" :aria-label="t('configEditorMoveDown', { index: index + 1 })" @click="move(index, 1)">↓</Button>
        <Button v-if="!reorderOnly" type="button" :disabled="!control.enabled || values.length <= (control.schema.minItems ?? 0)" :aria-label="t('configEditorRemove', { index: index + 1 })" @click="update(values.filter((_, at) => at !== index))">×</Button>
      </div>
    </div>
    <Button v-if="!reorderOnly" type="button" :disabled="!canAdd" @click="add">{{ t('configEditorAdd') }}</Button>
    <small v-if="control.errors" class="schema-error" role="status">{{ control.errors }}</small>
  </fieldset>
</template>
<style scoped>
.schema-array { display: flex; flex-direction: column; gap: .5rem; min-width: 0; padding: .75rem; border: 1px solid var(--border); border-radius: .5rem; margin: 0; }
.schema-array-row { display: flex; align-items: flex-start; gap: .5rem; min-width: 0; }
.schema-array-row > :first-child { flex: 1; min-width: 0; }
.schema-order-item { display: flex; align-items: center; gap: .65rem; min-height: var(--control-height); }
.schema-order-number { min-width: 1.5rem; color: var(--muted); font-variant-numeric: tabular-nums; }
.schema-array-actions { display: flex; gap: .25rem; flex: none; }
.schema-array > .ui-btn { align-self: flex-start; }
.schema-array-invalid { border-color: var(--danger); box-shadow: 0 0 0 1px var(--danger); }
.schema-array-description { color: var(--muted); line-height: 1.5; }
.schema-error { color: var(--danger); }
@media (max-width: 480px) { .schema-array-row { flex-wrap: wrap; } .schema-array-row > :first-child { flex-basis: 100%; } }
</style>
