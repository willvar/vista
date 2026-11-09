<script setup lang="ts">
import { Pagination } from '@ark-ui/vue/pagination'
import { IconChevronLeft, IconChevronRight } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
withDefaults(defineProps<{ current?: number; pageSize: number; total: number; disabled?: boolean; showLessItems?: boolean; showSizeChanger?: boolean }>(), { current: 1 })
const emit = defineEmits<{ 'update:current': [page: number]; change: [page: number] }>()
const { t } = useI18n()
function change(page: number) { emit('update:current', page); emit('change', page) }
</script>

<template>
  <Pagination.Root class="ui-pagination" :page="current" :page-size="pageSize" :count="total" :sibling-count="1"
    :translations="{ rootLabel: t('uiPagination'), prevTriggerLabel: t('uiPreviousPage'), nextTriggerLabel: t('uiNextPage'), itemLabel: details => t('uiPage', { page: details.page }) }" @page-change="!disabled && change($event.page)">
    <Pagination.PrevTrigger class="ui-pagination-prev" :disabled="disabled || current <= 1"><IconChevronLeft /></Pagination.PrevTrigger>
    <Pagination.Context v-slot="pagination"><template v-for="(page, index) in pagination.pages" :key="index">
      <Pagination.Item v-if="page.type === 'page'" :value="page.value" type="page" class="ui-pagination-item" :class="`ui-pagination-item-${page.value}`" :disabled="disabled">{{ page.value }}</Pagination.Item>
      <Pagination.Ellipsis v-else :index="index" class="ui-pagination-ellipsis">…</Pagination.Ellipsis>
    </template></Pagination.Context>
    <Pagination.NextTrigger class="ui-pagination-next" :disabled="disabled || current >= Math.ceil(total / pageSize)"><IconChevronRight /></Pagination.NextTrigger>
  </Pagination.Root>
</template>
