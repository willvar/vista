<script setup lang="ts">
import { TooltipPositioner, TooltipTrigger, TooltipContent, TooltipRoot } from '@ark-ui/vue/tooltip'
import type { CSSProperties } from 'vue'
import { placement as position } from './placement'
defineProps<{ title?: string; placement?: string; trigger?: string[]; styles?: { root?: CSSProperties } }>()
</script>

<template>
  <TooltipRoot :positioning="{ placement: position(placement || 'top'), gutter: 7 }" :open-delay="300" :close-delay="100" interactive lazy-mount unmount-on-exit>
    <TooltipTrigger as-child><slot /></TooltipTrigger>
    <Teleport to="body"><TooltipPositioner style="z-index: 1600"><TooltipContent class="ui-tooltip" :style="styles?.root"><slot name="title">{{ title }}</slot></TooltipContent></TooltipPositioner></Teleport>
  </TooltipRoot>
</template>
