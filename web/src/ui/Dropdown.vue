<script setup lang="ts">
import { Menu } from '@ark-ui/vue/menu'
defineProps<{ trigger?: string[]; menu: { items: { key: string; label: string; disabled?: boolean; danger?: boolean }[]; onClick?: (item: { key: string }) => void } }>()
</script>

<template>
  <Menu.Root :positioning="{ placement: 'bottom-start', gutter: 6 }" lazy-mount unmount-on-exit @select="menu.onClick?.({ key: $event.value })">
    <Menu.Trigger as-child><slot /></Menu.Trigger>
    <Teleport to="body"><Menu.Positioner style="z-index: 1500"><Menu.Content class="ui-menu"><Menu.Item v-for="item in menu.items" :key="item.key" :value="item.key" :disabled="item.disabled" class="ui-menu-item" :class="{ 'ui-menu-item-danger': item.danger }">{{ item.label }}</Menu.Item></Menu.Content></Menu.Positioner></Teleport>
  </Menu.Root>
</template>
