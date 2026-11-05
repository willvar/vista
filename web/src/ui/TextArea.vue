<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, type AriaAttributes } from 'vue'
import type { TextAreaRef } from './types'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ value?: string; rows?: number; autoSize?: boolean | { minRows?: number; maxRows?: number }; disabled?: boolean; status?: 'error' | 'warning' }>()
const emit = defineEmits<{ 'update:value': [value: string]; change: [event: Event] }>()
const input = ref<HTMLTextAreaElement>()
const bounds = computed(() => typeof props.autoSize === 'object' ? props.autoSize : { minRows: 1, maxRows: 12 })
let originalSize: { height: string; overflowY: string } | undefined
function resize() {
  const element = input.value
  if (!element) return
  if (!props.autoSize) {
    if (originalSize) { Object.assign(element.style, originalSize); originalSize = undefined }
    return
  }
  originalSize ??= { height: element.style.height, overflowY: element.style.overflowY }
  const style = getComputedStyle(element)
  const line = parseFloat(style.lineHeight) || 24
  const border = parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth)
  const padding = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + border
  // Measure separately so autosizing never resets the visible height mid-transition.
  const measurement = element.cloneNode() as HTMLTextAreaElement
  measurement.removeAttribute('id')
  measurement.removeAttribute('name')
  measurement.setAttribute('aria-hidden', 'true')
  measurement.tabIndex = -1
  Object.assign(measurement.style, { position: 'absolute', visibility: 'hidden', pointerEvents: 'none', width: style.width,
    height: '0', minHeight: '0', maxHeight: 'none', overflow: 'hidden', transition: 'none' })
  measurement.value = element.value
  element.after(measurement)
  const contentHeight = measurement.scrollHeight + border
  measurement.remove()
  const maxHeight = line * (bounds.value.maxRows ?? 12) + padding
  element.style.height = `${Math.min(maxHeight, Math.max(contentHeight, line * (bounds.value.minRows ?? 1) + padding))}px`
  element.style.overflowY = contentHeight > maxHeight ? 'auto' : 'hidden'
}
watch([() => props.value, () => !!props.autoSize, () => bounds.value.minRows, () => bounds.value.maxRows], resize, { flush: 'post' })
let observer: ResizeObserver | undefined
onMounted(() => {
  const element = input.value
  if (!element) return
  resize()
  let width = element.clientWidth
  observer = new ResizeObserver(() => {
    if (element.clientWidth === width) return
    width = element.clientWidth
    resize()
  })
  observer.observe(element)
})
onBeforeUnmount(() => observer?.disconnect())
function update(event: Event) { emit('update:value', (event.target as HTMLTextAreaElement).value) }
function focus(options?: FocusOptions & { cursor?: 'end' | 'start' }) {
  input.value?.focus(options)
  if (options?.cursor && input.value) {
    const end = options.cursor === 'end' ? input.value.value.length : 0
    input.value.setSelectionRange(end, end)
  }
}
defineExpose<TextAreaRef>({ focus, get input() { return input.value } })
</script>

<template>
  <textarea ref="input" v-bind="$attrs" class="ui-input ui-textarea" :class="{ 'ui-input-error': status === 'error' }" :aria-invalid="status === 'error' || ($attrs['aria-invalid'] as AriaAttributes['aria-invalid'])" :value="value" :disabled="disabled" :rows="autoSize ? bounds.minRows ?? 1 : rows"
    @input="update" @change="emit('change', $event)" />
</template>
