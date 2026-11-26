<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, Switch } from './ui'
import { useWorkspace } from './composables/useWorkspace'

const { t } = useI18n({ useScope: 'global' })
const { client, serverStatus, scopeActive } = useWorkspace()
const branching = computed(() => serverStatus.value?.branching === true)
const saving = ref(false)
const error = ref('')

async function setBranching(enabled: boolean) {
  if (saving.value || !scopeActive()) return
  saving.value = true
  error.value = ''
  const observed = serverStatus.value
  try {
    const saved = await client.request<{ branching: boolean }>('/preferences', { method: 'PATCH', body: JSON.stringify({ branching: enabled }) })
    // A newer shared snapshot wins over this response.
    if (scopeActive() && observed && serverStatus.value === observed) serverStatus.value = { ...observed, branching: saved.branching }
  } catch (failure) {
    if (scopeActive()) error.value = String(failure)
  } finally { saving.value = false }
}
</script>

<template>
  <section class="general-setting branching-setting" aria-labelledby="branching-heading">
    <div class="agent-instructions-heading">
      <h3 id="branching-heading">{{ t('branching') }}</h3>
      <Switch :checked="branching" :disabled="saving || serverStatus?.branching === undefined" aria-labelledby="branching-heading" @update:checked="setBranching" />
    </div>
    <Alert v-if="error" type="error" :message="error" />
    <p>{{ t('branchingIntro') }}</p>
    <figure class="personalization-figure branching-figure" aria-labelledby="branching-heading">
      <div class="branching-examples">
        <div class="branching-case" :data-selected="!branching">
          <strong>{{ t('branchingOff') }}</strong>
          <svg viewBox="0 0 160 55" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M15 18H145" /><path class="saved-path" d="M55 18 L86 40 H145" />
            <circle cx="15" cy="18" r="4"/><circle cx="55" cy="18" r="4"/><circle cx="103" cy="18" r="4"/><circle cx="145" cy="18" r="4"/>
          </svg>
          <p>{{ t('branchingOffHint') }}</p>
        </div>
        <div class="branching-case" :data-selected="branching">
          <strong>{{ t('branchingOn') }}</strong>
          <svg viewBox="0 0 160 55" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M15 18H145 M55 18L86 40H145"/>
            <circle cx="15" cy="18" r="4"/><circle cx="55" cy="18" r="4"/><circle cx="145" cy="18" r="4"/><circle cx="145" cy="40" r="4"/>
          </svg>
          <p>{{ t('branchingOnHint') }}</p>
        </div>
      </div>
      <figcaption>{{ t('branchingExistingHint') }}</figcaption>
    </figure>
  </section>
</template>

<style scoped>
.branching-examples { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 170px), 1fr)); gap: 10px; }
.branching-case { min-width: 0; padding: 10px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); }
.branching-case[data-selected='true'] { border-color: var(--accent); }
.branching-case svg { display: block; max-width: 100%; width: 160px; height: 55px; margin: 4px auto; color: var(--accent); }
.branching-case circle { fill: var(--surface); }
.saved-path { stroke-dasharray: 4 4; }
.branching-figure figcaption { margin-top: 10px; color: var(--text-secondary); }
</style>
