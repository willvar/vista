<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { SplitterResizeTrigger, SplitterPanel, SplitterRoot } from '@ark-ui/vue/splitter'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ sidebarWidth: number; minimum: number; maximum: number; desktop: boolean }>()
const emit = defineEmits<{ resize: [sizes: number[]]; resizeEnd: [sizes: number[]]; reset: [] }>()
const { t } = useI18n()
const container = ref<HTMLElement>()
const width = ref(window.innerWidth)
const panels = computed(() => [{ id: 'sidebar', minSize: `${props.desktop ? props.minimum : 0}px`, maxSize: `${props.desktop ? props.maximum : 0}px` }, { id: 'conversation', minSize: `${props.desktop ? 480 : 0}px` }])
const size = computed(() => [width.value ? props.sidebarWidth / width.value * 100 : 0, width.value ? 100 - props.sidebarWidth / width.value * 100 : 100])
let observer: ResizeObserver | undefined
function sizes(values: number[]) { return values.map(value => value / 100 * width.value) }
onMounted(() => {
  if (!container.value) return
  observer = new ResizeObserver(() => { if (container.value) width.value = container.value.clientWidth })
  observer.observe(container.value)
})
onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <div ref="container" class="workspace-split-container">
    <SplitterRoot class="workspace-splitter ui-splitter" :panels="panels" :size="size" :keyboard-resize-by="width ? 1000 / width : 1" @resize="desktop && emit('resize', sizes($event.size))" @resize-end="desktop && emit('resizeEnd', sizes($event.size))">
      <SplitterPanel id="sidebar" class="ui-splitter-panel sidebar-split-panel" :style="{ flex: `0 0 ${sidebarWidth}px` }"><slot name="sidebar" /></SplitterPanel>
      <SplitterResizeTrigger v-if="desktop" id="sidebar:conversation" class="ui-splitter-bar ui-splitter-bar-dragger" :aria-label="t('resizeSessionList')" :title="t('resizeSessionListHint')" aria-controls="sessions-panel" @dblclick="emit('reset')" />
      <SplitterPanel id="conversation" class="ui-splitter-panel conversation-split-panel" style="flex: 1"><slot /></SplitterPanel>
    </SplitterRoot>
  </div>
</template>
