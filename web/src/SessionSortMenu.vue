<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { MenuRoot, MenuTrigger, MenuPositioner, MenuContent, MenuRadioItemGroup, MenuRadioItem, MenuItemText, MenuItemIndicator } from '@ark-ui/vue/menu'
import { IconCheck, IconSortAscending, IconSortDescending } from '@tabler/icons-vue'
import { Button as VButton } from './ui'
import type { SessionSort } from '@vista/client'

const sort = defineModel<SessionSort>({ required: true })
defineProps<{ disabled?: boolean }>()
const { t } = useI18n({ useScope: 'global' })
const sortOptions = computed(() => [
  { value: 'created-desc', label: t('sessionSortCreatedNewest') },
  { value: 'created-asc', label: t('sessionSortCreatedOldest') },
  { value: 'updated-desc', label: t('sessionSortUpdatedNewest') },
  { value: 'updated-asc', label: t('sessionSortUpdatedOldest') },
  { value: 'title-asc', label: t('sessionSortTitleAZ') },
])
const sortTitle = computed(() => `${t('sortSessions')}: ${sortOptions.value.find(option => option.value === sort.value)!.label}`)
</script>

<template>
<MenuRoot :positioning="{ placement: 'bottom-end', gutter: 6 }" lazy-mount unmount-on-exit>
  <MenuTrigger as-child>
    <v-button variant="text" class="icon-button session-sort-trigger" :aria-label="t('sortSessions')" :title="sortTitle"
      :disabled="disabled">
      <IconSortAscending v-if="sort.endsWith('-asc')" aria-hidden="true" /><IconSortDescending v-else aria-hidden="true" />
    </v-button>
  </MenuTrigger>
  <Teleport to="body"><MenuPositioner style="z-index: 1500">
    <MenuContent class="ui-menu session-sort-menu" :aria-label="t('sortSessions')">
      <MenuRadioItemGroup v-model="sort">
        <MenuRadioItem v-for="option in sortOptions" :key="option.value" :value="option.value" class="ui-menu-item">
          <MenuItemText>{{ option.label }}</MenuItemText>
          <MenuItemIndicator><IconCheck aria-hidden="true" /></MenuItemIndicator>
        </MenuRadioItem>
      </MenuRadioItemGroup>
    </MenuContent>
  </MenuPositioner></Teleport>
</MenuRoot>
</template>
