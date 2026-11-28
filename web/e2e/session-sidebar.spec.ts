import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test, expect, type VistaFixture } from './fixtures'

// Inert rows exercise real SQLite paging without starting dozens of Workers.
// This database belongs exclusively to the native fixture, never a running user instance.
function seed(vista: VistaFixture, titles: string[]) {
  const db = new DatabaseSync(join(vista.stateDirectory, 'vista.db'))
  const rows = titles.map((title, index) => ({ id: randomUUID(), title, created: new Date(Date.UTC(2025, 0, 1) - index * 1_000).toISOString() }))
  try {
    db.exec('PRAGMA busy_timeout=5000; BEGIN IMMEDIATE')
    const insert = db.prepare('INSERT INTO sessions(id,title,is_open,created_at) VALUES (?,?,1,?)')
    for (const row of rows) insert.run(row.id, row.title, row.created)
    db.exec('COMMIT')
  } finally { db.close() }
  return rows
}

test('infinite sidebar retains a second-page anchor after remote close and reconnect deletion', { tag: '@responsive' }, async ({ page, vista, isMobile }) => {
  const items = seed(vista, Array.from({ length: 46 }, (_, i) => `Session ${String(i + 1).padStart(2, '0')}`))
  const reads: URLSearchParams[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/sessions') reads.push(url.searchParams)
  })
  await page.goto(vista.origin + '/')
  if (isMobile) await page.locator('.mobile-sessions-trigger:visible').tap()
  const sidebar = page.locator('.sessions-panel')
  const list = sidebar.locator('ul').first()
  const rows = sidebar.locator('.session-card')
  await expect(rows).toHaveCount(20)
  expect(reads.map(q => [q.get('page'), q.get('pageSize')])).toEqual([['1', '20']])
  await list.evaluate(element => { element.scrollTop = element.scrollHeight })
  await expect(rows).toHaveCount(40)
  const anchor = rows.filter({ hasText: 'Session 21' })
  await anchor.evaluate(element => {
    const list = element.parentElement!
    list.scrollTop += element.getBoundingClientRect().top - list.getBoundingClientRect().top
  })
  const offset = () => anchor.evaluate(row => row.getBoundingClientRect().top - row.parentElement!.getBoundingClientRect().top)
  const before = await offset()
  await vista.api(`/sessions/${items[3]!.id}/group/close`, { method: 'POST' })
  await expect(sidebar.locator('.sidebar-section-label small')).toHaveText('45')
  await expect(rows.nth(19)).toHaveAttribute('data-session-id', items[20]!.id)
  expect(Math.abs(await offset() - before)).toBeLessThan(2)

  await page.context().setOffline(true)
  await vista.api(`/sessions/${items[4]!.id}/group`, { method: 'DELETE' })
  await page.context().setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(rows.nth(18)).toHaveAttribute('data-session-id', items[20]!.id)
  expect(Math.abs(await offset() - before)).toBeLessThan(2)
  await list.evaluate(element => { element.scrollTop = element.scrollHeight })
  await expect(rows).toHaveCount(44)
  expect(await rows.evaluateAll(elements => elements.map(row => (row as HTMLElement).dataset.sessionId)))
    .toEqual(items.filter((_, i) => i !== 3 && i !== 4).map(item => item.id))
  expect(reads.every(q => q.has('page') && q.get('open') === 'true' && Number(q.get('pageSize')) <= 40)).toBe(true)
  expect(reads.some(q => q.get('page') === '1' && q.get('pageSize') === '40')).toBe(true)
})

