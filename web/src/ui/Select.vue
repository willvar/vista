<script setup lang="ts">
import { computed, ref, useAttrs } from 'vue'
import { Combobox, createListCollection } from '@ark-ui/vue/combobox'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { IconChevronDown, IconX, IconCheck } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
import { placement as position } from './placement'
import type { CSSProperties } from 'vue'
import type { SelectOption, SelectGroup } from './types'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{
  value?: string | number | (string | number)[] | null
  options?: (SelectOption | SelectGroup)[]; disabled?: boolean; placeholder?: string; showSearch?: boolean
  optionFilterProp?: 'searchText' | 'label'; optionLabelProp?: 'fullLabel'; allowClear?: boolean
  mode?: 'tags'; variant?: 'borderless'; placement?: string; virtual?: boolean; id?: string; sameWidth?: boolean
  styles?: { content?: CSSProperties }
}>(), { options: () => [], optionFilterProp: 'label', virtual: true, sameWidth: true })
const emit = defineEmits<{ 'update:value': [value: string | number | (string | number)[] | undefined]; change: [value: string | number | (string | number)[] | undefined] }>()
const attrs = useAttrs()
const { t } = useI18n()
const open = ref(false)
const search = ref('')
const text = ref('')
const scrolling = ref<HTMLElement>()
const control = ref<{ $el: HTMLElement }>()
const multiple = computed(() => props.mode === 'tags')
const editable = computed(() => !!props.showSearch || multiple.value)
const allOptions = computed(() => props.options.flatMap((item, index) => 'options' in item
  ? item.options.map(option => ({ ...option, group: item.label, groupKey: `group:${item.key ?? index}` }))
  : [{ ...item, group: '', groupKey: `option:${index}` }]))
const selected = computed(() => (Array.isArray(props.value) ? props.value : props.value == null ? [] : [props.value]).map(String))
function label(value: string) {
  const item = allOptions.value.find(item => String(item.value) === value)
  return item ? props.optionLabelProp === 'fullLabel' ? item.fullLabel || item.label : item.label : value
}
const selectedLabel = computed(() => selected.value.map(label).join(', '))
const filtered = computed(() => {
  const query = editable.value ? search.value.trim().toLocaleLowerCase() : ''
  return allOptions.value.filter(option => !query || String(option[props.optionFilterProp] || option.label).toLocaleLowerCase().includes(query))
})
const collection = computed(() => createListCollection({ items: filtered.value, itemToValue: item => String(item.value), itemToString: item => item.label, isItemDisabled: item => !!item.disabled }))
const virtualized = computed(() => filtered.value.length > 80 && props.virtual !== false)
const groupHeadingHeight = 32
function startsGroup(index: number) {
  const item = filtered.value[index]
  return !!item?.group && item.groupKey !== filtered.value[index - 1]?.groupKey
}
const virtualizer = useVirtualizer(computed(() => ({ count: virtualized.value ? filtered.value.length : 0, getScrollElement: () => scrolling.value || null,
  estimateSize: (index: number) => 40 + (startsGroup(index) ? groupHeadingHeight : 0),
  getItemKey: (index: number) => JSON.stringify([filtered.value[index]!.value, startsGroup(index)]), overscan: 5 })))
const rows = computed(() => virtualized.value
  ? virtualizer.value.getVirtualItems().map(row => ({ item: filtered.value[row.index]!, index: row.index, top: row.start, heading: startsGroup(row.index) }))
  : filtered.value.map((item, index) => ({ item, index, top: 0, heading: startsGroup(index) })))
const groups = computed(() => {
  const result: { key: string; label: string; rows: typeof rows.value }[] = []
  for (const row of rows.value) {
    let group = result.at(-1)
    if (group?.key !== row.item.groupKey) {
      group = { key: row.item.groupKey, label: row.item.group, rows: [] }
      result.push(group)
    }
    group!.rows.push(row)
  }
  return result.map(group => ({ ...group, heading: group.rows.find(row => row.heading) }))
})
function update(values: string[]) {
  const originals = values.map(value => allOptions.value.find(item => String(item.value) === value)?.value ?? value)
  const value = multiple.value ? originals : originals[0]
  emit('update:value', value); emit('change', value)
}
function openChange(value: boolean) {
  open.value = value
  if (!value) { search.value = ''; text.value = '' }
}
function inputChange(details: { inputValue: string; reason?: string }) {
  if (!editable.value) return
  if (details.reason === 'input-change') { text.value = details.inputValue; search.value = details.inputValue }
  else if (details.reason === 'item-select') { text.value = ''; search.value = '' }
}
function setScrolling(element: unknown) { scrolling.value = (element as { $el?: HTMLElement } | null)?.$el }
function requestDismiss(event: CustomEvent<{ targetLayer?: HTMLElement }>) {
  const parent = event.detail.targetLayer
  // A neighboring hover popover can finish closing after this list opens.
  // Its layer cleanup must only dismiss lists actually nested inside it.
  if (parent?.dataset.scope === 'popover' && !parent.contains(control.value?.$el || null)) event.preventDefault()
}
function addTag(event: KeyboardEvent) {
  if (!multiple.value || event.key !== 'Enter' || event.isComposing || event.keyCode === 229) return
  const value = text.value.trim()
  if (!value || filtered.value.some(item => String(item.value) === value)) return
  event.preventDefault(); event.stopPropagation()
  update([...new Set([...selected.value, value])]); text.value = ''; search.value = ''
}
</script>

