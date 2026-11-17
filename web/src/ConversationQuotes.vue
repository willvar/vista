<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button as VButton } from './ui'
import type { QuotedExcerpt } from './state/quotedReply'
import { useWorkspace } from './composables/useWorkspace'

const props = defineProps<{ quotes: QuotedExcerpt[]; disabled: boolean }>()
const emit = defineEmits<{ remove: [id: number]; locate: [quote: QuotedExcerpt] }>()
const { t } = useI18n({ useScope: 'global' })
const { localTime } = useWorkspace()
const list = ref<HTMLElement | null>(null)
const expanded = ref(new Set<number>())
watch(() => props.quotes, async (quotes, previous) => {
  const retained = new Set(quotes.map(quote => quote.id))
  expanded.value = new Set([...expanded.value].filter(id => retained.has(id)))
  if ((quotes.at(-1)?.id || 0) <= (previous?.at(-1)?.id || 0)) return
  await nextTick()
  if (props.quotes === quotes && list.value) list.value.scrollTop = list.value.scrollHeight
}, { immediate: true, flush: 'post' })
// Stable draft IDs preserve identity and expansion; visible numbers follow
// the current order and never become the removal or source-navigation key.

function toggle(id: number) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}
</script>

<template>
  <div v-if="props.quotes.length" ref="list" class="sender-quotes" role="group" :aria-label="t('quotedText')">
    <div v-for="(quote, index) in quotes" :key="quote.id" class="sender-quote" :data-quote-id="quote.id">
      <span class="sender-quote-mark" aria-hidden="true">“</span>
      <div class="sender-quote-body">
        <div class="sender-quote-header">
          <strong>{{ t('quoteNumber', { number: index + 1 }) }}</strong>
          <button v-if="quote.source" type="button" class="sender-quote-source" :title="t('viewQuoteSource')" :disabled="disabled" @click="emit('locate', quote)">
            {{ t(quote.role === 'assistant' ? 'quotedReply' : 'quotedMessage') }} · {{ localTime(quote.source.createdAt) }}
          </button>
          <span v-else class="sender-quote-label">{{ t(quote.role === 'assistant' ? 'quotedReply' : 'quotedMessage') }}</span>
        </div>
        <p :id="`quote-preview-${quote.id}`" :class="{ expanded: expanded.has(quote.id) }">{{ quote.text }}</p>
        <button type="button" class="sender-quote-toggle" :aria-expanded="expanded.has(quote.id)" :aria-controls="`quote-preview-${quote.id}`" @click="toggle(quote.id)">{{ t(expanded.has(quote.id) ? 'collapseQuote' : 'expandQuote') }}</button>
      </div>
      <v-button variant="text" class="icon-button sender-quote-remove icon-close" :aria-label="t('removeQuoteNumber', { number: index + 1 })" :disabled="disabled" @click="emit('remove', quote.id)">×</v-button>
    </div>
  </div>
</template>

<style scoped>
.sender-quotes{display:flex;flex-direction:column;gap:8px;max-height:min(30dvh,240px);margin:12px 12px 0;overflow-y:auto;overscroll-behavior:contain;scrollbar-gutter:stable}
.sender-quote{display:flex;flex:none;align-items:flex-start;gap:10px;padding:10px 10px 10px 12px;min-width:0;border-inline-start:3px solid var(--accent);border-radius:4px 9px 9px 4px;background:color-mix(in srgb,var(--accent) 7%,var(--surface));color:var(--text)}
.sender-quote-mark{flex:none;height:26px;color:var(--accent);font-family:Georgia,serif;font-size:38px;line-height:1}
.sender-quote-body{min-width:0;flex:1}
.sender-quote-header{display:flex;flex-wrap:wrap;align-items:baseline;gap:3px 8px;margin-bottom:3px;font-size:12px;line-height:1.5}
.sender-quote-header strong{font-weight:600}
.sender-quote-label{color:var(--muted)}
.sender-quote-source,.sender-quote-toggle{padding:0;border:0;background:none;color:var(--muted);font:inherit;font-size:12px;text-align:start;overflow-wrap:anywhere;cursor:pointer}
.sender-quote-source:hover,.sender-quote-toggle:hover{color:var(--accent);text-decoration:underline}
.sender-quote-source:disabled{cursor:default;opacity:.5}
.sender-quote-source:focus-visible,.sender-quote-toggle:focus-visible{outline:var(--focus-outline,2px solid var(--accent));outline-offset:2px}
.sender-quote-body p{display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2;margin:0;overflow:hidden;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px;line-height:1.6}
.sender-quote-body p.expanded{display:block;overflow:visible}
.sender-quote-toggle{margin-top:4px;color:var(--accent)}
@media(max-height:520px){
  .sender-quotes{gap:6px;max-height:25dvh;margin:6px 8px 0}
  .sender-quote{gap:7px;padding:6px 8px}
  .sender-quote-body p:not(.expanded){-webkit-line-clamp:1}
}
</style>
