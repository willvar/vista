<script setup lang="ts">
import { computed } from 'vue'
import { IconChevronRight } from '@tabler/icons-vue'
import { compactTraceText } from './state/runTrace'

const props = defineProps<{ label: string; status: string; modelName: string; delegatedBy?: string; purpose?: string }>()
const preview = computed(() => compactTraceText(props.purpose, 90))
const longPurpose = computed(() => preview.value !== (props.purpose || '').replace(/\s+/g, ' ').trim())
</script>

<template>
  <div class="trace-agent-heading">
    <span class="trace-agent-identity"><span class="trace-agent-role">{{ label }}</span><small class="trace-agent-status">{{ status }}</small></span>
    <span v-if="delegatedBy || modelName" class="trace-agent-meta"><span v-if="delegatedBy" class="trace-agent-delegator">{{ delegatedBy }}</span><span v-if="modelName">{{ modelName }}</span></span>
    <details v-if="purpose && longPurpose" class="trace-agent-assignment">
      <summary><IconChevronRight aria-hidden="true" /><span>{{ preview }}</span></summary>
      <p class="trace-agent-purpose">{{ purpose }}</p>
    </details>
    <p v-else-if="purpose" class="trace-agent-purpose">{{ purpose }}</p>
  </div>
</template>

<style scoped>
.trace-agent-heading{display:flex;flex-direction:column;gap:.3rem;min-width:0;max-width:100%;font-weight:400;color:var(--text-secondary)}
.trace-agent-identity{display:flex;align-items:center;gap:.6rem;flex-wrap:wrap;min-width:0}.trace-agent-role{display:inline-flex;align-items:center;padding:.15rem .5rem;border-radius:5px;background:var(--accent-soft);color:var(--accent);font-size:12px;font-weight:500;white-space:nowrap}
.trace-agent-status{color:var(--muted);font-size:12px}.trace-agent-meta{display:flex;flex-wrap:wrap;gap:.25rem .8rem;font-size:12px;color:var(--muted);overflow-wrap:anywhere}.trace-agent-delegator{color:var(--text-secondary)}
.trace-agent-purpose{margin:0;white-space:pre-wrap;font-size:14px;line-height:1.65;color:var(--text-secondary);overflow-wrap:anywhere;user-select:text}
.trace-agent-assignment{min-width:0;font-size:14px;line-height:1.65}
.trace-agent-assignment summary{display:flex;align-items:baseline;gap:.35rem;cursor:pointer;list-style:none;overflow-wrap:anywhere}
.trace-agent-assignment summary::-webkit-details-marker{display:none}
.trace-agent-assignment summary:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:3px;border-radius:4px}
.trace-agent-assignment summary:hover{color:var(--text)}
.trace-agent-assignment summary svg{transition:transform .16s}
.trace-agent-assignment[open] summary svg{transform:rotate(90deg)}
.trace-agent-assignment[open] .trace-agent-purpose{margin-top:.5rem}
@media(prefers-reduced-motion:reduce){.trace-agent-assignment summary svg{transition:none}}
</style>
