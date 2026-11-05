<script setup lang="ts">
import { Tooltip } from '@ark-ui/vue/tooltip'
import type { CSSProperties } from 'vue'
import { placement as position } from './placement'
defineProps<{ title?: string; placement?: string; trigger?: string[]; styles?: { root?: CSSProperties } }>()
</script>

<template>
  <Tooltip.Root :positioning="{ placement: position(placement || 'top'), gutter: 7 }" :open-delay="300" :close-delay="100" interactive lazy-mount unmount-on-exit>
    <Tooltip.Trigger as-child><slot /></Tooltip.Trigger>
    <Teleport to="body"><Tooltip.Positioner style="z-index: 1600"><Tooltip.Content class="ui-tooltip" :style="styles?.root"><slot name="title">{{ title }}</slot></Tooltip.Content></Tooltip.Positioner></Teleport>
  </Tooltip.Root>
</template>
