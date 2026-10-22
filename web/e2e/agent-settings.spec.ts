import { openSettingsSection } from './settings-navigation'
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { createServer } from 'vite'
import { resolve } from 'node:path'

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({
      root: resolve(import.meta.dirname, '..'),
      configFile: resolve(import.meta.dirname, '../vite.config.ts'),
      server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } },
    })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close() }
  }, { scope: 'worker' }],
})

async function mockAgentSettings(page: Page, origin: string, section = 'general') {
  const saved = { instructions: 'Keep responses concise.', instructionsPath: '/home/test/.config/vista/AGENTS.md', delegationDepth: 1, maxDelegationDepth: 8 }
  const puts: { instructions?: string; delegationDepth?: number }[] = []
  const forbidden: string[] = []
  const unexpected: string[] = []
  await page.addInitScript(() => {
    if (!localStorage.getItem('vista-language')) localStorage.setItem('vista-language', 'en')
  })
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice(4)
    const method = request.method()
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/agent-settings' && method === 'GET') return reply(saved)
    if (path === '/memories' && method === 'GET') return reply({ memories: [], maxCount: 128, maxBytes: 4096, maxTotalBytes: 32768 })
    if (path === '/agent-settings' && method === 'PUT') {
      const body = request.postDataJSON()
      puts.push(body)
      if (body.instructions !== undefined && typeof body.instructions !== 'string'
        || body.delegationDepth !== undefined && (!Number.isInteger(body.delegationDepth) || body.delegationDepth < 0 || body.delegationDepth > saved.maxDelegationDepth)) {
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
  else await expect(page.locator('#agent-instructions')).toBeEnabled()
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
    const modal = section.closest('.ant-modal-container')!
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

test('Personalization saves delegation depth and instructions independently', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockAgentSettings(page, mockOrigin)
  const settings = page.locator('.settings-page:visible')
  const depth = settings.getByRole('spinbutton', { name: 'Delegation depth', exact: true })
  const instructions = settings.getByRole('textbox', { name: 'Global instructions', exact: true })
  const save = settings.getByRole('form', { name: 'Delegation depth', exact: true }).getByRole('button', { name: 'Save changes', exact: true })
  const saveInstructions = settings.getByRole('form', { name: 'Global instructions', exact: true }).getByRole('button', { name: 'Save changes', exact: true })
  await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText('Appearance')
  await expect(depth).toHaveCount(0)
  await expect(instructions).toHaveCount(0)
  await expect(settings.getByRole('region', { name: 'Memory', exact: true })).toHaveCount(0)
  await openSettingsSection(page, 'personalization')
  await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText('Personalization')
  await expect(instructions).toHaveValue(fixture.saved.instructions)
  await expect(depth).toHaveValue('1')
  await expect(depth).toHaveAttribute('aria-valuemin', '0')
  await expect(depth).toHaveAttribute('aria-valuemax', '8')
  await expect(save).toBeDisabled()

  await depth.fill('2')
  await expect(save).toBeEnabled()
  await reachable(save)
  await save.click()
  await expect.poll(() => fixture.puts).toEqual([{ delegationDepth: 2 }])
  await expect(save).toBeDisabled()
  await page.reload()
  await expect(depth).toHaveValue('2')
  await expect(save).toBeDisabled()

  fixture.saved.instructions = 'Instructions changed in another window.'
  await depth.fill('3')
  await expect(save).toBeEnabled()
  await save.click()
  await expect.poll(() => fixture.puts.length).toBe(2)
  expect(fixture.puts[1]).toEqual({ delegationDepth: 3 })
  expect(fixture.saved.instructions).toBe('Instructions changed in another window.')
  await page.reload()
  await expect(depth).toHaveValue('3')
  await expect(save).toBeDisabled()

  await depth.fill('9')
  await expect(save).toBeDisabled()
  await depth.fill('')
  await expect(save).toBeDisabled()
  await depth.fill('0')
  await expect(save).toBeEnabled()
  await save.click()
  await expect.poll(() => fixture.puts.length).toBe(3)
  expect(fixture.puts[2]?.delegationDepth).toBe(0)
  await page.reload()
  await expect(depth).toHaveValue('0')
  await expect(save).toBeDisabled()
  await depth.fill('8')
  await expect(save).toBeEnabled()
  await expect(settings.getByRole('button', { name: 'Increase Value', exact: true })).toBeDisabled()
  await save.click()
  await expect.poll(() => fixture.puts.length).toBe(4)
  expect(fixture.puts[3]?.delegationDepth).toBe(8)
  await noHorizontalOverflow(page)
  const screenshot = testInfo.outputPath('agent-depth-saved.png')
  await page.screenshot({ path: screenshot, animations: 'disabled' })
  await testInfo.attach('agent-depth-saved', { path: screenshot, contentType: 'image/png' })

  await expect(depth).toHaveValue('8')
  await expect(settings.locator('.instructions-file-location')).toContainText(fixture.saved.instructionsPath)
  await expect(instructions).toHaveValue('Instructions changed in another window.')
  await expect(saveInstructions).toBeDisabled()
  await instructions.fill('Use project instructions before changing code.\n保留用户指定的限制。')
  fixture.saved.delegationDepth = 4
  await saveInstructions.click()
  await expect.poll(() => fixture.puts.length).toBe(5)
  expect(fixture.puts[4]).toEqual({ instructions: 'Use project instructions before changing code.\n保留用户指定的限制。' })
  expect(fixture.saved.delegationDepth).toBe(4)
  await page.reload()
  await expect(instructions).toHaveValue(fixture.saved.instructions)
  await expect(saveInstructions).toBeDisabled()
  await expect(depth).toHaveValue('4')
  await openSettingsSection(page, 'general')
  await expect(depth).toHaveCount(0)
  await openSettingsSection(page, 'personalization')
  await expect(depth).toHaveValue('4')
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})

test('full-screen instructions keep an unsaved draft, focus the editor and scroll long content', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockAgentSettings(page, mockOrigin, 'personalization')
  const settings = page.locator('.settings-page:visible')
  const expand = settings.getByRole('button', { name: 'Full-screen editor', exact: true })
  const inline = settings.getByRole('textbox', { name: 'Global instructions', exact: true })
  await expand.click()
  const dialog = page.locator('.instructions-editor-modal')
  const editor = dialog.getByRole('textbox', { name: 'Global instructions', exact: true })
  await expect(editor).toBeFocused()
  await expect(editor).toHaveValue(fixture.saved.instructions)
  const draft = Array.from({ length: 1800 }, (_, index) => `${index + 1}. Keep this constraint.`).join('\n') + '\n保留用户指定的要求。'
  await editor.fill(draft)
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
  await page.screenshot({ path: testInfo.outputPath('instructions-fullscreen-light.png'), animations: 'disabled' })
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.screenshot({ path: testInfo.outputPath('instructions-fullscreen-dark.png'), animations: 'disabled' })
  await editor.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(settings).toBeVisible()
  await expect(expand).toBeFocused()
  await expect(inline).toHaveValue(draft)
  expect(fixture.puts).toEqual([])
  expect(fixture.saved.instructions).toBe('Keep responses concise.')

  await expand.click()
  await expect(editor).toHaveValue(draft)
  await editor.fill('中'.repeat(18000))
  await expect(editor).not.toHaveClass(/ant-input-status-error/)
  await dialog.getByRole('button', { name: 'Exit full screen', exact: true }).click()
  await expect(dialog).toBeHidden()
  const save = settings.getByRole('form', { name: 'Global instructions', exact: true }).getByRole('button', { name: 'Save changes', exact: true })
  await expect(save).toBeEnabled()
  await expand.click()
  await expect(editor).toHaveValue('中'.repeat(18000))
  await editor.fill(draft)
  await dialog.getByRole('button', { name: 'Exit full screen', exact: true }).click()
  await expect(dialog).toBeHidden()
  await save.scrollIntoViewIfNeeded()
  await save.click()
  await expect.poll(() => fixture.puts).toEqual([{ instructions: draft }])
  await page.reload()
  await expect(inline).toHaveValue(draft)
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})

test('Appearance and Personalization remain usable in English and Chinese', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockAgentSettings(page, mockOrigin)
  for (const language of ['en', 'zh-CN']) {
    await page.evaluate(value => localStorage.setItem('vista-language', value), language)
    await page.goto(`${mockOrigin}/settings/general`)
    const settings = page.locator('.settings-page:visible')
    const depth = settings.getByRole('spinbutton', { name: language === 'en' ? 'Delegation depth' : '委派层数', exact: true })
    const instructions = settings.getByRole('textbox', { name: language === 'en' ? 'Global instructions' : '全局提示词', exact: true })
    const save = settings.getByRole('form', { name: language === 'en' ? 'Global instructions' : '全局提示词', exact: true }).getByRole('button', { name: language === 'en' ? 'Save changes' : '保存更改', exact: true })
    await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText(language === 'en' ? 'Appearance' : '外观')
    await expect(settings.getByRole('group', { name: language === 'en' ? 'Color mode' : '颜色模式', exact: true })).toBeVisible()
    await expect(settings.locator('.settings-nav')).not.toContainText(/Agent|Subagent/i)
    await expect(depth).toHaveCount(0)
    await noHorizontalOverflow(page)
    const screenshot = testInfo.outputPath(`appearance-${language}.png`)
    await page.screenshot({ path: screenshot, animations: 'disabled' })
    await testInfo.attach(`appearance-${language}`, { path: screenshot, contentType: 'image/png' })
    await openSettingsSection(page, 'personalization')
    await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText(language === 'en' ? 'Personalization' : '个性化')
    await expect(depth).toHaveValue('1')
    await reachable(depth)
    await noHorizontalOverflow(page)
    await page.screenshot({ path: testInfo.outputPath(`agent-depth-${language}.png`), animations: 'disabled' })
    await instructions.fill(language === 'en' ? 'Follow the review checklist.' : '遵循审核清单。')
    await expect(save).toBeEnabled()
    await reachable(save)
    await noHorizontalOverflow(page)
    await settings.getByRole('button', { name: language === 'en' ? 'Full-screen editor' : '全屏编辑', exact: true }).click()
    const editor = page.locator('.instructions-editor-modal')
    await expect(editor).not.toContainText(fixture.saved.instructionsPath)
    await expect(editor.getByRole('textbox', { name: language === 'en' ? 'Global instructions' : '全局提示词', exact: true })).toBeFocused()
    await editor.getByRole('button', { name: language === 'en' ? 'Exit full screen' : '退出全屏', exact: true }).click()
    await expect(editor).toBeHidden()
    await expect(settings).toBeVisible()
  }
  expect(fixture.puts).toEqual([])
  expect(fixture.forbidden).toEqual([])
  expect(fixture.unexpected).toEqual([])
})
