import { ref } from 'vue'
import { VistaAPIError, type VistaClient } from '@vista/client'
import { profileStorageKey } from '../client/profileScope.ts'
import type { QuotedReply } from '../state/quotedReply.ts'

export type DraftImage = { hash: string; name: string; mediaType: string; bytes: number }
type Saved = { revision: number; text: string; quote?: QuotedReply | null; images: DraftImage[] }
export type DraftInput = { text: string; quote: QuotedReply | null; files: File[] }
function sameInput(a: DraftInput | undefined,b: DraftInput) { return !!a && a.text===b.text && a.quote===b.quote && a.files.length===b.files.length && a.files.every((file,i)=>file===b.files[i]) }

type Pending = Omit<Saved, 'images'> & { images?: DraftImage[] }
function readPending(storageKey: string): Pending | undefined {
  try {
    const pending = JSON.parse(sessionStorage.getItem(storageKey) || 'null')
    if (pending && typeof pending.text === 'string' && Number.isSafeInteger(pending.revision) && pending.revision >= 0) return pending
  } catch { /* The tab can still keep unacknowledged edits in memory. */ }
}

type Phase = 'idle' | 'loadFailed' | 'saveFailed' | 'conflict'
type Entry = { saved?: Saved; current?: DraftInput; dirty: boolean; phase: Phase; work: Promise<unknown>; timer?: ReturnType<typeof setTimeout>; legacy?: () => void; conflictBase?: number; sentThrough?: number }

