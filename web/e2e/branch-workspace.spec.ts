import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'
import { selectSessions } from './session-manager-actions'

test.use({ questionTool: true, replyWithPrompt: true })

// These cases specifically exercise the independent branch path.
test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ branching: true }) })
})

test('a saved branch preference follows the Profile while group activity opens its own branch', { tag: '@responsive' }, async ({ page, browser, vista }, info) => {
  test.setTimeout(55_000)
  const post = (path: string, body: object = {}) => vista.api(path, { method: 'POST', body: JSON.stringify(body) })
  const root = await post('/sessions', { title: 'Branch workspace' })
  const elsewhere = await post('/sessions', { title: 'Elsewhere' })
  const sent = await post(`/sessions/${root.id}/messages`, { content: 'A message with alternatives' })
  await expect.poll(async () => (await vista.api(`/sessions/${root.id}/checkpoint`)).session.executing).toBe(false)
  const fork = () => post(`/sessions/${root.id}/messages/${sent.message.id}/revert`)
  const { session: preferred } = await fork()
  const { session: attention } = await fork()
  const url = (id: string) => `${vista.origin}/sessions/${id}`
  const row = (tab: Page, id: string) => tab.locator(`.session-card[data-session-id="${id}"]`)
  const showSidebar = async (tab: Page) => {
    if (info.project.name !== 'desktop' && !await tab.locator('#sessions-panel.mobile-open').count()) {
      await tab.getByRole('button', { name: 'Open sessions', exact: true }).click()
    }
  }
  const enter = async (tab: Page, id: string, target = id) => {
    await showSidebar(tab)
    await row(tab, id).click()
    await expect(tab).toHaveURL(url(target))
  }
  const entryId = async () => (await vista.api('/sessions?open=true')).find((item: { id: string }) => item.id === root.id)?.branchGroup?.entryId
  const device = await browser.newContext({ viewport: info.project.use.viewport, isMobile: info.project.use.isMobile, hasTouch: info.project.use.hasTouch, locale: 'en-US' })
  const other = await device.newPage()
  try {
    await page.goto(url(root.id))
    await other.goto(url(root.id))
    await expect(other.locator('article.user')).toContainText('A message with alternatives')
    await expect.poll(entryId).toBe(root.id) // Forking didn't select a preference.
    await page.locator('article.user').getByRole('button', { name: 'Next branch', exact: true }).click()
    await expect(page).toHaveURL(url(preferred.id))
    await expect.poll(entryId).toBe(preferred.id)

    // This reply begins with a question tool call, not streamed text.
    // Hold it before output so the executing badge stays observable.
    const release = vista.pauseModelReplies()
    try {
      await post(`/sessions/${attention.id}/messages`, { content: 'Ask me about the setup' })
      await showSidebar(other)
      await expect(row(other, root.id).getByRole('button', { name: 'Executing', exact: true })).toBeVisible()
      // The other device did not switch branches; on mobile its drawer is still open.
      await expect(other).toHaveURL(url(root.id) + (info.project.name === 'desktop' ? '' : '?layer=sessions-drawer:'))
      await enter(other, elsewhere.id)
      await enter(other, root.id, preferred.id)
      await expect(row(other, root.id)).toHaveAttribute('aria-current', 'page')
      await expect(row(other, preferred.id)).toHaveCount(0)
    } finally { release() }

    await showSidebar(other)
    await row(other, root.id).getByRole('button', { name: 'Waiting for your answer', exact: true }).click()
    await expect(other).toHaveURL(url(attention.id))
    await expect(other.locator('.sender-card .question-form')).toBeVisible()
    expect(await entryId()).toBe(preferred.id)
    await other.goto(url(root.id))
    await other.reload()
    await expect(other.locator('article.user')).toContainText('A message with alternatives')
    expect(await entryId()).toBe(preferred.id) // Exact URLs and reloads don't write preferences.

    await post(`/sessions/${preferred.id}/close`)
    await other.goto(url(elsewhere.id))
    await enter(other, root.id)
    expect((await vista.api(`/sessions/${preferred.id}/checkpoint`)).session.open).toBe(false)
    await post(`/sessions/${root.id}/close`)
    await other.goto(url(elsewhere.id))
    await enter(other, root.id, attention.id)
    await expect(row(other, root.id)).toHaveAttribute('aria-current', 'page')
    expect((await vista.api(`/sessions/${root.id}/checkpoint`)).session.open).toBe(false)
    await other.goto(`${vista.origin}/sessions`)
    const managed = other.locator(`.managed-session[data-session-id="${root.id}"]`)
    await expect(managed.locator('.managed-session-meta')).not.toContainText('Closed')
    await managed.locator('.managed-session-entry').click()
    await expect(other).toHaveURL(url(attention.id))
  } finally {
    await device.close()
  }
})


