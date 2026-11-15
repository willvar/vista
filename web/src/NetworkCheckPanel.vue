<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import SchemaForm from './SchemaForm.vue'
import { Alert as VAlert, Button as VButton } from './ui'
import type { NetworkCheck } from './client/network'
const props = defineProps<{ perform: (target: string, signal: AbortSignal) => Promise<NetworkCheck>; disabled?: boolean; revisionKey?: string | number }>()
const { t } = useI18n({ useScope: 'global' })
const target = ref<Record<string, unknown>>({ target: '' }), valid = ref(false), busy = ref(false), result = ref<NetworkCheck>(), error = ref('')
let pending: AbortController | undefined, generation = 0
const schema = computed(() => ({ type: 'object' as const, additionalProperties: false, required: ['target'], properties: {
  target: { type: 'string' as const, title: t('networkCheckTarget'), minLength: 1, pattern: '^https?://[^/?#@]+(?:/[^?#]*)?$' },
} }))
const uischema = { type: 'VerticalLayout', elements: [{ type: 'Control', scope: '#/properties/target', options: { showErrorsOnBlur: true } }] }
async function check() {
  if (busy.value || props.disabled || !valid.value) return
  const ticket = ++generation, controller = pending = new AbortController()
  busy.value = true; result.value = undefined; error.value = ''
  try { const next = await props.perform(String(target.value.target), controller.signal); if (ticket === generation) result.value = next }
  catch (cause) { if (ticket === generation && !controller.signal.aborted) error.value = cause instanceof Error ? cause.message : String(cause) }
  finally { if (ticket === generation) busy.value = false }
}
function invalidate() { generation++; pending?.abort(); pending = undefined; busy.value = false; result.value = undefined; error.value = '' }
watch(() => props.revisionKey, invalidate)
watch(target, invalidate, { deep: true })
onBeforeUnmount(invalidate)
</script>
<template>
  <section class="network-check">
    <SchemaForm id="network-check" class="network-check-form" v-model="target" :schema="schema" :uischema="uischema" :disabled="busy || disabled" @validity="valid = $event">
      <template #target-after><v-button class="network-check-action" type="button" :loading="busy" :disabled="!valid || disabled" @click="check">{{ t('networkCheck') }}</v-button></template>
    </SchemaForm>
    <v-alert v-if="error" type="error" role="alert" :message="error" />
    <v-alert v-if="result" :type="result.ok ? 'success' : 'error'" :role="result.ok ? 'status' : 'alert'"
      :message="result.ok ? t('networkCheckSuccess', { status: result.httpStatus }) : t('networkCheckFailure', { stage: result.stage, error: result.error })" />
    <p v-if="result" class="network-check-meta">{{ result.route === 'direct' ? t('networkDirect') : result.route }} · {{ result.target }} · {{ new Date(result.checkedAt).toLocaleString() }}</p>
  </section>
</template>
<style scoped>
.network-check { display: flex; flex-direction: column; min-width: 0; border-top: 1px solid var(--border); padding-top: .5rem; }
.network-check > * + * { margin-top: .65rem; }
.network-check p { color: var(--muted); line-height: 1.6; }
.network-check-form :deep(.schema-control) { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: start; }
.network-check-form :deep(.schema-control > label), .network-check-form :deep(.schema-control > small) { grid-column: 1 / -1; }
.network-check-form :deep(.ui-input) { grid-column: 1; grid-row: 2; }
.network-check-action { grid-column: 2; grid-row: 2; }
.network-check-meta { overflow-wrap: anywhere; font-size: var(--font-size-small); }
</style>
