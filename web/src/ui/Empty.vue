<script setup lang="ts">
defineProps<{ description?: string }>()
</script>

<template>
  <div class="ui-empty">
    <svg class="ui-empty-image" width="64" height="44" viewBox="0 0 64 44" aria-hidden="true"><path d="M9 27 20 10h24l11 17v11H9Z" fill="var(--surface-subtle)" stroke="var(--border-strong)" stroke-linejoin="round" /><path d="M9 27h15l3 5h10l3-5h15" fill="none" stroke="var(--border-strong)" /><path d="M24 18h16m-20 6h24" stroke="var(--border)" stroke-linecap="round" /></svg>
    <div class="ui-empty-description"><slot name="description">{{ description }}</slot></div><div v-if="$slots.default" class="ui-empty-actions"><slot /></div>
  </div>
</template>
