<script setup lang="ts">
import { CollapsibleContent, CollapsibleRoot, CollapsibleTrigger } from '@ark-ui/vue/collapsible'
import { IconChevronRight } from '@tabler/icons-vue'

defineProps<{ title: string }>()
</script>

<template>
  <CollapsibleRoot class="ui-collapsible">
    <CollapsibleTrigger class="ui-collapsible-trigger"><IconChevronRight class="ui-collapsible-indicator" aria-hidden="true" /><span>{{ title }}</span></CollapsibleTrigger>
    <CollapsibleContent class="ui-collapsible-content"><slot /></CollapsibleContent>
  </CollapsibleRoot>
</template>

<style scoped>
.ui-collapsible { min-width: 0; }
.ui-collapsible-trigger { display: flex; align-items: baseline; gap: .35rem; max-width: 100%; padding: 0; border: 0; border-radius: 4px; background: none; color: inherit; font: inherit; text-align: start; overflow-wrap: anywhere; cursor: pointer; touch-action: manipulation; }
.ui-collapsible-trigger > span { min-width: 0; }
.ui-collapsible-trigger:hover { color: var(--text); }
.ui-collapsible-trigger:focus-visible { outline: var(--focus-outline, 2px solid var(--accent)); outline-offset: 3px; }
.ui-collapsible-trigger[data-state="open"] > .ui-collapsible-indicator { transform: rotate(90deg); }
.ui-collapsible-content { padding-top: .5rem; }
</style>
