import { test, expect } from './fixtures'
import { closeSettings } from './settings-navigation'

const sections = [
  ['general', 'Appearance'], ['personalization', 'Personalization'],
  ['models', 'Service connections'], ['search', 'Model web access'], ['mcp', 'MCP'],
  ['skills', 'Skill library'], ['network', 'Network configuration'], ['statistics', 'Usage'], ['storage', 'Storage'], ['about', 'About Vista'],
] as const

test.beforeEach(async ({ vista }) => {
})

test('settings entry and category returns preserve the conversation and draft', { tag: '@smoke' }, async ({ page, vista }) => {
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
  await expect(nav.getByRole('link')).toHaveText(sections.map(([, label]) => label))
  if (mobile) {
    await expect(settings.locator('.ui-modal-title')).toHaveText('Settings')
    await expect(settings.locator('.settings-content')).toHaveCount(0)
  }
  for (const [path, label] of sections) {
    const link = nav.getByRole('link', { name: label, exact: true })
    if (mobile) expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await link.click()
    await expect(page).toHaveURL(url => url.pathname === `/settings/${path}` && url.searchParams.get('session') === session.id)
    await expect(settings.locator('.ui-modal-title')).toHaveText(label)
    await expect(settings.locator('.settings-content')).toBeVisible()
    if (path === 'mcp') {
      await expect(settings.locator('.network-policy-summary')).toHaveCount(0)
      await expect(settings.locator('.mcp-settings').getByRole('link', { name: 'Network configuration', exact: true })).toHaveCount(0)
    }
    if (mobile) {
      await expect(nav).toBeHidden()
      await expect(settings.locator('.ui-modal-title')).toBeFocused()
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
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
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
  await expect(page.locator('.settings-page .ui-modal-title')).toHaveText('Service connections')
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

test('advanced settings is one history entry above About', { tag: '@smoke' }, async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const conversation = `${vista.origin}/sessions/${session.id}`
  await page.goto(conversation)
  const mobile = page.viewportSize()!.width <= 830
  if (mobile) await page.locator('.mobile-sessions-trigger:visible').click()
  await page.locator('.settings-trigger').click()
  await page.getByRole('link', { name: 'About Vista', exact: true }).click()
  const about = `${vista.origin}/settings/about?session=${session.id}`
  await expect(page).toHaveURL(about)
  const trigger = page.getByRole('button', { name: 'Advanced settings', exact: true })
  const dialog = page.getByRole('dialog', { name: 'Advanced settings', exact: true })
  await trigger.click()
  await expect(dialog).toBeVisible()
  await page.goBack()
  await expect(dialog).toHaveCount(0)
  await expect(page).toHaveURL(about)
  await expect(trigger).toBeFocused()
  await page.goForward()
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(page).toHaveURL(about)
  await expect(dialog).toHaveCount(0)
  if (mobile) {
    await page.goBack()
    await expect(page).toHaveURL(url => url.pathname === '/settings' && url.searchParams.get('session') === session.id)
  }
  await page.goBack()
  await expect(page).toHaveURL(conversation)
})

test('cancelling a guarded history jump keeps the whole workspace visible', async ({ page, vista }) => {
  await page.goto(`${vista.origin}/`)
  const mobile = page.viewportSize()!.width <= 830
  if (mobile) await page.locator('.mobile-sessions-trigger:visible').click()
  await page.locator('.settings-trigger').click()
  await page.getByRole('link', { name: 'About Vista', exact: true }).click()
  await page.getByRole('button', { name: 'Advanced settings', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Advanced settings', exact: true })
  await dialog.getByRole('tab', { name: 'Debug mode', exact: true }).click()
  const debug = dialog.getByRole('switch', { name: 'Debug mode', exact: true })
  await debug.click()
  await expect(debug).toBeChecked()
  const address = page.url()
  await page.evaluate(steps => history.go(-steps), mobile ? 3 : 2)
  const discard = page.getByRole('dialog', { name: 'Discard unsaved changes', exact: true })
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page).toHaveURL(address)
  await expect(dialog).toBeVisible()
  await expect(debug).toBeChecked()
  await expect(page.locator('.settings-page')).toBeVisible()
})

test('a directly opened settings layer has an in-app parent after refresh', async ({ page, vista }) => {
  const profile = await vista.api('/profiles', { method: 'POST', body: JSON.stringify({ name: 'Other workspace' }) })
  const about = `${vista.origin}/profiles/${profile.id}/settings/about`
  await page.goto(`${about}?layer=advanced:`)
  await page.reload()
  const dialog = page.getByRole('dialog', { name: 'Advanced settings', exact: true })
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(page).toHaveURL(about)
  await page.goto(`${vista.origin}/settings/general?layer=advanced:`)
  await expect(page).toHaveURL(`${vista.origin}/settings/general`)
  await expect(dialog).toHaveCount(0)
})

test('the header compaction icon exposes the context estimate without taking composer space', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  let tokens: number | null = 3940
  let usageReads = 0
  let compactRequests = 0
  await page.route('**/api/sessions/*/context-usage', route => {
    usageReads++
    return route.fulfill({ json: tokens === null ? null : { tokens, sequence: 0 } })
  })
  page.on('request', request => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === `/api/sessions/${session.id}/compact`) compactRequests++
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const usage = page.locator('.conversation-header .context-compaction')
  const panel = page.getByRole('dialog', { name: 'Context', exact: true })
  const estimate = panel.locator('.context-usage-estimate')
  await expect(usage).toHaveText('')
  await expect(usage).toBeEnabled()
  await expect(usage).toHaveAttribute('data-tokens', '3940')
  await expect(page.locator('.sender-card .context-compaction, .sender-card .context-usage')).toHaveCount(0)
  await expect(page.locator('.composer-send-label')).toHaveText('Send')
  await expect(page.locator('.conversation-header-tools').getByRole('button', { name: 'Response options', exact: true })).toBeVisible()
  const bounds = await usage.evaluate(element => {
    const box = element.getBoundingClientRect()
    const header = element.closest('.conversation-header')!.getBoundingClientRect()
    const options = element.closest('.conversation-header-tools')!.querySelector('.response-options-trigger')!.getBoundingClientRect()
    return { left: box.left, top: box.top, bottom: box.bottom, headerRight: header.right, headerTop: header.top,
      headerBottom: header.bottom, viewportWidth: innerWidth, gap: options.left - box.right, optionsRight: options.right }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(bounds.optionsRight).toBeLessThanOrEqual(bounds.viewportWidth)
  expect(bounds.gap).toBeGreaterThanOrEqual(0)
  expect(bounds.gap).toBeLessThanOrEqual(12)
  expect(bounds.headerRight - bounds.optionsRight).toBeLessThanOrEqual(40)
  expect(bounds.top).toBeGreaterThanOrEqual(bounds.headerTop)
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.headerBottom)
  const before = usageReads
  await expect(panel).toBeHidden()
  await page.getByRole('button', { name: 'Rename', exact: true }).focus()
  await page.keyboard.press('Tab')
  await expect(usage).toBeFocused()
  await expect(panel).toBeHidden()
  await page.keyboard.press('Enter')
  await expect(estimate).toHaveText('Estimated tokens from retained instructions, messages, summaries and tool content: 3.9K.')
  await expect(panel.getByRole('button', { name: 'Compact context', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(usage).toBeFocused()
  expect(usageReads).toBe(before)

  for (const [count, display] of [[0, '0'], [876, '876'], [999950, '1M']] as const) {
    tokens = count
    await page.reload()
    await expect(usage).toHaveAttribute('data-tokens', String(count))
    if (testInfo.project.use.hasTouch) await usage.tap()
    else await usage.click()
    await expect(estimate.locator('strong')).toHaveText(display)
  }
  await page.evaluate(() => localStorage.setItem('vista-language', 'zh-CN'))
  tokens = 1240000
  await page.reload()
  const chinesePanel = page.getByRole('dialog', { name: '上下文', exact: true })
  await expect(usage).toHaveAccessibleName('上下文')
  await expect(usage).toHaveAttribute('data-tokens', String(tokens))
  await usage.click()
  await expect(chinesePanel.locator('.context-usage-estimate')).toHaveText('根据当前保留的指令、消息、摘要和工具内容估算词元：1.2M。')
  await page.screenshot({ path: testInfo.outputPath('context-popover.png'), animations: 'disabled' })
  tokens = null
  await page.reload()
  await usage.click()
  await expect(chinesePanel.locator('.context-usage-estimate')).toHaveText('暂未获得估算。')
  const outside = page.locator('.conversation-heading h2')
  if (testInfo.project.use.hasTouch) await outside.tap()
  else await outside.click()
  await expect(chinesePanel).toBeHidden()
  expect(compactRequests).toBe(0)
})
