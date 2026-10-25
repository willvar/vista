<script setup lang="ts">
import { onErrorCaptured, ref, watch } from 'vue'
const props = defineProps<{ resetKey: string }>()
const emit = defineEmits<{ failure: [error: unknown] }>()
const failed = ref(false)
onErrorCaptured(error => { failed.value = true; emit('failure', error); return false })
watch(() => props.resetKey, () => { failed.value = false })
function retry() { failed.value = false }
</script>

<template><slot v-if="!failed" /><slot v-else name="fallback" :retry="retry" /></template>