test('closing and deleting a conversation apply once to every branch', { tag: '@responsive' }, async ({ page, context, vista, isMobile }) => {
  test.setTimeout(55_000)
  const post = (path: string, body: object = {}) => vista.api(path, { method: 'POST', body: JSON.stringify(body) })
  const root = await post('/sessions', { title: 'One conversation, several versions' })
  const other = await post('/sessions', { title: 'Unrelated conversation' })
  const sent = await post(`/sessions/${root.id}/messages`, { content: 'Original saved input', modelKey: 'test' })
  const checkpoint = (id: string) => vista.api(`/sessions/${id}/checkpoint`)
  await expect.poll(async () => (await checkpoint(root.id)).session.executing).toBe(false)
  const fork = async () => (await post(`/sessions/${root.id}/messages/${sent.message.id}/revert`)).session
  const active = await fork()
  const closed = await fork()
  // Use the compatibility endpoint to stage an already closed root and a
  // closed branch. The WebUI still manages one complete conversation.
  await post(`/sessions/${root.id}/close`)
  await post(`/sessions/${closed.id}/close`)
  const ids = [root.id, active.id, closed.id]
  const row = page.locator(`.session-card[data-session-id="${root.id}"]`)
  const openSidebar = async () => {
    if (isMobile && !await page.locator('#sessions-panel.mobile-open').count()) {
      await page.getByRole('button', { name: 'Open sessions', exact: true }).tap()
    }
  }
  const mutations: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'DELETE' || request.method() === 'POST' && path.endsWith('/close')) mutations.push(path)
  })
  const observe = await context.newPage()
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  await observe.goto(`${vista.origin}/sessions/${active.id}`)
  const release = vista.pauseModelReplies()
  try {
    await post(`/sessions/${active.id}/messages`, { content: 'A branch with live work', modelKey: 'test' })
    await expect.poll(async () => (await checkpoint(active.id)).session.executing).toBe(true)
    await expect(observe.locator('article.user')).toContainText('A branch with live work')
    await openSidebar()
    await row.getByRole('button', { name: 'Session menu', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Close', exact: true }).click()
    const confirmation = page.getByRole('dialog', { name: 'Close session?', exact: true })
    expect(mutations).toEqual([])
    await expect(confirmation).toContainText('every branch')
    await confirmation.getByRole('button', { name: 'Close and stop', exact: true }).click()
    await expect(confirmation).toBeHidden()
    await expect(row).toHaveCount(0)
    await expect(page).toHaveURL(vista.origin + '/')
    for (const id of ids) expect((await checkpoint(id)).session).toMatchObject({ open: false, executing: false })
    await expect(observe.locator('.sender-card textarea')).toBeDisabled()
    await expect(observe.locator('article.user')).toContainText('A branch with live work')
    expect((await vista.api('/status')).workerMetrics.every((worker: { sessionId: string }) => !ids.includes(worker.sessionId))).toBe(true)
  } finally { release() }
  await openSidebar()
  await page.getByRole('button', { name: 'Manage sessions', exact: true }).click()
  const manager = page.locator('.session-manager-page')
  const group = manager.locator(`.managed-session[data-session-id="${root.id}"]`)
  await expect(group).toBeVisible()
  await group.locator('.managed-session-entry').click()
  await expect(page).toHaveURL(`${vista.origin}/sessions/${root.id}`)
  expect((await checkpoint(active.id)).session.open).toBe(false)
  const next = page.locator('article.user').getByRole('button', { name: 'Next branch', exact: true })
  if (isMobile) await next.tap()
  else await next.click()
  await expect(page).toHaveURL(`${vista.origin}/sessions/${active.id}`)
  await expect(page.locator('article.user')).toContainText('A branch with live work')
  expect((await checkpoint(active.id)).session).toMatchObject({ open: true, executing: false })
  expect((await checkpoint(closed.id)).session.open).toBe(false)
  // Delete the group from the manager while a derived conversation is visible.
  await openSidebar()
  await page.getByRole('button', { name: 'Manage sessions', exact: true }).click()
  await expect(page).toHaveURL(url => url.pathname === '/sessions' && url.searchParams.get('session') === active.id)
  await selectSessions(manager)
  await group.getByRole('checkbox').check()
  await manager.getByRole('button', { name: 'Delete selected', exact: true }).click()
  const deleteConfirm = page.getByRole('dialog', { name: 'Delete selected', exact: true })
  await expect(deleteConfirm).toContainText('All branches will be permanently deleted.')
  expect(mutations).toEqual([`/api/sessions/${root.id}/group/close`])
  await deleteConfirm.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(group).toHaveCount(0)
  await expect(page).toHaveURL(url => url.pathname === '/sessions' && !url.searchParams.has('session'))
  await expect(observe).toHaveURL(vista.origin + '/')
  for (const id of ids) expect((await page.request.get(`${vista.origin}/api/sessions/${id}/checkpoint`)).status()).toBe(404)
  expect((await checkpoint(other.id)).session.open).toBe(true)
  expect(mutations).toEqual([`/api/sessions/${root.id}/group/close`, `/api/sessions/${root.id}/group`])
})
