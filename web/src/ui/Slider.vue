<script setup lang="ts">
import { SliderMarkerGroup, SliderHiddenInput, SliderControl, SliderMarker, SliderTrack, SliderRange, SliderThumb, SliderRoot } from '@ark-ui/vue/slider'
import { onBeforeUnmount, ref } from 'vue'
import Tooltip from './Tooltip.vue'
const props = withDefaults(defineProps<{ value?: number; min?: number; max?: number; step?: number; dots?: boolean; disabled?: boolean; tooltip?: { formatter: (index?: number) => string }; ariaLabelForHandle?: string; ariaValueTextFormatterForHandle?: (value: number) => string }>(), { min: 0, max: 100, step: 1, value: 0 })
const emit = defineEmits<{ 'update:value': [value: number] }>()
// Only pointer movement suppresses the value transition. A track click still
// animates to the new value; Ark remains authoritative about an active drag.
const moving = ref(false)
let tracking: AbortController | undefined
function endPointer() { moving.value = false; tracking?.abort(); tracking = undefined }
function trackPointer(event: PointerEvent) {
  endPointer()
  if (props.disabled || event.button !== 0) return
  const root = event.currentTarget as HTMLElement
  tracking = new AbortController()
  const options = { signal: tracking.signal }
  window.addEventListener('pointermove', move => {
    if (move.pointerId === event.pointerId && root.hasAttribute('data-dragging')) moving.value = true
  }, options)
  const finish = (end: PointerEvent) => { if (end.pointerId === event.pointerId) endPointer() }
  window.addEventListener('pointerup', finish, options)
  window.addEventListener('pointercancel', finish, options)
  window.addEventListener('blur', endPointer, options)
}
onBeforeUnmount(endPointer)
</script>

<template>
  <SliderRoot class="ui-slider" :class="{ 'ui-slider-moving': moving }" :model-value="[value]" :min="min" :max="max" :step="step" thumb-alignment="center" :disabled="disabled" :aria-label="[ariaLabelForHandle || '']"
    :get-aria-value-text="details => ariaValueTextFormatterForHandle?.(details.value) || String(details.value)" @value-change="emit('update:value', $event.value[0]!)"
    @pointerdown.capture="trackPointer" @value-change-end="endPointer">
    <SliderControl class="ui-slider-control"><SliderTrack class="ui-slider-rail"><SliderRange class="ui-slider-track" /></SliderTrack>
      <SliderMarkerGroup v-if="dots" class="ui-slider-markers"><SliderMarker v-for="index in Math.floor((max - min) / step) + 1" :key="index" :value="min + (index - 1) * step" class="ui-slider-dot" :class="{ 'ui-slider-dot-active': min + (index - 1) * step <= value }" /></SliderMarkerGroup>
      <Tooltip v-if="tooltip && !disabled" :title="tooltip.formatter(value)"><SliderThumb :index="0" class="ui-slider-handle"><SliderHiddenInput /></SliderThumb></Tooltip>
      <SliderThumb v-else :index="0" class="ui-slider-handle"><SliderHiddenInput /></SliderThumb>
    </SliderControl>
  </SliderRoot>
</template>
