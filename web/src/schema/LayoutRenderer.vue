<script setup lang="ts">
import { computed } from 'vue'
import { DispatchRenderer, rendererProps, useJsonFormsLayout } from '@jsonforms/vue'
import type { Layout } from '@jsonforms/core'
const props = defineProps(rendererProps<Layout>())
const { layout } = useJsonFormsLayout(props)
const label = computed(() => 'label' in layout.value.uischema ? String(layout.value.uischema.label ?? '') : '')
</script>
<template>
  <div v-if="layout.visible" class="schema-layout" :class="{ 'schema-group': !!label }">
    <strong v-if="label">{{ label }}</strong>
    <DispatchRenderer v-for="(element, index) in layout.uischema.elements" :key="index" :schema="layout.schema" :uischema="element"
      :path="layout.path" :enabled="layout.enabled" :renderers="layout.renderers" :cells="layout.cells" />
  </div>
</template>
<style scoped>
.schema-layout { display: flex; flex-direction: column; gap: .85rem; min-width: 0; }
.schema-group { padding: .75rem; border: 1px solid var(--border); border-radius: .5rem; }
</style>