test('title order matches the manager and an off-page active conversation stays open', async ({ page, vista }) => {
  const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Zulu active' }) })
  const seeded = seed(vista, ['Chrome', '北京', 'Apple', ...Array.from({ length: 20 }, (_, i) => `Middle ${i}`)])
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  const sidebar = page.locator('.sessions-panel')
  const rows = sidebar.locator('.session-card')
  await expect(rows).toHaveCount(20)
  const sort = async (scope: typeof sidebar) => {
    await scope.getByRole('button', { name: 'Sort sessions', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Title: A–Z', exact: true }).click()
  }
  await sort(sidebar)
  await expect(rows.first()).toHaveAttribute('data-session-id', seeded[2]!.id)
  await expect(rows.nth(1)).toHaveAttribute('data-session-id', seeded[1]!.id)
  await expect(rows.nth(2)).toHaveAttribute('data-session-id', seeded[0]!.id)
  await expect(rows.filter({ hasText: 'Zulu active' })).toHaveCount(0)
  const draft = page.locator('.sender-card textarea')
  await draft.fill('Keep this off-page draft')
  await vista.api(`/sessions/${seeded[1]!.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Bamboo' }) })
  await expect(rows.nth(1)).toContainText('Bamboo')
  await expect(draft).toHaveValue('Keep this off-page draft')
  await expect(page).toHaveURL(`${vista.origin}/sessions/${active.id}`)
  await sidebar.getByRole('button', { name: 'Manage sessions' }).click()
  const manager = page.locator('.session-manager-page')
  await sort(manager)
  const managed = manager.locator('.managed-session')
  await expect(managed.first()).toHaveAttribute('data-session-id', seeded[2]!.id)
  expect(await managed.evaluateAll(elements => elements.map(row => (row as HTMLElement).dataset.sessionId)))
    .toEqual((await rows.evaluateAll(elements => elements.map(row => (row as HTMLElement).dataset.sessionId))).slice(0, 10))
})

test('a failed next page keeps existing rows and a retried old order cannot leak across a sort change', async ({ page, vista }) => {
  const seeded = seed(vista, ['Z initial', ...Array.from({ length: 24 }, (_, i) => `Mid ${String(i).padStart(2, '0')}`), 'Apple'])
  let fail = true
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  let requested = false
  await page.route('**/api/sessions?*', async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.get('open') !== 'true' || params.get('page') !== '2' || params.has('sort')) return route.continue()
    if (fail) { fail = false; return route.fulfill({ status: 503, body: 'Controlled page failure' }) }
    const response = await route.fetch()
    requested = true
    await held
    await route.fulfill({ response }).catch(() => {}) // A sort switch aborts the old request.
  })
  try {
    await page.goto(vista.origin + '/')
    const sidebar = page.locator('.sessions-panel')
    const list = sidebar.locator('ul').first()
    const rows = sidebar.locator('.session-card')
    await expect(rows).toHaveCount(20)
    await list.evaluate(element => { element.scrollTop = element.scrollHeight })
    const retry = sidebar.getByRole('button', { name: 'Retry', exact: true })
    await expect(retry).toBeVisible()
    await expect(rows).toHaveCount(20)
    await retry.click()
    await expect.poll(() => requested).toBe(true)
    await sidebar.getByRole('button', { name: 'Sort sessions', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Title: A–Z', exact: true }).click()
    await expect(rows.first()).toHaveAttribute('data-session-id', seeded.at(-1)!.id)
    await expect(rows).toHaveCount(20)
    release()
    await list.evaluate(element => { element.scrollTop = element.scrollHeight })
    await expect(rows).toHaveCount(26)
    expect(await rows.evaluateAll(elements => elements.map(row => (row as HTMLElement).dataset.sessionId)))
      .toEqual([seeded.at(-1)!, ...seeded.slice(1, -1), seeded[0]!].map(row => row.id))
  } finally { release() }
})

test('sidebar restores the saved order on the first page request after reload and in a new tab', async ({ page, vista }) => {
  const [newest, oldest] = seed(vista, ['Z newest', 'Apple oldest'])
  await page.reload()
  const rows = page.locator('.sessions-panel .session-card')
  const sort = page.locator('.sessions-panel').getByRole('button', { name: 'Sort sessions', exact: true })
  await expect(rows.first()).toHaveAttribute('data-session-id', newest!.id)
  await sort.click()
  await page.getByRole('menuitemradio', { name: 'Title: A–Z', exact: true }).click()
  await expect(rows.first()).toHaveAttribute('data-session-id', oldest!.id)
  const firstRead = page.waitForRequest(request => {
    const url = new URL(request.url())
    return request.method() === 'GET' && url.pathname === '/api/sessions' && url.searchParams.get('page') === '1'
  })
  await page.reload()
  expect(new URL((await firstRead).url()).searchParams.get('sort')).toBe('title-asc')
  await expect(rows.first()).toHaveAttribute('data-session-id', oldest!.id)
  await sort.click()
  await expect(page.getByRole('menuitemradio', { name: 'Title: A–Z', exact: true })).toBeChecked()
  const another = await page.context().newPage()
  try {
    await another.goto(vista.origin + '/')
    await expect(another.locator('.sessions-panel .session-card').first()).toHaveAttribute('data-session-id', oldest!.id)
  } finally { await another.close() }
  await page.evaluate(() => localStorage.setItem('vista-sidebar-sort', 'unsupported-order'))
  await page.reload()
  await expect(rows.first()).toHaveAttribute('data-session-id', newest!.id)
})
