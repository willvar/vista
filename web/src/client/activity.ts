// Coalesce actual interaction minutes. An idle page produces no requests;
// multiple clients are deduplicated by the server's UTC-minute primary key.
export function createActivityCollector(send: (minutes: number[]) => Promise<unknown>, options: { now?: () => number; delayMs?: number } = {}) {
  const now = options.now ?? Date.now
  const seen = new Set<number>()
  const pending = new Set<number>()
  let timer: ReturnType<typeof setTimeout> | undefined
  let sending: Promise<void> | undefined
  let stopped = false

  async function flush() {
    clearTimeout(timer); timer = undefined
    if (sending) { await sending; if (!stopped && pending.size) return flush(); return }
    const minute = Math.floor(now() / 60_000)
    const batch = [...pending].filter(value => value >= minute - 1440 && value <= minute).slice(-120)
    pending.clear()
    if (!batch.length) return
    sending = (async () => {
      try {
        await send(batch)
        if (!stopped && pending.size && !timer) timer = setTimeout(() => { void flush() }, options.delayMs ?? 15_000)
      }
      catch { if (!stopped) batch.forEach(value => pending.add(value)) }
      finally { sending = undefined }
    })()
    await sending
  }
  function record() {
    if (stopped) return
    const minute = Math.floor(now() / 60_000)
    if (!seen.has(minute)) {
      seen.add(minute); pending.add(minute)
      if (seen.size > 120) seen.delete(seen.values().next().value!)
      if (pending.size > 120) pending.delete(pending.values().next().value!)
    }
    if (pending.size && !timer && !sending) timer = setTimeout(() => { void flush() }, options.delayMs ?? 15_000)
  }
  return { record, flush, stop() { stopped = true; clearTimeout(timer); void flush() } }
}

export function trackActivity(send: (minutes: number[]) => Promise<unknown>) {
  const collector = createActivityCollector(send)
  const input = (event: Event) => { if (event.isTrusted && !document.hidden) collector.record() }
  const hidden = () => { if (document.hidden) void collector.flush() }
  const flush = () => { void collector.flush() }
  const events = ['pointerdown', 'keydown', 'input', 'wheel', 'touchstart']
  events.forEach(name => window.addEventListener(name, input, { capture: true, passive: true }))
  document.addEventListener('visibilitychange', hidden)
  window.addEventListener('pagehide', flush)
  return () => {
    events.forEach(name => window.removeEventListener(name, input, true))
    document.removeEventListener('visibilitychange', hidden)
    window.removeEventListener('pagehide', flush)
    collector.stop()
  }
}
