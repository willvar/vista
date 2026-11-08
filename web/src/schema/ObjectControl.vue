<script setup lang="ts">
import { computed } from 'vue'
import { Generate, type ControlElement } from '@jsonforms/core'
import { DispatchRenderer, rendererProps, useJsonFormsControl } from '@jsonforms/vue'
const props = defineProps(rendererProps<ControlElement>())
const { control } = useJsonFormsControl(props)
const detail = computed(() => Generate.uiSchema(control.value.schema, 'VerticalLayout', undefined, control.value.rootSchema))
</script>
<template>
  <fieldset v-if="control.visible" class="schema-object">
    <legend v-if="control.label">{{ control.label }}</legend>
    <DispatchRenderer :schema="control.schema" :uischema="detail" :path="control.path" :enabled="control.enabled"
      :renderers="control.renderers" :cells="control.cells" />
  </fieldset>
</template>
<style scoped>
.schema-object { border: 1px solid var(--border); border-radius: .5rem; padding: .75rem; margin: 0; min-width: 0; }
.schema-object legend { padding: 0 .3rem; }
</style>
