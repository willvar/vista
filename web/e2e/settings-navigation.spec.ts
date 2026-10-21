import { test, expect } from './fixtures'
import { closeSettings } from './settings-navigation'

const sections = [
  ['general', 'Appearance'], ['personalization', 'Personalization'],
  ['models', 'Model connections'], ['search', 'Web queries'], ['mcp', 'MCP'],
  ['skills', 'Skill library'], ['storage', 'Storage'], ['about', 'About Vista'],
] as const

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

test('settings entry and category returns preserve the conversation and draft', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const draft = page.locator('.sender-card textarea')
  await draft.fill('Keep this draft while I browse settings')
  const mobile = page.viewportSize()!.width <= 830
  if (mobile) await page.locator('.mobile-sessions-trigger:visible').click()
  await page.locator('.settings-trigger').click()
  await expect(page).toHaveURL(url => url.pathname === (mobile ? '/settings' : '/settings/general') && url.searchParams.get('session') === session.id)
  const settings = page.locator('.settings-page')
  const nav = settings.locator('.settings-nav')
  await expect(nav).toBeVisible()
  await expect(nav.getByRole('link')).toHaveCount(8)
  if (mobile) {
    await expect(settings.locator('.page-heading h2')).toHaveText('Settings')
    await expect(settings.locator('.settings-content')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('settings-directory.png'), animations: 'disabled' })
  }
  for (const [path, label] of sections) {
    const link = nav.getByRole('link', { name: label, exact: true })
    if (mobile) expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await link.click()
    await expect(page).toHaveURL(url => url.pathname === `/settings/${path}` && url.searchParams.get('session') === session.id)
    await expect(settings.locator('.page-heading h2')).toHaveText(label)
    await expect(settings.locator('.settings-content')).toBeVisible()
    if (mobile) {
      await expect(nav).toBeHidden()
      await expect(settings.locator('.page-heading h2')).toBeFocused()
    } else await expect(nav).toBeVisible()
    if (path === 'models') {
      await settings.getByRole('button', { name: 'Connect provider', exact: true }).click()
      const form = page.getByRole('dialog', { name: 'Connect provider', exact: true })
      await expect(form).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(form).toHaveCount(0)
      await expect(page).toHaveURL(url => url.pathname === '/settings/models')
    }
    if (mobile) {
      await settings.getByRole('button', { name: 'Back to settings', exact: true }).click()
      await expect(page).toHaveURL(url => url.pathname === '/settings' && url.searchParams.get('session') === session.id)
      await expect(nav).toBeVisible()
    }
  }
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.screenshot({ path: testInfo.outputPath('settings-navigation-dark.png'), animations: 'disabled' })
  await closeSettings(page)
  await expect(page).toHaveURL(`${vista.origin}/sessions/${session.id}`)
  await expect(draft).toHaveValue('Keep this draft while I browse settings')
  await expect(draft).toBeFocused()
})

test('direct settings links, reload, browser history and viewport changes keep navigation usable', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const query = `?session=${session.id}`
  await page.goto(`${vista.origin}/settings/models${query}`)
  await page.reload()
  await expect(page.locator('.settings-page .page-heading h2')).toHaveText('Model connections')
  if (page.viewportSize()!.width > 830) await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: 'Back to settings', exact: true }).click()
  await expect(page).toHaveURL(`${vista.origin}/settings${query}`)
  await page.reload()
  await expect(page.locator('.settings-index .settings-nav')).toBeVisible()
  await page.locator('.settings-nav').getByRole('link', { name: 'MCP', exact: true }).click()
  await expect(page).toHaveURL(`${vista.origin}/settings/mcp${query}`)
  await page.goBack()
  await expect(page).toHaveURL(`${vista.origin}/settings${query}`)
  await expect(page.locator('.settings-nav')).toBeVisible()
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(page).toHaveURL(`${vista.origin}/settings/general${query}`)
  await expect(page.locator('.settings-nav')).toBeVisible()
  await expect(page.locator('.settings-content')).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('.settings-nav')).toBeHidden()
  await closeSettings(page)
  await expect(page).toHaveURL(`${vista.origin}/sessions/${session.id}`)
})

test('compact context estimate sits beside the submit controls and explains itself on hover or click', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.route('**/api/sessions/*/context-usage', route => route.fulfill({ json: { tokens: 3940, sequence: 0 } }))
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const usage = page.locator('.context-usage')
  const hint = page.locator('.context-usage-hint:visible')
  await expect(usage).toHaveText('3.9K')
  await expect(page.locator('.sender-card .context-status-panel')).toHaveCount(0)
  const bounds = await usage.evaluate(element => {
    const box = element.getBoundingClientRect()
    const submit = element.nextElementSibling!.getBoundingClientRect()
    return { left: box.left, right: box.right, middle: box.y + box.height / 2, submitLeft: submit.left, submitTop: submit.top, submitBottom: submit.bottom, viewportWidth: innerWidth }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(bounds.right).toBeLessThanOrEqual(bounds.submitLeft)
  expect(bounds.middle).toBeGreaterThanOrEqual(bounds.submitTop)
  expect(bounds.middle).toBeLessThanOrEqual(bounds.submitBottom)
  expect(bounds.right).toBeLessThanOrEqual(bounds.viewportWidth)
  const explanation = 'Estimated from retained instructions, messages, summaries and tool content.'
  await usage.hover()
  await expect(hint).toHaveText(explanation)
  await page.keyboard.press('Escape')
  await expect(hint).toHaveCount(0)
  await page.locator('.sender-card textarea').click()
  await usage.click()
  await expect(hint).toHaveText(explanation)
  await page.keyboard.press('Escape')
  await expect(hint).toHaveCount(0)
  await page.evaluate(() => localStorage.setItem('vista-language', 'zh-CN'))
  await page.reload()
  await expect(usage).toHaveText('3.9K')
  await usage.click()
  await expect(hint).toHaveText('根据当前保留的指令、消息、摘要和工具内容估算。')
  await page.screenshot({ path: testInfo.outputPath('compact-context-hint.png'), animations: 'disabled' })
})
