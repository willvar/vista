import { installMockStateStream } from './state-stream-fixture'
import { openSettingsSection } from './settings-navigation'
import { test, expect } from './frontend-fixture'
import type { Locator, Page } from '@playwright/test'
import { fillTextFixture } from './text-fixture'

async function mockAgentSettings(page: Page, origin: string, section = 'general') {
  const saved = { instructions: 'Keep responses concise.', instructionsPath: '/home/test/.config/vista/AGENTS.md', delegationDepth: 1, maxDelegationDepth: 8, automaticTitles: true }
  const puts: { instructions?: string; delegationDepth?: number; automaticTitles?: boolean }[] = []
  const forbidden: string[] = []
  const unexpected: string[] = []
  await page.addInitScript(() => {
    if (!localStorage.getItem('vista-language')) localStorage.setItem('vista-language', 'en')
  })
  await installMockStateStream(page)
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice(4)
    const method = request.method()
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/activity' && method === 'POST') return route.fulfill({ status: 204 })
    if (path === '/agent-settings' && method === 'GET') return reply(saved)
    if (path === '/memories' && method === 'GET') return reply({ memories: [], maxCount: 128, maxBytes: 4096, maxTotalBytes: 32768 })
    if (path === '/agent-settings' && method === 'PUT') {
      const body = request.postDataJSON()
      puts.push(body)
      if (body.instructions !== undefined && typeof body.instructions !== 'string'
        || body.delegationDepth !== undefined && (!Number.isInteger(body.delegationDepth) || body.delegationDepth < 0 || body.delegationDepth > saved.maxDelegationDepth)
        || body.automaticTitles !== undefined && typeof body.automaticTitles !== 'boolean') {
        return route.fulfill({ status: 400, body: 'Invalid settings' })
      }
      Object.assign(saved, body)
      return reply(saved)
    }
    if (method !== 'GET') { forbidden.push(`${method} ${path}`); return route.abort() }
    if (path === '/preferences') return reply({ userId: 'settings-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: '', reasoningByModelId: {}, feedbackInvitationSeen: true })
    if (path === '/models') return reply({ default: '', models: [] })
    if (path === '/sessions') return reply([])
    if (path === '/status') return reply({ state: 'running', sessions: { total: 0, open: 0, closed: 0, running: 0, workers: 0 }, workerMetrics: [] })
    unexpected.push(`${method} ${path}`)
    return route.fulfill({ status: 404, body: 'Unmocked API request' })
  })
  await page.goto(`${origin}/settings/${section}`)
  if (section === 'general') await expect(page.getByRole('group', { name: 'Color mode', exact: true })).toBeVisible()
  else await expect(page.getByRole('heading', { name: 'Global instructions', exact: true })).toBeVisible()
  return { saved, puts, forbidden, unexpected }
}

async function reachable(control: Locator) {
  await control.scrollIntoViewIfNeeded()
  await expect(control).toBeVisible()
  const bounds = await control.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight, uncovered: !!hit && (hit === element || element.contains(hit)) }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(-1)
  expect(bounds.right).toBeLessThanOrEqual(bounds.width + 1)
  expect(bounds.top).toBeGreaterThanOrEqual(-1)
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.height + 1)
  expect(bounds.uncovered).toBe(true)
}

async function noHorizontalOverflow(page: Page) {
  const result = await page.locator('.settings-page:visible').evaluate(section => {
    const modal = section.closest('.ui-modal-container')!
    const content = section.querySelector('.settings-content')!
    return {
      viewport: innerWidth,
      page: document.documentElement.scrollWidth,
      modal: modal.scrollWidth - modal.clientWidth,
      content: content.scrollWidth - content.clientWidth,
      form: Math.max(0, ...Array.from(section.querySelectorAll('form'), form => form.scrollWidth - form.clientWidth)),
    }
  })
  expect(result.page).toBeLessThanOrEqual(result.viewport + 2)
  expect(result.modal).toBeLessThanOrEqual(2)
  expect(result.content).toBeLessThanOrEqual(2)
  expect(result.form).toBeLessThanOrEqual(2)
}

test('automatic titles toggle persists at the bottom of Appearance with an independent update', async ({ page, mockOrigin }) => {
  const fixture = await mockAgentSettings(page, mockOrigin)
  const setting = page.locator('.automatic-title-setting')
  const toggle = setting.getByRole('switch', { name: 'Generate conversation titles', exact: true })
  await expect(toggle).toBeEnabled()
  await expect(toggle).toBeChecked()
  await expect(setting).toContainText('extra model request')
  await reachable(toggle)
  await toggle.click()
  await expect(toggle).not.toBeChecked()
  await expect.poll(() => fixture.puts).toEqual([{ automaticTitles: false }])
  expect(fixture.saved.instructions).toBe('Keep responses concise.')
  expect(fixture.saved.delegationDepth).toBe(1)
  await noHorizontalOverflow(page)
  const layout = await setting.evaluate(element => {
    const label = element.querySelector('label')!.getBoundingClientRect()
    const toggle = element.querySelector('[role="switch"]')!.getBoundingClientRect()
    return { switchLeft: toggle.left, labelRight: label.right, isLast: element === element.parentElement!.lastElementChild }
  })
  expect(layout.isLast).toBe(true)
  expect(layout.switchLeft).toBeGreaterThan(layout.labelRight)
  await page.reload()
  await expect(toggle).toBeEnabled()
  await expect(toggle).not.toBeChecked()
  await toggle.click()
  await expect.poll(() => fixture.puts).toEqual([{ automaticTitles: false }, { automaticTitles: true }])
  await openSettingsSection(page, 'personalization')
  await expect(toggle).toHaveCount(0)
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})

