<script setup lang="ts">
import { IconInfoCircle, IconAlertCircle, IconCircleCheck } from '@tabler/icons-vue'
withDefaults(defineProps<{ type?: 'error' | 'warning' | 'info' | 'success'; message?: string; description?: string; showIcon?: boolean }>(), { type: 'info' })
</script>

<template>
  <div class="ui-alert" :class="`ui-alert-${type}`" :role="type === 'error' ? 'alert' : 'status'">
    <component v-if="showIcon" :is="type === 'success' ? IconCircleCheck : type === 'info' ? IconInfoCircle : IconAlertCircle" class="ui-alert-icon" aria-hidden="true" />
    <div class="ui-alert-copy"><div class="ui-alert-title">{{ message }}<slot /></div><div v-if="description || $slots.description" class="ui-alert-description"><slot name="description">{{ description }}</slot></div></div>
  </div>
</template>
