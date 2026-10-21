<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch, type ComponentPublicInstance } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { Button } from 'antdv-next'
import { VerticalAlignBottomOutlined } from '@antdv-next/icons'
import { useI18n } from 'vue-i18n'
import { useWorkspace } from './composables/useWorkspace'
import { jobActive } from './state/jobs'
import type { Job, OutputView } from './client/types'
import PlainTextOutput from './PlainTextOutput.vue'

const props = defineProps<{ sessionId: string; job: Job }>()
const { client } = useWorkspace()
const { t } = useI18n()
// Byte offsets let us jump to any part of a large file without downloading
// earlier output. Only visible blocks and a bounded cache stay in memory.
const blockSize = 16384
const cacheLimit = 32
const viewport = ref<HTMLElement | null>(null)
const pages = shallowRef(new Map<number, OutputView>())
const requests = new Map<number, AbortController>()
const failures = new Set<number>()
const size = ref(props.job.bytes)
const error = ref('')
const following = ref(true)
let closed = false
let timer: ReturnType<typeof setTimeout> | undefined
let frame = 0
let programmatic = false
let observer: ResizeObserver | undefined
const count = computed(() => Math.max(1, Math.ceil(size.value / blockSize)))
const virtualizer = useVirtualizer(computed(() => ({
  count: count.value,
  getScrollElement: () => viewport.value,
  estimateSize: () => 800,
  overscan: 1,
  useAnimationFrameWithResizeObserver: true,
})))
const rows = computed(() => virtualizer.value.getVirtualItems())
function follow() {
  if (closed || !following.value) return
  cancelAnimationFrame(frame)
  frame = requestAnimationFrame(() => {
    const element = viewport.value
    if (!element || !following.value || closed) return
    programmatic = true
    element.scrollTop = element.scrollHeight
    frame = requestAnimationFrame(() => { programmatic = false })
  })
}
function manualScroll(event: Event) {
  // Pointer input includes dragging the scrollbar and touch scrolling. A
  // subsequent bottom scroll restores follow; rendering cannot pause it.
  if (event instanceof WheelEvent && event.deltaY >= 0) return
  if (event instanceof KeyboardEvent && !['ArrowUp', 'PageUp', 'Home', 'ArrowDown', 'PageDown', 'End', ' '].includes(event.key)) return
  following.value = false
  programmatic = false
  cancelAnimationFrame(frame)
}
function scroll() {
  const element = viewport.value
  if (!element || programmatic) return
  following.value = element.scrollHeight - element.clientHeight - element.scrollTop <= 4
}
function resume() { following.value = true; follow() }
function remember(index: number, page: OutputView) {
  const updated = new Map(pages.value)
  updated.delete(index)
  updated.set(index, page)
  const visible = new Set(rows.value.map(row => row.index))
  for (const key of updated.keys()) {
    if (updated.size <= cacheLimit) break
    if (!visible.has(key)) updated.delete(key)
  }
  pages.value = updated
}
async function read(index: number, refresh = false) {
  const cached = pages.value.get(index)
  const current = cached && (cached.nextOffset >= (index + 1) * blockSize || cached.availableBytes >= size.value)
  if (closed || !refresh && (requests.has(index) || current || failures.has(index))) return
  if (refresh && requests.has(index)) {
    requests.get(index)!.abort()
    requests.delete(index)
  }
  if (requests.size >= 4) return
  const controller = new AbortController()
  requests.set(index, controller)
  try {
    const page = await client.request<OutputView>(`/sessions/${encodeURIComponent(props.sessionId)}/jobs/${encodeURIComponent(props.job.id)}/output?offset=${index * blockSize}&limit=${blockSize}`, { signal: controller.signal })
    if (closed || controller.signal.aborted) return
    size.value = Math.max(size.value, page.availableBytes)
    remember(index, page)
    failures.delete(index)
    if (!failures.size) error.value = ''
    await nextTick()
    follow()
  } catch (cause) {
    if (!closed && !controller.signal.aborted) {
      failures.add(index)
      error.value = cause instanceof Error ? cause.message : String(cause)
    }
  } finally {
    if (requests.get(index) === controller) requests.delete(index)
    if (!closed) loadVisible()
  }
}
function loadVisible() {
  for (const row of rows.value) void read(row.index)
}
function measure(element: Element | ComponentPublicInstance | null) {
  if (element instanceof Element) virtualizer.value.measureElement(element)
  else if (element === null) void nextTick(() => { if (!closed) virtualizer.value.measureElement(null) })
}
async function poll() {
  if (closed) return
  // Re-read the boundary block too when a UTF-8 rune straddles the tail.
  const index = Math.floor(Math.max(0, size.value - 4) / blockSize)
  await read(index, true)
  if (!closed && jobActive(props.job)) { clearTimeout(timer); timer = setTimeout(poll, 1000) }
}
function retry() { failures.clear(); error.value = ''; loadVisible(); void poll() }
watch(() => rows.value.map(row => row.index).join(','), loadVisible)
watch(() => props.job.bytes, bytes => { size.value = Math.max(size.value, bytes) })
watch(() => virtualizer.value.getTotalSize(), follow, { flush: 'post' })
watch(() => jobActive(props.job), active => { if (!active) { clearTimeout(timer); void poll() } })
onMounted(async () => {
  await nextTick()
  observer = new ResizeObserver(follow)
  if (viewport.value) observer.observe(viewport.value)
  follow()
  void poll()
})
onUnmounted(() => {
  closed = true
  clearTimeout(timer)
  cancelAnimationFrame(frame)
  observer?.disconnect()
  for (const controller of requests.values()) controller.abort()
  requests.clear()
})
</script>

