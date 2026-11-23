import assert from 'node:assert/strict'
import test from 'node:test'
import { appendSessionPage, readSessionWindow, sessionWindowFromPage } from '../src/sessionPages.ts'
import type { Session, SessionPage } from '../src/types.ts'

const page = (page: number, pageSize: number, total = 143, version = 'server:1'): SessionPage => ({
  page, pageSize, total, version,
  items: Array.from({ length: Math.min(pageSize, total - (page - 1) * pageSize) }, (_, i) => ({ id: String((page - 1) * pageSize + i) } as Session)),
})

test('the loaded window replaces its prefix in bounded pages and refuses to mix revisions', async () => {
  const requests: string[] = []
  let changed = false
  const client = { async request<T>(path: string) {
    requests.push(path)
    const q = new URL(path, 'https://example.com').searchParams
    return page(Number(q.get('page')), Number(q.get('pageSize')), 143, changed && q.get('page') !== '1' ? 'server:2' : 'server:1') as T
  } }
  const signal = new AbortController().signal
  const query = { open: true, sort: 'title-asc', untitledLabel: '新会话' } as const
  const prefix = await readSessionWindow(client, query, 6, 20, signal)
  assert.deepEqual(requests.map(request => [...new URL(request, 'https://example.com').searchParams.values()]), [
    ['1', '100', 'true', 'title-asc', '新会话'], ['6', '20', 'true', 'title-asc', '新会话'],
  ])
  assert.deepEqual(prefix?.items.map(row => row.id), Array.from({ length: 120 }, (_, i) => String(i)))
  changed = true
  assert.equal(await readSessionWindow(client, query, 6, 20, signal), undefined)
})

test('append rejects clamped pages, changed counts, or duplicate rows at the old boundary', () => {
  const first = sessionWindowFromPage(page(1, 20))!
  assert.equal(appendSessionPage(first, page(2, 20))?.items.length, 40)
  assert.equal(appendSessionPage(first, page(1, 20)), undefined)
  assert.equal(appendSessionPage(first, page(2, 20, 142)), undefined)
  const shifted = page(2, 20)
  shifted.items[0] = first.items[19]!
  assert.equal(appendSessionPage(first, shifted), undefined)
})
