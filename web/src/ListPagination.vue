<script setup lang="ts">
import { Pagination, Select } from './ui'
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ page: number; pageSize: number; total: number; disabled?: boolean }>()
const emit = defineEmits<{ change: [page: number, pageSize: number] }>()
const { t } = useI18n({ useScope: 'global' })
const sizeID = useId()
const sizes = [10, 20, 50, 100].map(value => ({ label: String(value), value }))
</script>

<template>
  <div v-if="total > 0" class="list-pagination">
    <small role="status">{{ t('usagePage', { start: total ? (page - 1) * pageSize + 1 : 0, end: Math.min(page * pageSize, total), total }) }}</small>
    <Pagination :current="page" :page-size="pageSize" :total="total" :show-less-items="true"
      :show-size-changer="false" :disabled="disabled" @change="emit('change', $event, props.pageSize)" />
    <div class="list-page-size"><label :for="sizeID">{{ t('itemsPerPage') }}</label><Select :id="sizeID" :value="pageSize" :options="sizes"
      :disabled="disabled" :show-search="false" placement="topRight" @change="emit('change', 1, Number($event))" /></div>
  </div>
</template>

<style scoped>
.list-pagination{display:flex;align-items:center;justify-content:flex-end;flex-wrap:wrap;gap:.5rem .75rem;min-width:0;padding:.75rem 0;background:var(--surface);flex:none}.list-pagination>small{margin-inline-end:auto;color:var(--text-secondary)}.list-pagination :deep(.ui-pagination){margin:0;display:flex;flex-wrap:wrap;row-gap:.25rem}.list-page-size{display:flex;align-items:center;gap:.5rem;flex:none;font-size:var(--font-size-body)}.list-page-size :deep(.ui-select){width:76px}
@media(max-width:480px){.list-pagination>small{flex-basis:100%}.list-pagination{justify-content:space-between;gap:.4rem}.list-page-size{gap:.3rem}.list-page-size :deep(.ui-select){width:66px}}
</style>
