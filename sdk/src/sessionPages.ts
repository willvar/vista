import type { SessionPage, SessionSort } from './types.js'
import type { VistaClient } from './vista.js'

export type SessionPageQuery = { open?: boolean; sort?: SessionSort; search?: string; untitledLabel?: string }
export type SessionWindow = Pick<SessionPage, 'items' | 'total' | 'version' | 'attention' | 'throughSequences'>

export function sessionPagePath(page: number, pageSize: number, query: SessionPageQuery = {}) {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) })
  if (query.open !== undefined) params.set('open', String(query.open))
  if (query.sort && query.sort !== 'created-desc') params.set('sort', query.sort)
  if (query.search) params.set('search', query.search)
  if (query.untitledLabel && (query.search || query.sort === 'title-asc')) params.set('untitledLabel', query.untitledLabel)
  return `/sessions?${params}`
}

export function pageVersionAtLeast(value: string | undefined, expected: string) {
  const [epoch, revision] = value?.split(':') ?? []
  const [expectedEpoch, expectedRevision] = expected.split(':')
  return epoch === expectedEpoch && Number.isSafeInteger(Number(revision)) && Number(revision) >= Number(expectedRevision)
}

export function appendSessionPage(prefix: SessionWindow, next: SessionPage): SessionWindow | undefined {
  if (prefix.version !== next.version || prefix.total !== next.total || prefix.items.length !== (next.page - 1) * next.pageSize
    || next.items.length !== Math.min(next.pageSize, next.total - prefix.items.length)) return
  const items = [...prefix.items, ...next.items]
  if (new Set(items.map(item => item.id)).size !== items.length) return
  return { ...prefix, items, throughSequences: { ...prefix.throughSequences, ...next.throughSequences } }
}

export function sessionWindowFromPage(page: SessionPage) {
  return appendSessionPage({ items: [], total: page.total, version: page.version, attention: page.attention }, page)
}

// Revalidate a loaded prefix without broadcasting or reading the full list.
// Use a single SQLite snapshot for up to 100 rows, and version-checked pages
// for deeper scroll positions. An inconsistent prefix is never rendered.
export async function readSessionWindow(client: Pick<VistaClient, 'request'>, query: SessionPageQuery,
  batches: number, batchSize: number, signal: AbortSignal): Promise<SessionWindow | undefined> {
  if (!Number.isSafeInteger(batches) || batches < 1 || !Number.isSafeInteger(batchSize) || batchSize < 1 || batchSize > 100) {
    throw new RangeError('Invalid session window size')
  }
  const chunkSize = Math.floor(100 / batchSize) * batchSize
  const wanted = batches * batchSize
  const first = await client.request<SessionPage>(sessionPagePath(1, Math.min(wanted, chunkSize), query), { signal })
  let prefix = sessionWindowFromPage(first)
  while (prefix && prefix.items.length < Math.min(prefix.total, wanted)) {
    const offset = prefix.items.length
    const size = wanted - offset >= chunkSize ? chunkSize : batchSize
    const next = await client.request<SessionPage>(sessionPagePath(offset / size + 1, size, query), { signal })
    prefix = appendSessionPage(prefix, next)
  }
  return prefix
}
