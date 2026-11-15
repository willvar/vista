<script setup lang="ts">
import { TabIndicator, TabTrigger, TabContent, TabsRoot, TabList } from '@ark-ui/vue/tabs'
import type { TabItem } from './types'
withDefaults(defineProps<{ activeKey?: string; items: TabItem[]; preserveContent?: boolean; label?: string; orientation?: 'horizontal' | 'vertical' }>(), { orientation: 'horizontal' })
const emit = defineEmits<{ 'update:activeKey': [key: string] }>()
</script>

<template>
  <TabsRoot class="ui-tabs" :model-value="activeKey" :orientation="orientation" :lazy-mount="false" :unmount-on-exit="false" @update:model-value="emit('update:activeKey', $event)">
    <TabList class="ui-tabs-nav" :aria-label="label"><TabTrigger v-for="item in items" :key="item.value" :value="item.value" :disabled="item.disabled" class="ui-tabs-tab" :aria-invalid="item.invalid || undefined">{{ item.label }}<span v-if="item.invalid" class="ui-tabs-error" aria-hidden="true">•</span></TabTrigger><TabIndicator class="ui-tabs-indicator" /></TabList>
    <TabContent v-for="item in items" :key="item.value" :value="item.value" class="ui-tabs-panel"><slot v-if="preserveContent || activeKey === item.value" :name="$slots[item.value] ? item.value : 'default'" /></TabContent>
  </TabsRoot>
</template>