<template>
  <Combobox.Root class="ui-select" :class="[attrs.class, { 'ui-select-borderless': variant === 'borderless', 'ui-select-multiple': multiple, 'ui-select-disabled': disabled }]" :style="attrs.style"
    :collection="collection" :model-value="selected" :input-value="editable && (multiple || open) ? text : selectedLabel" :open="open" :multiple="multiple" :allow-custom-value="multiple" :disabled="disabled"
    :close-on-select="!multiple" open-on-click :ids="id ? { input: id } : undefined"
    :positioning="{ placement: position(placement), sameWidth, gutter: 5 }"
    :translations="{ triggerLabel: t('uiToggleOptions'), clearTriggerLabel: t('uiClear'), resultsCount: count => t('uiResultsCount', { count }), noResults: t('traceNoMatches'), loading: t('uiLoading') }"
    :scroll-to-index-fn="virtualized ? details => virtualizer.scrollToIndex(details.index) : undefined" lazy-mount unmount-on-exit
    @open-change="openChange($event.open)" @input-value-change="inputChange" @update:model-value="update">
    <Combobox.Control ref="control" class="ui-select-selector">
      <template v-if="multiple"><span v-for="value in selected" :key="value" class="ui-select-tag">{{ label(value) }}<button type="button" :aria-label="`${t('remove')} ${label(value)}`" :disabled="disabled" @pointerdown.prevent @click.stop="update(selected.filter(item => item !== value))">×</button></span></template>
      <span v-else class="ui-select-selection-item" :style="[{ visibility: open && editable ? 'hidden' : undefined }, styles?.content]">{{ selectedLabel || placeholder }}</span>
      <Combobox.Input v-bind="{ ...attrs, class: undefined, style: undefined }" class="ui-select-input" :style="!multiple && (!open || !editable) ? { color: 'transparent', caretColor: 'transparent' } : undefined"
        :readonly="!editable" :aria-autocomplete="editable ? 'list' : 'none'" :placeholder="multiple ? placeholder : open && editable ? selectedLabel || placeholder : undefined" @keydown.capture="addTag" />
      <button v-if="allowClear && selected.length" type="button" class="ui-select-clear" :aria-label="t('uiClear')" :disabled="disabled" @pointerdown.prevent @click.stop="update([])"><IconX /></button>
      <Combobox.Trigger class="ui-select-arrow" tabindex="-1"><IconChevronDown /></Combobox.Trigger>
    </Combobox.Control>
    <Teleport to="body"><Combobox.Positioner style="z-index: 1500"><Combobox.Content :ref="setScrolling" class="ui-select-dropdown" :data-virtualized="virtualized || undefined" @layer:request-dismiss="requestDismiss">
      <Combobox.Empty class="ui-select-empty">{{ t('traceNoMatches') }}</Combobox.Empty>
      <div :style="virtualized ? { height: `${virtualizer.getTotalSize()}px`, position: 'relative' } : undefined">
        <component :is="group.label ? Combobox.ItemGroup : 'div'" v-for="group in groups" :key="group.key" class="ui-select-item-group">
          <Combobox.ItemGroupLabel v-if="group.label" class="ui-select-group-label" :class="{ 'visually-hidden': virtualized && !group.heading }" :title="group.label"
            :style="virtualized && group.heading ? { position: 'absolute', top: `${group.heading.top}px`, left: 0, right: 0, height: `${groupHeadingHeight}px` } : undefined">{{ group.label }}</Combobox.ItemGroupLabel>
          <Combobox.Item v-for="row in group.rows" :key="String(row.item.value)" :item="row.item" :index="row.index" class="ui-select-item-option"
            :style="virtualized ? { position: 'absolute', top: `${row.top + (row.heading ? groupHeadingHeight : 0)}px`, left: 0, right: 0, height: '40px' } : undefined">
            <Combobox.ItemText class="ui-select-item-option-content">{{ row.item.label }}</Combobox.ItemText><Combobox.ItemIndicator><IconCheck /></Combobox.ItemIndicator>
          </Combobox.Item>
        </component>
      </div>
    </Combobox.Content></Combobox.Positioner></Teleport>
  </Combobox.Root>
</template>
