<script setup lang="ts">
import { ProgressRange, ProgressRoot, ProgressTrack } from '@ark-ui/vue/progress'
import { computed } from 'vue'

const props = withDefaults(defineProps<{ label: string; value?: number; max?: number }>(), { max: 100 })
const maximum = computed(() => Math.max(1, props.max))
const progressValue = computed(() => props.value === undefined ? null
  : Math.max(0, Math.min(maximum.value, props.value)))
</script>

<template>
  <ProgressRoot :model-value="progressValue" :max="maximum">
    <ProgressTrack class="ui-progress-bar" :aria-label="label">
      <ProgressRange class="ui-progress-bar-fill" />
    </ProgressTrack>
  </ProgressRoot>
</template>

<style scoped>
.ui-progress-bar { position: relative; height: .5rem; border-radius: 999px; overflow: hidden; background: var(--border-subtle); }
.ui-progress-bar-fill { display: block; height: 100%; width: 0; border-radius: inherit; background: var(--accent); transition: width .2s ease; }
.ui-progress-bar-fill[data-state=indeterminate] { width: 40%; transform: translateX(75%); transition: none; animation: ui-progress-pending 1.5s ease-in-out infinite; }
@keyframes ui-progress-pending { from { transform: translateX(-100%); } to { transform: translateX(250%); } }
@media (prefers-reduced-motion: reduce) {
  .ui-progress-bar-fill { transition: none; }
  .ui-progress-bar-fill[data-state=indeterminate] { animation: none; }
}
</style>
