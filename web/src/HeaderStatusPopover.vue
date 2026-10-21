<script setup lang="ts">
import { ref } from 'vue'
import { Button, Popover } from 'antdv-next'
import { useEscapeDismiss } from './composables/useEscape'

const props = defineProps<{ label: string; tooltip: string; triggerClass: string }>()
const open = ref(false)
useEscapeDismiss(open)
</script>

<template>
  <Popover v-model:open="open" trigger="click" placement="bottomRight" root-class="header-status-popover"
    :styles="{ root: { maxWidth: 'calc(100vw - 2rem)' } }">
    <template #content><div class="header-status-detail"><slot /></div></template>
    <Button type="text" class="header-status-trigger" :class="props.triggerClass"
      :aria-label="label" :aria-expanded="open" :title="tooltip"><slot name="icon" /></Button>
  </Popover>
</template>
