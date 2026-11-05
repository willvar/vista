<script setup lang="ts">
import { computed, ref } from 'vue'
import { Collapsible } from '@ark-ui/vue/collapsible'
import { DownOutlined } from '@antdv-next/icons'
const props = withDefaults(defineProps<{ expanded?: boolean; defaultExpanded?: boolean; title?: string; triggerLabel?: string; loading?: boolean; classes?: { status?: string } }>(), { expanded: undefined, defaultExpanded: false })
const emit = defineEmits<{ 'update:expanded': [open: boolean]; expand: [open: boolean] }>()
const inner = ref(props.defaultExpanded)
const open = computed(() => props.expanded ?? inner.value)
function change(value: boolean) { inner.value = value; emit('update:expanded', value); emit('expand', value) }
</script>

<template>
  <Collapsible.Root class="ui-think" :open="open" lazy-mount unmount-on-exit @open-change="change($event.open)">
    <Collapsible.Trigger as-child><div class="ui-think-status-wrapper" :class="classes?.status" :aria-label="triggerLabel || title" role="button" tabindex="0" @keydown.enter.prevent="change(!open)" @keydown.space.prevent="change(!open)">
      <span class="ui-think-status-icon" aria-hidden="true"><span v-if="loading" class="ui-spinner" /><svg v-else viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" stroke-width="1.4"><ellipse cx="12" cy="12" rx="10" ry="4.5" transform="rotate(45 12 12)" /><ellipse cx="12" cy="12" rx="10" ry="4.5" transform="rotate(-45 12 12)" /><circle cx="12" cy="12" r="1.5" fill="currentColor" stroke="none" /></svg></span>
      <div class="ui-think-status-text"><slot name="title">{{ title }}</slot></div>
      <DownOutlined class="ui-think-status-down-icon" :class="{ expanded: open }" aria-hidden="true" />
    </div></Collapsible.Trigger>
    <Collapsible.Content class="ui-think-content ui-collapse"><slot /></Collapsible.Content>
  </Collapsible.Root>
</template>
