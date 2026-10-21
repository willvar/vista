<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ content: string }>()
// A single large text node can exhaust Chromium's line-layout memory on a
// narrow viewport. Inline fragments keep the same text and wrapping flow.
const chunks = computed(() => {
  const result: string[] = []
  for (let start = 0; start < props.content.length;) {
    let end = Math.min(start + 4096, props.content.length)
    const last = props.content.charCodeAt(end - 1)
    if (end < props.content.length && last >= 0xd800 && last <= 0xdbff) end--
    result.push(props.content.slice(start, end))
    start = end
  }
  return result
})
</script>

<template><pre class="trace-output"><span v-for="(chunk, index) in chunks" :key="index">{{ chunk }}</span></pre></template>
