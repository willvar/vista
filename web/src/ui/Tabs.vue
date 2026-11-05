<script setup lang="ts">
import { Tabs } from '@ark-ui/vue/tabs'
defineProps<{ activeKey?: string; items: { value: string; label: string }[] }>()
const emit = defineEmits<{ 'update:activeKey': [key: string] }>()
</script>

<template>
  <Tabs.Root class="ui-tabs" :model-value="activeKey" @update:model-value="emit('update:activeKey', $event)">
    <Tabs.List class="ui-tabs-nav"><Tabs.Trigger v-for="item in items" :key="item.value" :value="item.value" class="ui-tabs-tab">{{ item.label }}</Tabs.Trigger><Tabs.Indicator class="ui-tabs-indicator" /></Tabs.List>
    <Tabs.Content v-for="item in items" :key="item.value" :value="item.value" class="ui-tabs-panel"><slot v-if="activeKey === item.value" /></Tabs.Content>
  </Tabs.Root>
</template>