<template>
  <section class="process-output" :aria-label="t('processOutput')">
    <div class="process-output-toolbar">
      <strong>{{ t('processOutput') }}</strong><span>{{ t('bytesCount', { count: size.toLocaleString() }) }}</span>
      <span v-if="following" class="process-following">{{ t('processFollowing') }}</span>
      <Button v-else type="text" size="small" :aria-label="t('processFollow')" @click="resume"><VerticalAlignBottomOutlined aria-hidden="true" />{{ t('processFollow') }}</Button>
    </div>
    <div v-if="error" class="process-output-error" role="alert"><span>{{ t('outputLoadFailed') }}: {{ error }}</span><Button size="small" @click="retry">{{ t('retry') }}</Button></div>
    <div ref="viewport" class="process-output-viewport" tabindex="0" :aria-label="t('processOutput')" @scroll="scroll" @wheel.passive="manualScroll" @pointerdown="manualScroll" @keydown="manualScroll">
      <div class="process-output-space" :style="{ height: `${virtualizer.getTotalSize()}px` }">
        <div v-for="row in rows" :key="row.key.toString()" :ref="measure" :data-index="row.index" class="process-output-block" :style="{ transform: `translateY(${row.start}px)`, minHeight: pages.has(row.index) ? undefined : '800px' }">
          <PlainTextOutput v-if="pages.get(row.index)?.content" :content="pages.get(row.index)!.content" />
          <p v-else-if="pages.has(row.index)" class="process-output-placeholder">{{ size ? '' : t('processNoOutput') }}</p>
          <p v-else class="process-output-placeholder">{{ failures.has(row.index) ? t('outputLoadFailed') : t('outputLoading') }}</p>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.process-output{display:flex;flex-direction:column;flex:1;min-height:124px;border:1px solid var(--border);border-radius:10px;overflow:hidden;background:var(--surface-subtle)}
.process-output-toolbar{display:flex;align-items:center;gap:.75rem;flex:none;padding:.4rem .75rem;min-height:38px;border-bottom:1px solid var(--border);color:var(--muted);font-size:12px}
.process-output-toolbar strong{color:var(--text-secondary);font-weight:500}
.process-following,.process-output-toolbar .ant-btn{margin-left:auto;font-size:12px}
.process-output-viewport{position:relative;flex:1;min-height:0;overflow:auto;overscroll-behavior:contain;overflow-anchor:none;scrollbar-gutter:stable;padding:.75rem}
.process-output-viewport:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
.process-output-space{position:relative;width:100%}
.process-output-block{position:absolute;top:0;left:0;width:100%;display:flow-root}
.process-output-block :deep(pre){margin:0;white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;overflow:visible;max-height:none;font:12px/1.65 var(--font-mono,monospace);color:var(--text-secondary)}
.process-output-placeholder{margin:0;padding:.2rem 0;color:var(--muted);font-size:13px}
.process-output-error{display:flex;align-items:center;gap:.5rem;padding:.4rem .75rem;color:var(--danger);font-size:12px;max-height:5rem;overflow:auto;overflow-wrap:anywhere}
.process-output-error span{min-width:0;flex:1}
@media(pointer:coarse){.process-output-toolbar .ant-btn{min-height:40px}.process-output-toolbar{min-height:44px}}
</style>
