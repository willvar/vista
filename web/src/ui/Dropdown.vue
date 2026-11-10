<script setup lang="ts">
import { MenuPositioner, MenuTrigger, MenuContent, MenuRoot, MenuItem } from '@ark-ui/vue/menu'
defineProps<{ trigger?: string[]; menu: { items: { key: string; label: string; disabled?: boolean; danger?: boolean }[]; onClick?: (item: { key: string }) => void } }>()
</script>

<template>
  <MenuRoot :positioning="{ placement: 'bottom-start', gutter: 6 }" lazy-mount unmount-on-exit @select="menu.onClick?.({ key: $event.value })">
    <MenuTrigger as-child><slot /></MenuTrigger>
    <Teleport to="body"><MenuPositioner style="z-index: 1500"><MenuContent class="ui-menu"><MenuItem v-for="item in menu.items" :key="item.key" :value="item.key" :disabled="item.disabled" class="ui-menu-item" :class="{ 'ui-menu-item-danger': item.danger }">{{ item.label }}</MenuItem></MenuContent></MenuPositioner></Teleport>
  </MenuRoot>
</template>
