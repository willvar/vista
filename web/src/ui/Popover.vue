<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { PopoverPositioner, PopoverTrigger, PopoverContent, PopoverRoot } from '@ark-ui/vue/popover'
import type { CSSProperties } from 'vue'
import { placement as position } from './placement'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{ open?: boolean; trigger?: string | string[]; placement?: string; rootClass?: string; zIndex?: number; styles?: { root?: CSSProperties } }>(), { open: undefined, trigger: 'click', zIndex: 1500 })
const emit = defineEmits<{ 'update:open': [open: boolean]; 'open-change': [open: boolean] }>()
const inner = ref(false)
const pinned = ref(false)
const triggerElement = ref<{ $el: HTMLElement }>()
const contentElement = ref<{ $el: HTMLElement }>()
const visible = computed(() => props.open ?? inner.value)
const hover = computed(() => props.trigger.includes('hover'))
let timer: ReturnType<typeof setTimeout> | undefined
function change(open: boolean) { clearTimeout(timer); if (!open) pinned.value = false; inner.value = open; emit('update:open', open); emit('open-change', open) }
function enter() { clearTimeout(timer); if (hover.value && !visible.value) timer = setTimeout(() => change(true), 100) }
function leave() { if (hover.value && !pinned.value) timer = setTimeout(() => change(false), 120) }
function click(event: MouseEvent) {
  if (!hover.value) return
  event.preventDefault(); event.stopPropagation()
  if (props.trigger.includes('click')) { pinned.value = !pinned.value; change(pinned.value) }
}
function finalFocus() {
  const active = document.activeElement
  // Keep focus on an outside control the user has just chosen, including
  // another popover. Hover-only dismissals must not focus and reopen a trigger.
  if (active instanceof HTMLElement && active !== document.body && !contentElement.value?.$el.contains(active)) return active
  return triggerElement.value?.$el || null
}
function requestDismiss(event: CustomEvent<{ targetLayer?: HTMLElement }>) {
  const parent = event.detail.targetLayer
  // Ark's layer stack also includes independent popovers opened later.
  // A closing sibling must not dismiss this one as if it were a child.
  if (parent?.dataset.scope === 'popover' && !parent.contains(triggerElement.value?.$el || null)) event.preventDefault()
}
watch(visible, open => { if (!open) { clearTimeout(timer); pinned.value = false } })
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <PopoverRoot :open="visible" :positioning="{ placement: position(placement), gutter: 8, overlap: true }" :auto-focus="false" :final-focus-el="finalFocus" lazy-mount unmount-on-exit @open-change="change($event.open)" @request-dismiss="requestDismiss">
    <PopoverTrigger ref="triggerElement" as-child @mouseenter="enter" @mouseleave="leave" @click.capture="click"><slot /></PopoverTrigger>
    <Teleport to="body"><PopoverPositioner :style="{ zIndex }"><PopoverContent ref="contentElement" v-bind="$attrs" class="ui-popover" :class="rootClass" :style="styles?.root" @mouseenter="enter" @mouseleave="leave">
      <div class="ui-popover-inner"><slot name="content" /></div>
    </PopoverContent></PopoverPositioner></Teleport>
  </PopoverRoot>
</template>
