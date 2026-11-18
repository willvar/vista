import { test, expect } from './fixtures'

test('connection usage reports real calls without ranking or loading when closed', { tag: '@responsive' }, async ({ page, vista }) => {
  await vista.api('/agent-settings', { method: 'PUT', body: JSON.stringify({ automaticTitles: false }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const input = page.locator('.sender-card textarea')
  await input.fill('Hi')
  await input.press('Enter')
  await expect(page.locator('article.assistant').last()).toContainText('Mock reply: Hi')
  await expect(page.locator('.model-ttft').last()).not.toContainText('—')
  const requests: string[] = []
  page.on('request', request => { if (request.url().includes('/api/connection-statistics?')) requests.push(request.url()) })
  await page.goto(`${vista.origin}/settings`)
  await page.getByRole('link', { name: 'Service connections', exact: true }).click()
  const usage = page.getByRole('button', { name: 'Usage statistics', exact: true }).first()
  await expect(usage).toBeVisible()
  const enabled = page.getByRole('switch', { name: 'Enable Layout test', exact: true })
  await enabled.click()
  await expect(enabled).not.toBeChecked()
  expect(requests).toEqual([])
  await usage.click()
  const dialog = page.getByRole('dialog', { name: /usage statistics/i })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('Succeeded · 1')).toBeVisible()
  await expect(dialog.locator('tbody')).toContainText('Direct')
  await expect(dialog.getByText('100%', { exact: true })).toBeVisible()
  expect(requests).toHaveLength(1)
})

test('connection usage keeps its header and filters usable with long details', { tag: '@responsive' }, async ({ page, vista }) => {
  const longModel = 'gateway/' + 'long-model-name-'.repeat(14)
  const requests: string[] = []
  await page.route('**/api/connection-statistics?*', async route => {
    const days = new URL(route.request().url()).searchParams.get('days')!
    requests.push(days)
    await route.fulfill({ json: { groups: Array.from({ length: 18 }, (_, index) => ({
      model: index ? `model-${index}` : longModel, proxyId: '', route: 'direct',
      requestedTier: index ? 'priority' : '', actualTier: '', outcome: 'succeeded', requests: 1,
      firstTokenMs: 150, firstTokenSamples: 1, outputTps: 60, outputSamples: 1,
      connectMs: null, connectSamples: 0, tlsMs: null, tlsSamples: 0,
    })), truncated: false } })
  })
  await page.goto(`${vista.origin}/settings/models`)
  await page.getByRole('button', { name: 'Usage statistics', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: /usage statistics/i })
  const header = dialog.getByRole('heading', { name: /usage statistics/i })
  const detail = dialog.getByRole('table')
  await expect(detail.locator('tbody tr')).toHaveCount(18)
  const viewport = page.viewportSize()!
  await expect.poll(async () => {
    const panel = (await dialog.boundingBox())!
    return panel.x >= 0 && panel.y >= 0 && panel.x + panel.width <= viewport.width && panel.y + panel.height <= viewport.height + 1
  }).toBe(true)

  const body = dialog.locator('.ui-modal-body')
  const scrollableTable = dialog.locator('.connection-usage-table')
  expect(await body.evaluate(el => el.scrollWidth)).toBeLessThanOrEqual(await body.evaluate(el => el.clientWidth + 1))
  expect(await scrollableTable.evaluate(el => el.scrollHeight)).toBeGreaterThan(await scrollableTable.evaluate(el => el.clientHeight))
  if (viewport.width < 650) {
    expect(await scrollableTable.evaluate(el => el.scrollWidth)).toBeGreaterThan(await scrollableTable.evaluate(el => el.clientWidth))
  }

  const period = dialog.getByRole('combobox', { name: 'Period', exact: true })
  const model = dialog.getByRole('combobox', { name: 'Model', exact: true })
  const dropdown = page.locator('.ui-select-dropdown:visible')
  await period.click()
  await dropdown.getByRole('option', { name: /7 days/i }).click()
  await expect.poll(() => requests).toEqual(['30', '7'])
  await model.click()
  await dropdown.getByRole('option', { name: longModel, exact: true }).click()
  await expect(detail.locator('tbody tr')).toHaveCount(1)
  await expect(detail.locator('tbody')).toContainText(longModel)
  await model.click()
  await dropdown.getByRole('option', { name: 'All', exact: true }).click()
  await expect(detail.locator('tbody tr')).toHaveCount(18)

  const top = (await header.boundingBox())!.y
  await body.evaluate(el => { el.scrollTop = el.scrollHeight })
  expect((await header.boundingBox())!.y).toBeCloseTo(top, 0)
  const close = dialog.getByRole('button', { name: 'Close', exact: true })
  await expect(close).toBeInViewport()
  await close.click()
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('heading', { name: 'Service connections', exact: true })).toBeInViewport()
  expect(requests).toEqual(['30', '7'])
})
