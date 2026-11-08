<script setup lang="ts">
import { computed, inject, type Slots } from 'vue'
import { useI18n } from 'vue-i18n'
import { rendererProps, useJsonFormsControl } from '@jsonforms/vue'
import type { ControlElement } from '@jsonforms/core'
import type { SelectGroup, SelectOption } from '../ui/types'
import Input from '../ui/Input.vue'
import InputPassword from '../ui/InputPassword.vue'
import InputNumber from '../ui/InputNumber.vue'
import Select from '../ui/Select.vue'
import Switch from '../ui/Switch.vue'
import TextArea from '../ui/TextArea.vue'
const props = defineProps(rendererProps<ControlElement>())
const { control, handleChange } = useJsonFormsControl(props)
const { t } = useI18n({ useScope: 'global' })
const prefix = inject('vista-schema-editor-id', 'configuration')
const fieldSlots = inject<Slots>('vista-schema-form-slots', {})
const before = computed(() => fieldSlots[`${control.value.path}-before`])
const after = computed(() => fieldSlots[`${control.value.path}-after`])
const id = computed(() => `${prefix}-${control.value.path || 'value'}-input`)
const options = computed(() => control.value.uischema.options ?? {})
const choices = computed<(SelectOption | SelectGroup)[]>(() => {
  const values: SelectOption[] = (control.value.schema.enum ?? []).map(value => ({ value: String(value), label: String(options.value.enumLabels?.[String(value)] ?? value), fullLabel: options.value.enumFullLabels?.[String(value)], searchText: String(options.value.enumSearchText?.[String(value)] ?? options.value.enumLabels?.[String(value)] ?? value) }))
  const groups = options.value.enumGroups as { key: string; label: string; values: string[] }[] | undefined
  if (!groups?.length) return values
  const grouped = new Set(groups.flatMap(group => group.values.map(String)))
  return [...groups.map(group => ({ key: group.key, label: group.label, options: group.values.flatMap(value => values.filter(option => String(option.value) === String(value))) })), ...values.filter(option => !grouped.has(String(option.value)))]
})
const secret = computed(() => 'writeOnly' in control.value.schema && control.value.schema.writeOnly === true || options.value.secret === true)
const describedBy = computed(() => [options.value.descriptionIds, control.value.description ? id.value + '-description' : '', control.value.errors ? id.value + '-error' : ''].filter(Boolean).join(' ') || undefined)
// Object-level `required` means the property must exist, not that its value
// must be nonempty. Empty text and false switches are valid settings values.
const requiresValue = computed(() => {
  if (!control.value.required) return false
  const schema = control.value.schema
  if (schema.type === 'boolean' || Array.isArray(schema.type) && schema.type.includes('null')) return false
  if (schema.type === 'string') return (schema.minLength ?? 0) > 0 || !!schema.enum?.length && !schema.enum.includes('')
  return true
})
const numeric = computed(() => control.value.schema.type === 'integer' || control.value.schema.type === 'number')
function update(value: unknown) { handleChange(control.value.path, value) }
function select(value: unknown) { update(control.value.schema.enum?.find(option => String(option) === value)) }
</script>
<template>
  <div v-if="control.visible" class="schema-control" :class="{ 'schema-control-invalid': !!control.errors, 'schema-control-switch': control.schema.type === 'boolean' && !choices.length }" :data-field-path="control.path">
    <component v-if="before" :is="before" />
    <label :for="id">{{ control.label }}<span v-if="requiresValue" class="schema-required" aria-hidden="true"> *</span></label>
    <Select v-if="choices.length" :id="id" :value="control.data == null ? null : String(control.data)" :options="choices" :disabled="!control.enabled" :show-search="!!options.showSearch" :option-filter-prop="options.enumSearchText ? 'searchText' : 'label'" :option-label-prop="options.enumFullLabels ? 'fullLabel' : undefined" :placeholder="options.placeholder"
      :class="{ 'ui-input-error': !!control.errors }" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-required="requiresValue || undefined" :aria-describedby="describedBy" @update:value="select" />
    <Switch v-else-if="control.schema.type === 'boolean'" :id="id" :checked="control.data === true" :disabled="!control.enabled" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-describedby="describedBy" @update:checked="update" />
    <InputNumber v-else-if="numeric" :id="id" :value="typeof control.data === 'number' ? control.data : null" :min="control.schema.minimum" :max="control.schema.maximum"
      :step="control.schema.type === 'integer' ? 1 : undefined" :clamp-value-on-blur="false" :disabled="!control.enabled"
      :class="{ 'ui-input-error': !!control.errors }" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-required="requiresValue || undefined" :aria-describedby="describedBy" @update:value="update($event === null ? undefined : $event)" />
    <InputPassword v-else-if="secret" :id="id" :value="typeof control.data === 'string' ? control.data : ''" :disabled="!control.enabled"
      :maxlength="control.schema.maxLength" :placeholder="options.placeholder" :class="{ 'ui-input-error': !!control.errors }" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-required="requiresValue || undefined" :aria-describedby="describedBy" autocomplete="new-password" @update:value="update" />
    <TextArea v-else-if="options.multi" :id="id" :value="typeof control.data === 'string' ? control.data : ''" :rows="options.rows ?? 6" :disabled="!control.enabled"
      :maxlength="control.schema.maxLength" :placeholder="options.placeholder" :class="{ 'ui-input-error': !!control.errors }" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-required="requiresValue || undefined" :aria-describedby="describedBy" @update:value="update" />
    <Input v-else :id="id" :value="typeof control.data === 'string' ? control.data : ''" :disabled="!control.enabled" :maxlength="control.schema.maxLength" :placeholder="options.placeholder"
      :class="{ 'ui-input-error': !!control.errors }" :aria-label="control.label" :aria-invalid="!!control.errors" :aria-required="requiresValue || undefined" :aria-describedby="describedBy" autocomplete="off" spellcheck="false" @update:value="update" />
    <small v-if="control.description" :id="id + '-description'">{{ control.description }}</small>
    <small v-if="control.errors" :id="id + '-error'" class="schema-error" role="status">{{ control.errors }}</small>
    <component v-if="after" :is="after" />
  </div>
</template>
<style scoped>
.schema-control { display: flex; flex-direction: column; gap: .5rem; min-width: 0; }
.schema-control-switch { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; column-gap: 1rem; }
.schema-control-switch > label { grid-column: 1; grid-row: 1; }
.schema-control-switch > :deep(.ui-switch) { grid-column: 2; grid-row: 1; }
.schema-control-switch > small { grid-column: 1; }
.schema-control :deep(.ui-input-number), .schema-control :deep(.ui-select) { width: 100%; }
.schema-control small { color: var(--muted); line-height: 1.5; white-space: pre-wrap; }
.schema-required { color: var(--danger); }
.schema-control-invalid :deep(.ui-input-affix-wrapper),.schema-control-invalid :deep(.ui-input-number),.schema-control-invalid :deep(.ui-select-selector),.schema-control-invalid :deep(.ui-select-control) { border-color: var(--danger); box-shadow: 0 0 0 1px var(--danger); }
.schema-control .schema-error { color: var(--danger); }
</style>
