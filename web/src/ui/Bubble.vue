<script setup lang="ts">
defineProps<{ content?: string; streaming?: boolean; placement?: 'start' | 'end'; variant?: 'filled' | 'borderless'; styles?: { body?: Record<string, string | undefined> } }>()
</script>

<template>
  <div class="ui-bubble" :class="`ui-bubble-${placement || 'start'}`"><div class="ui-bubble-body" :style="styles?.body">
    <div v-if="$slots.header" class="ui-bubble-header"><slot name="header" /></div>
    <div class="ui-bubble-content" :class="`ui-bubble-content-${variant || 'filled'}`"><slot name="content">{{ content }}</slot></div>
    <div v-if="$slots.footer" class="ui-bubble-footer"><slot name="footer" /></div>
  </div></div>
</template>
