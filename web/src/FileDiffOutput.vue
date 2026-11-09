<script setup lang="ts">
import { computed, ref } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useI18n } from 'vue-i18n'
import type { TraceFileDiff } from './state/runTrace'
import { diffLines } from './state/fileDiff'

const props = defineProps<{ diff: TraceFileDiff }>()
const { t } = useI18n()
const lines = computed(() => diffLines(props.diff.unified))
const viewport = ref<HTMLElement | null>(null)
const virtualizer = useVirtualizer(computed(() => ({
  count: lines.value.length, getScrollElement: () => viewport.value,
  estimateSize: () => 24, overscan: 8,
})))
const rows = computed(() => virtualizer.value.getVirtualItems())
const counts = computed(() => ({ added: lines.value.filter(line => line.kind === 'add').length, removed: lines.value.filter(line => line.kind === 'remove').length }))
// Separate large text nodes just as PlainTextOutput does. No wrapping keeps
// line heights fixed and large diffs bounded to the visible rows.
function fragments(text: string) { return text.match(/[\s\S]{1,4096}/gu) || [''] }
</script>

<template>
  <div class="file-diff">
    <div class="file-diff-heading"><code>{{ diff.path }}</code><span>{{ t(diff.deleted ? 'traceFileDeleted' : diff.created ? 'traceFileCreated' : 'traceFileUpdated') }}</span><span v-if="lines.length" class="file-diff-counts"><span class="file-diff-added">+{{ counts.added }}</span> <span class="file-diff-removed">−{{ counts.removed }}</span></span></div>
    <p v-if="diff.omitted" class="file-diff-message">{{ t(diff.omitted === 'too_large' ? 'traceDiffTooLarge' : 'traceDiffUnavailable') }}</p>
    <p v-else-if="!lines.length && !diff.created" class="file-diff-message">{{ t('traceFileUnchanged') }}</p>
    <div v-if="lines.length" ref="viewport" class="file-diff-viewport" :style="{ height: `${Math.min(lines.length * 24, 384)}px` }" tabindex="0">
      <div class="file-diff-lines" :style="{ height: `${virtualizer.getTotalSize()}px` }">
        <div v-for="row in rows" :key="row.key.toString()" class="file-diff-line" :class="`file-diff-${lines[row.index]!.kind}`" :style="{ transform: `translateY(${row.start}px)` }">
          <span class="file-diff-number">{{ lines[row.index]!.oldLine }}</span><span class="file-diff-number">{{ lines[row.index]!.newLine }}</span><span class="file-diff-marker">{{ lines[row.index]!.kind === 'add' ? '+' : lines[row.index]!.kind === 'remove' ? '−' : '' }}</span><code><span v-for="(text, index) in fragments(lines[row.index]!.text)" :key="index">{{ lines[row.index]!.kind === 'notice' ? t('traceDiffNoNewline') : text }}</span></code>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.file-diff{min-width:0;border:1px solid var(--border);border-radius:8px;overflow:hidden}
.file-diff-heading{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem .75rem;padding:.6rem .75rem;font-size:14px;color:var(--muted);background:var(--code-bg)}
.file-diff-heading>code{min-width:0;overflow-wrap:anywhere;color:var(--text);flex:1 1 160px}
.file-diff-counts{white-space:nowrap}.file-diff-added{color:var(--success)}.file-diff-removed{color:var(--danger)}
.file-diff-message{padding:.6rem .75rem;margin:0;font-size:14px;color:var(--muted)}
.file-diff-viewport{overflow:auto;background:var(--code-bg)}
.file-diff-lines{position:relative;min-width:100%}
.file-diff-line{position:absolute;top:0;left:0;display:flex;min-width:100%;width:max-content;height:24px;line-height:24px;font-size:13px;font-family:var(--font-mono,monospace);white-space:pre;tab-size:4}
.file-diff-line code{font:inherit;padding-right:1rem}
.file-diff-number{flex:0 0 4.5em;width:4.5em;text-align:right;color:var(--muted);padding-right:.75em;user-select:none}
.file-diff-marker{flex:0 0 1.5em;width:1.5em;user-select:none}
.file-diff-add{background:color-mix(in srgb,var(--success) 12%,transparent)}.file-diff-add .file-diff-marker{color:var(--success)}
.file-diff-remove{background:var(--danger-soft);background:color-mix(in srgb,var(--danger) 10%,transparent)}.file-diff-remove .file-diff-marker{color:var(--danger)}
.file-diff-hunk,.file-diff-notice{color:var(--muted)}
</style>
