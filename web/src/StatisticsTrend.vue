<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { defineChart } from '@tanstack/charts/scene'
import type { ChartPoint } from '@tanstack/charts/types'
import { Chart } from '@tanstack/charts/vue'
import { barY } from '@tanstack/charts/bar'
import { lineY } from '@tanstack/charts/line'
import { focusGroupX } from '@tanstack/charts/focus'
import { scaleBand } from '@tanstack/charts/scales/band'
import { scaleLinear } from '@tanstack/charts/scales/linear'
import { calendarDate, type StatisticsDay } from './client/statistics'

const props = defineProps<{ days: StatisticsDay[] }>()
const emit = defineEmits<{ select: [date: string] }>()
const { t, locale } = useI18n({ useScope: 'global' })
const definition = computed(() => {
  const days = props.days
  const compact = new Intl.NumberFormat(locale.value, { notation: 'compact', maximumFractionDigits: 1 })
  const date = new Intl.DateTimeFormat(locale.value, { month: 'short', day: 'numeric' })
  return defineChart({
    marks: [
      barY(days, { id: 'tokens', x: 'date', y: 'tokens', key: 'date', fill: 'var(--accent)', fillOpacity: .7, radius: 2 }),
      lineY(days, { id: 'activity', x: 'date', y: 'activeMinutes', key: 'date', yScale: 'activity', stroke: 'var(--statistics-activity)', strokeWidth: 2 }),
    ],
    scales: {
      x: { scale: () => scaleBand<string>().padding(.3), axis: { line: false, ticks: { size: 0, format: (value: string) => date.format(calendarDate(value)) } } },
      y: { scale: () => scaleLinear().domain([0, Math.max(1, ...days.map(day => day.tokens))]), grid: true,
        axis: { line: false, ticks: { count: 3, size: 0, format: (value: number) => compact.format(value) } } },
      activity: { channel: 'y', side: 'right', scale: () => scaleLinear().domain([0, Math.max(1, ...days.map(day => day.activeMinutes))]),
        axis: { line: false, ticks: { count: 3, size: 0, format: (value: number) => compact.format(value) } } },
    },
  }, { focus: focusGroupX, tooltip: false, svgAnimation: false })
})
function select(point: ChartPoint<StatisticsDay> | null) { if (point) emit('select', point.datum.date) }
</script>

<template>
  <Chart class="statistics-trend-chart" :definition="definition" :height="202" :aria-label="t('statisticsTrend')"
    :aria-description="t('statisticsChartHint')" :on-focus-change="select" :on-select="select" />
</template>

<style scoped>
.statistics-trend-chart { min-width: 0; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
</style>
