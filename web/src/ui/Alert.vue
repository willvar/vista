<script setup lang="ts">
import { InfoCircleOutlined, ExclamationCircleOutlined, CheckCircleOutlined } from '@antdv-next/icons'
withDefaults(defineProps<{ type?: 'error' | 'warning' | 'info' | 'success'; message?: string; description?: string; showIcon?: boolean }>(), { type: 'info' })
</script>

<template>
  <div class="ui-alert" :class="`ui-alert-${type}`" :role="type === 'error' ? 'alert' : 'status'">
    <component v-if="showIcon" :is="type === 'success' ? CheckCircleOutlined : type === 'info' ? InfoCircleOutlined : ExclamationCircleOutlined" class="ui-alert-icon" aria-hidden="true" />
    <div class="ui-alert-copy"><div class="ui-alert-title">{{ message }}<slot /></div><div v-if="description || $slots.description" class="ui-alert-description"><slot name="description">{{ description }}</slot></div></div>
  </div>
</template>
