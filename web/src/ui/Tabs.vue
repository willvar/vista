<script setup lang="ts">
import { TabIndicator, TabTrigger, TabContent, TabsRoot, TabList } from '@ark-ui/vue/tabs'
defineProps<{ activeKey?: string; items: { value: string; label: string }[] }>()
const emit = defineEmits<{ 'update:activeKey': [key: string] }>()
</script>

<template>
  <TabsRoot class="ui-tabs" :model-value="activeKey" @update:model-value="emit('update:activeKey', $event)">
    <TabList class="ui-tabs-nav"><TabTrigger v-for="item in items" :key="item.value" :value="item.value" class="ui-tabs-tab">{{ item.label }}</TabTrigger><TabIndicator class="ui-tabs-indicator" /></TabList>
    <TabContent v-for="item in items" :key="item.value" :value="item.value" class="ui-tabs-panel"><slot v-if="activeKey === item.value" /></TabContent>
  </TabsRoot>
</template>
