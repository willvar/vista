<script setup lang="ts">
import { compactTraceText } from './state/runTrace'

defineProps<{ label: string; status: string; modelName: string; delegatedBy?: string; purpose?: string; purposeExpanded?: boolean }>()
</script>

<template>
  <span class="trace-agent-heading">
    <span class="trace-agent-identity"><span class="trace-agent-role">{{ label }}</span><small class="trace-agent-status">{{ status }}</small></span>
    <span v-if="delegatedBy || modelName" class="trace-agent-meta"><span v-if="delegatedBy" class="trace-agent-delegator">{{ delegatedBy }}</span><span v-if="modelName">{{ modelName }}</span></span>
    <span v-if="purpose" class="trace-agent-purpose" :class="{ expanded: purposeExpanded }" :title="purposeExpanded ? undefined : purpose" @click="purposeExpanded && $event.stopPropagation()">{{ purposeExpanded ? purpose : compactTraceText(purpose, 90) }}</span>
  </span>
</template>

<style scoped>
.trace-agent-heading{display:flex;flex-direction:column;gap:.3rem;min-width:0;max-width:100%;font-weight:400;color:var(--text-secondary)}
.trace-agent-identity{display:flex;align-items:center;gap:.6rem;flex-wrap:wrap;min-width:0}.trace-agent-role{display:inline-flex;align-items:center;padding:.15rem .5rem;border-radius:5px;background:var(--accent-soft);color:var(--accent);font-size:12px;font-weight:500;white-space:nowrap}
.trace-agent-status{color:var(--muted);font-size:12px}.trace-agent-meta{display:flex;flex-wrap:wrap;gap:.25rem .8rem;font-size:12px;color:var(--muted);overflow-wrap:anywhere}.trace-agent-delegator{color:var(--text-secondary)}.trace-agent-purpose{font-size:14px;line-height:1.65;color:var(--text-secondary);overflow-wrap:anywhere}
.trace-agent-purpose.expanded{white-space:pre-wrap;user-select:text;-webkit-user-select:text;cursor:text}
</style>