test('Personalization saves delegation depth and instructions independently', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockAgentSettings(page, mockOrigin)
  const settings = page.locator('.settings-page:visible')
  await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText('Appearance')
  await expect(settings.getByRole('spinbutton')).toHaveCount(0)
  await openSettingsSection(page, 'personalization')
  await expect(settings.getByRole('heading', { name: 'Delegation depth', exact: true })).toBeVisible()
  await expect(settings.getByRole('textbox', { name: 'Global instructions', exact: true })).toHaveCount(0)
  await expect(settings.getByRole('spinbutton', { name: 'Delegation depth', exact: true })).toHaveCount(0)
  await expect(settings.getByRole('complementary', { name: 'Want to add rules for a specific folder?', exact: true })).toContainText('AGENTS.md')
  await settings.getByRole('button', { name: 'Full-screen editor', exact: true }).click()
  const instructionsDialog = page.getByRole('dialog', { name: 'Global instructions', exact: true })
  const instructions = instructionsDialog.getByRole('textbox', { name: 'Global instructions', exact: true })
  await instructions.fill('Keep this instruction draft unsaved.')
  await instructions.press('Escape')
  await settings.locator('.delegation-depth-setting').getByRole('button', { name: 'Edit', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Delegation depth', exact: true })
  const depth = dialog.getByRole('spinbutton', { name: 'Delegation depth', exact: true })
  const save = dialog.getByRole('button', { name: 'Save changes', exact: true })
  await expect(depth).toHaveValue('1')
  await expect(depth).toHaveAttribute('aria-valuemin', '0')
  await expect(depth).toHaveAttribute('aria-valuemax', '8')
  await expect(save).toBeDisabled()
  for (const value of [2, 3, 0, 8]) {
    if (value === 0) {
      await depth.fill('9')
      await expect(save).toBeDisabled()
      await depth.fill('')
      await expect(save).toBeDisabled()
    }
    await depth.fill(String(value))
    if (value === 8) await expect(dialog.getByRole('button', { name: 'Increase value', exact: true })).toBeDisabled()
    await expect(save).toBeEnabled()
    await reachable(save)
    await save.click()
    await expect.poll(() => fixture.saved.delegationDepth).toBe(value)
    await expect(save).toBeDisabled()
    expect(fixture.puts.at(-1)).toEqual({ delegationDepth: value })
  }
  expect(fixture.saved.instructions).toBe('Keep responses concise.')
  await page.screenshot({ path: testInfo.outputPath('agent-depth-saved.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await noHorizontalOverflow(page)
  await settings.getByRole('button', { name: 'Full-screen editor', exact: true }).click()
  await expect(instructions).toHaveValue('Keep this instruction draft unsaved.')
  const saveInstructions = instructionsDialog.getByRole('button', { name: 'Save changes', exact: true })
  await instructions.fill('Use project instructions before changing code.\n保留用户指定的限制。')
  fixture.saved.delegationDepth = 4
  await saveInstructions.click()
  await expect.poll(() => fixture.puts.length).toBe(5)
  expect(fixture.puts[4]).toEqual({ instructions: 'Use project instructions before changing code.\n保留用户指定的限制。' })
  expect(fixture.saved.delegationDepth).toBe(4)
  await page.reload()
  await settings.getByRole('button', { name: 'Full-screen editor', exact: true }).click()
  await expect(instructions).toHaveValue(fixture.saved.instructions)
  await expect(saveInstructions).toBeDisabled()
  await page.keyboard.press('Escape')
  await settings.locator('.delegation-depth-setting').getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(depth).toHaveValue('4')
  await expect(save).toBeDisabled()
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})

test('full-screen instructions keep an unsaved draft, focus the editor and save long content', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockAgentSettings(page, mockOrigin, 'personalization')
  const settings = page.locator('.settings-page:visible')
  const expand = settings.getByRole('button', { name: 'Full-screen editor', exact: true })
  await expect(settings.getByRole('textbox', { name: 'Global instructions', exact: true })).toHaveCount(0)
  await expect(settings.getByRole('button', { name: 'Save changes', exact: true })).toHaveCount(0)
  await expect(settings.locator('.instructions-figure')).toBeVisible()
  await expand.click()
  const dialog = page.locator('.instructions-editor-modal')
  const editor = dialog.getByRole('textbox', { name: 'Global instructions', exact: true })
  const save = dialog.getByRole('button', { name: 'Save changes', exact: true })
  await expect(editor).toBeFocused()
  await expect(editor).toHaveValue(fixture.saved.instructions)
  await expect(save).toBeDisabled()
  const draft = Array.from({ length: 1800 }, (_, index) => `${index + 1}. Keep this constraint.`).join('\n') + '\n保留用户指定的要求。'
  await fillTextFixture(editor, draft)
  const viewport = page.viewportSize()!
  const bounds = await dialog.boundingBox()
  expect(bounds!.x).toBe(0)
  expect(bounds!.y).toBe(0)
  expect(bounds!.width).toBe(viewport.width)
  expect(bounds!.height).toBe(viewport.height)
  await reachable(editor)
  expect(await editor.evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true)
  await editor.evaluate(element => { element.scrollTop = element.scrollHeight })
  expect(await editor.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
  await reachable(save)
  await page.screenshot({ path: testInfo.outputPath('instructions-fullscreen-light.png'), animations: 'disabled' })
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.screenshot({ path: testInfo.outputPath('instructions-fullscreen-dark.png'), animations: 'disabled' })
  await editor.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(expand).toBeFocused()
  expect(fixture.puts).toEqual([])
  await expand.click()
  await expect(editor).toHaveValue(draft)
  await fillTextFixture(editor, '中'.repeat(18000))
  expect(await editor.evaluate(element => (element as HTMLTextAreaElement).checkValidity())).toBe(true)
  await dialog.getByRole('button', { name: 'Exit full screen', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expand.click()
  await expect(editor).toHaveValue('中'.repeat(18000))
  await fillTextFixture(editor, draft)
  await save.click()
  await expect.poll(() => fixture.puts).toEqual([{ instructions: draft }])
  await expect(save).toBeDisabled()
  await page.reload()
  await expand.click()
  await expect(editor).toHaveValue(draft)
  await expect(save).toBeDisabled()
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})

test('Appearance and Personalization remain usable in English and Chinese', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockAgentSettings(page, mockOrigin)
  for (const language of ['en', 'zh-CN']) {
    await page.evaluate(value => localStorage.setItem('vista-language', value), language)
    await page.goto(`${mockOrigin}/settings/general`)
    const settings = page.locator('.settings-page:visible')
    const labels = language === 'en' ? { depth: 'Delegation depth', instructions: 'Global instructions', edit: 'Edit', expand: 'Full-screen editor', exit: 'Exit full screen', save: 'Save changes' } : { depth: '委派层数', instructions: '全局提示词', edit: '编辑', expand: '全屏编辑', exit: '退出全屏', save: '保存更改' }
    await expect(settings.getByRole('group', { name: language === 'en' ? 'Color mode' : '颜色模式', exact: true })).toBeVisible()
    await expect(settings.locator('.settings-nav')).not.toContainText(/Agent|Subagent/i)
    await expect(settings.locator('.automatic-title-setting')).toBeVisible()
    await noHorizontalOverflow(page)
    await page.screenshot({ path: testInfo.outputPath(`appearance-${language}.png`), animations: 'disabled' })
    await openSettingsSection(page, 'personalization')
    await expect(settings.getByRole('heading', { name: labels.instructions, exact: true })).toBeVisible()
    await expect(settings.getByRole('heading', { name: labels.depth, exact: true })).toBeVisible()
    await expect(settings.locator('.instructions-figure')).toBeVisible()
    await expect(settings.getByRole('spinbutton')).toHaveCount(0)
    await expect(settings.getByRole('textbox')).toHaveCount(0)
    await noHorizontalOverflow(page)
    await settings.locator('.delegation-depth-setting').getByRole('button', { name: labels.edit, exact: true }).click()
    const depthDialog = page.getByRole('dialog', { name: labels.depth, exact: true })
    await expect(depthDialog.getByRole('spinbutton', { name: labels.depth, exact: true })).toHaveValue('1')
    await reachable(depthDialog.getByRole('button', { name: labels.save, exact: true }))
    await page.screenshot({ path: testInfo.outputPath(`agent-depth-${language}.png`), animations: 'disabled' })
    await page.keyboard.press('Escape')
    await settings.getByRole('button', { name: labels.expand, exact: true }).click()
    const editor = page.locator('.instructions-editor-modal')
    await expect(editor).not.toContainText(fixture.saved.instructionsPath)
    await expect(editor.getByRole('textbox', { name: labels.instructions, exact: true })).toBeFocused()
    await editor.getByRole('textbox', { name: labels.instructions, exact: true }).fill(language === 'en' ? 'Follow the review checklist.' : '遵循审核清单。')
    await reachable(editor.getByRole('button', { name: labels.save, exact: true }))
    await expect(editor.getByRole('button', { name: labels.save, exact: true })).toBeEnabled()
    await editor.getByRole('button', { name: labels.exit, exact: true }).click()
    await expect(editor).toBeHidden()
    await expect(settings).toBeVisible()
  }
  expect(fixture.puts).toEqual([])
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})
