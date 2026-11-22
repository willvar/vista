import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

test.use({ questionTool: true, replyWithPrompt: true })

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
      await expect(other).toHaveURL(url(root.id)) // The other device didn't move.
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
