<script setup lang="ts">
import { PaginationPrevTrigger, PaginationNextTrigger, PaginationEllipsis, PaginationContext, PaginationRoot, PaginationItem } from '@ark-ui/vue/pagination'
import { IconChevronLeft, IconChevronRight } from '@tabler/icons-vue'
import { useI18n } from 'vue-i18n'
withDefaults(defineProps<{ current?: number; pageSize: number; total: number; disabled?: boolean; showLessItems?: boolean; showSizeChanger?: boolean }>(), { current: 1 })
const emit = defineEmits<{ 'update:current': [page: number]; change: [page: number] }>()
const { t } = useI18n()
function change(page: number) { emit('update:current', page); emit('change', page) }
</script>

<template>
  <PaginationRoot class="ui-pagination" :page="current" :page-size="pageSize" :count="total" :sibling-count="1"
    :translations="{ rootLabel: t('uiPagination'), prevTriggerLabel: t('uiPreviousPage'), nextTriggerLabel: t('uiNextPage'), itemLabel: details => t('uiPage', { page: details.page }) }" @page-change="!disabled && change($event.page)">
    <PaginationPrevTrigger class="ui-pagination-prev" :disabled="disabled || current <= 1"><IconChevronLeft /></PaginationPrevTrigger>
    <PaginationContext v-slot="pagination"><template v-for="(page, index) in pagination.pages" :key="index">
      <PaginationItem v-if="page.type === 'page'" :value="page.value" type="page" class="ui-pagination-item" :class="`ui-pagination-item-${page.value}`" :disabled="disabled">{{ page.value }}</PaginationItem>
      <PaginationEllipsis v-else :index="index" class="ui-pagination-ellipsis">…</PaginationEllipsis>
    </template></PaginationContext>
    <PaginationNextTrigger class="ui-pagination-next" :disabled="disabled || current >= Math.ceil(total / pageSize)"><IconChevronRight /></PaginationNextTrigger>
  </PaginationRoot>
</template>