export function serviceDrafts(client: VistaClient, apply: (key: string, input: DraftInput) => void, profileId = 'default') {
  const pendingDraftKey = (key: string) => profileStorageKey('vista-pending-draft:', profileId) + key
  const currentKey = ref('home')
  const phase = ref<Phase>('idle')
  const entries = new Map<string, Entry>()
  const refs = new WeakMap<File, { key: string; ref: DraftImage }>()
  const path = (key: string) => `/drafts/${encodeURIComponent(key)}`
  let disposed = false
  // A read belongs to one visit, even when shared state triggered it.
  // Writes can outlive a conversation view, but not its Profile workspace.
  const writeLifetime = new AbortController()
  let readView = new AbortController()
  const readSignal = (caller?: AbortSignal) => caller ? AbortSignal.any([readView.signal, caller]) : readView.signal

  function entry(key: string) {
    if (!entries.has(key)) entries.set(key, { dirty: false, phase: 'idle', work: Promise.resolve() })
    return entries.get(key)!
  }
  function status(key: string, value: Phase) {
    entry(key).phase = value
    if (currentKey.value === key) phase.value = value
  }
  // Only unacknowledged text, quote and already-uploaded references go into
  // tab storage. It survives reload but isn't another shared draft database.
  function remember(key: string, e: Entry) {
    if (!e.current) return false
    const { text, quote, files } = e.current
    const images = files.map(file => refs.get(file)).map(image => image?.key === key ? image.ref : undefined)
    const pending = { revision: e.conflictBase ?? e.saved?.revision ?? 0, text, quote, images: images.every(Boolean) ? images : undefined }
    try {
      sessionStorage.setItem(pendingDraftKey(key), JSON.stringify(pending))
      return true
    } catch { return false }
  }
  function forget(key: string) {
    try { sessionStorage.removeItem(pendingDraftKey(key)) } catch { /* Storage can be disabled. */ }
  }
  function conflict() { return new VistaAPIError('Another tab saved this draft. Your changes have not been uploaded.', 409) }
  function serialize<T>(e: Entry, next: () => Promise<T>) {
    const result = e.work.then(next, next)
    e.work = result.catch(() => {})
    return result
  }
  async function get(key: string, signal?: AbortSignal) {
    const saved = await client.request<Saved>(path(key), { signal })
    if (!saved || !Number.isSafeInteger(saved.revision) || !Array.isArray(saved.images)) throw Error('Invalid draft response')
    return saved
  }
  async function refresh(key: string, caller?: AbortSignal) {
    if (disposed || currentKey.value !== key) return
    const signal = readSignal(caller)
    const e = entry(key)
    await serialize(e, async () => {
      if (signal.aborted || e.dirty) return
      try {
        const pending = !e.current ? readPending(pendingDraftKey(key)) : undefined
        const candidate = await get(key, signal)
        if (signal.aborted || e.dirty) return
        if ((e.sentThrough !== undefined && candidate.revision <= e.sentThrough) || (e.saved?.revision === candidate.revision && e.current)) {
          status(key, 'idle')
          return
        }
        const chosen = pending?.revision === candidate.revision && Array.isArray(pending.images)
          ? pending.images.flatMap(ref => candidate.images.find(image => image.hash === ref?.hash && image.name === ref?.name) || [])
          : candidate.images
        const files = await Promise.all(chosen.map(async image => {
          const existing = e.current?.files.find(file => refs.get(file)?.ref.hash === image.hash && file.name === image.name)
          if (existing) return existing
          const blob = await client.image(`${path(key)}/images/${image.hash}`, signal)
          const file = new File([blob], image.name, { type: image.mediaType })
          refs.set(file, { key, ref: image })
          return file
        }))
        if (signal.aborted || e.dirty) return
        e.saved = candidate
        const matches = pending?.text === candidate.text && JSON.stringify(pending?.quote || null) === JSON.stringify(candidate.quote || null)
          && (pending?.images === undefined || JSON.stringify(pending.images) === JSON.stringify(candidate.images))
        if (pending && (!matches || pending.revision === candidate.revision)) {
          e.current = { text: pending.text, quote: pending.quote || null, files }
          e.dirty = true
          if (pending.revision !== candidate.revision) {
            e.conflictBase = pending.revision
            status(key, 'conflict')
          } else {
            status(key, 'idle')
            schedule(key)
          }
        } else {
          e.current = { text: candidate.text, quote: candidate.quote || null, files }
          if (pending) forget(key)
          status(key, 'idle')
        }
        apply(key, e.current)
      } catch {
        if (!signal.aborted && !e.dirty) status(key, 'loadFailed')
      }
    })
  }
  function schedule(key: string) {
    const e = entry(key)
    clearTimeout(e.timer)
    if (e.phase === 'conflict') return
    e.timer = setTimeout(() => { if (e.current && e.dirty) void save(key, e.current).catch(() => {}) }, 350)
  }
  function change(key: string, input: DraftInput) {
    if (disposed) return
    const e = entry(key)
    e.current = input
    e.dirty = true
    if (e.phase === 'loadFailed') status(key, 'idle')
    remember(key, e)
    schedule(key)
  }
  function select(key: string, legacy?: DraftInput, done?: () => void, signal?: AbortSignal) {
    if (disposed) return
    readView.abort()
    readView = new AbortController()
    currentKey.value = key
    const e = entry(key)
    phase.value = e.phase
    if (e.current) apply(key, e.current)
    const pending = !e.current ? readPending(pendingDraftKey(key)) : undefined
    if (pending) {
      apply(key, { text: pending.text, quote: pending.quote || null, files: [] })
      void refresh(key, signal)
      return
    }
    if (legacy && (legacy.text || legacy.files.length || legacy.quote?.items.length) && !e.saved && !e.dirty) {
      const loadSignal = readSignal(signal)
      void serialize(e, async () => {
        if (loadSignal.aborted || e.dirty) return
        try {
          const value = await get(key, loadSignal)
          if (loadSignal.aborted || e.dirty) return
          if (value.revision === 0) {
            e.saved = value; e.legacy = done
            if (currentKey.value === key) apply(key, legacy)
            change(key, legacy)
          }
        } catch {
          if (!loadSignal.aborted && !e.dirty) status(key, 'loadFailed')
        }
      }).then(() => { if (!loadSignal.aborted) return refresh(key, signal) })
    } else void refresh(key, signal)
  }
  async function save(key: string, input: DraftInput, caller?: AbortSignal): Promise<{ key: string; revision: number; images: DraftImage[] }> {
    const e = entry(key)
    const signal = caller ? AbortSignal.any([writeLifetime.signal, caller]) : writeLifetime.signal
    clearTimeout(e.timer)
    return serialize(e, async () => {
      if (disposed) throw Error('Draft view closed')
      signal.throwIfAborted()
      if (e.conflictBase !== undefined) throw conflict()
      try {
        if (!e.saved) {
          const baseline = await get(key, signal)
          if (baseline.revision !== 0) throw conflict()
          e.saved = baseline
        }
        const images: DraftImage[] = []
        for (const file of input.files) {
          const known = refs.get(file)
          if (known?.key === key && e.saved.images.some(image => image.hash === known.ref.hash)) { images.push(known.ref); continue }
          e.saved = await client.request<Saved>(`${path(key)}/images?revision=${e.saved.revision}&name=${encodeURIComponent(file.name)}`, { method: 'POST', headers: { 'Content-Type': file.type }, body: file, signal })
          const ref = e.saved.images.at(-1)
          if (!ref) throw Error('Attachment upload did not return a reference')
          refs.set(file, { key, ref }); images.push(ref)
          if (e.dirty) remember(key, e)
        }
        if (input.text !== e.saved.text || JSON.stringify(input.quote || null) !== JSON.stringify(e.saved.quote || null) || JSON.stringify(images) !== JSON.stringify(e.saved.images) || e.saved.revision === 0) {
          e.saved = await client.request<Saved>(path(key), { method: 'PUT', body: JSON.stringify({ revision: e.saved.revision, text: input.text, quote: input.quote, images }), signal })
        }
        signal.throwIfAborted()
        if (sameInput(e.current,input) || !e.current) { e.current = input; e.dirty = false }
        if (e.dirty) remember(key, e)
        else { forget(key); e.legacy?.(); e.legacy = undefined }
        status(key, 'idle')
        return { key, revision: e.saved.revision, images }
      } catch (cause) {
        if (!disposed) {
          if (cause instanceof VistaAPIError && cause.status === 409) e.conflictBase = e.saved?.revision ?? 0
          if (e.dirty) remember(key, e)
          status(key, e.conflictBase === undefined ? 'saveFailed' : 'conflict')
        }
        throw cause
      }
    })
  }
  function sent(key: string, sentRevision: number, input: DraftInput) {
    const e = entry(key)
    // An accepted send either clears this revision or leaves a newer one.
    // A response with the old revision can never refill the composer.
    e.sentThrough = Math.max(e.sentThrough ?? 0, sentRevision)
    if (e.current && !sameInput(e.current,input)) return // A newer local edit is still pending.
    if (e.saved?.revision === sentRevision) e.saved = undefined
    e.current = { text: '', quote: null, files: [] }
    e.dirty = false
    forget(key)
    void refresh(key)
  }
  // A Profile switch unmounts this editor. Commit only dirty drafts, including
  // their in-memory attachments, without waiting for an obsolete view read.
  async function flush() {
    const pending = [...entries].flatMap(([key, e]) => e.dirty && e.current ? [{ key, input: e.current }] : [])
    if (!pending.length) return
    readView.abort()
    readView = new AbortController()
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(Error('Draft save timed out')), 5000)
    const deadline = new Promise<void>(resolve => {
      controller.signal.addEventListener('abort', () => resolve(), { once: true })
    })
    try { await Promise.race([Promise.allSettled(pending.map(({ key, input }) => save(key, input, controller.signal))), deadline]) }
    finally { clearTimeout(timer); controller.abort() }
    // Failed or conflicting text can be recovered in this tab. Unsaved files
    // cannot be recovered after unmount, so keep that editor open instead.
    if ([...entries].some(([key, e]) => e.dirty && (e.current?.files.length || !remember(key, e)))) {
      throw Error('Draft could not be retained when switching Profile')
    }
  }
  return { phase, change, select, refresh, save, sent, flush, dispose: () => {
    disposed = true
    writeLifetime.abort()
    readView.abort()
    for (const e of entries.values()) clearTimeout(e.timer)
    entries.clear()
  } }
}
