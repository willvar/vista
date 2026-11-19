import { closeSettings } from './settings-navigation'
import { test, expect } from './fixtures'

test('catalog names and raw model IDs are searchable in Settings without a composer picker', async ({ page, vista }, testInfo) => {
  const catalog = await vista.api('/models')
  const model = catalog.models.find((item: { key: string }) => item.key === catalog.default)
  expect(model.id).toBe(vista.modelId)
  expect(model.modelName).toBeTruthy()
  expect(model.modelName).not.toBe(model.id)
  const label = `${model.name} · ${model.modelName}`
  const authHeaders: string[] = []
  page.on('request', request => {
    if (new URL(request.url()).pathname.startsWith('/api/') && request.headers().authorization) {
      authHeaders.push(request.headers().authorization!)
    }
  })
  const checkSearch = async (selector: ReturnType<typeof page.locator>, option: string) => {
    await selector.click()
    const input = selector.locator('input')
    for (const query of [model.id, model.modelName]) {
      await input.fill(query)
      const dropdown = page.locator('.ui-select-dropdown:visible')
      const group = dropdown.getByRole('group', { name: model.name, exact: true })
      await expect(group).toBeVisible()
      await expect(group.locator('.ui-select-group-label')).toHaveText(model.name)
      await expect(group.locator('.ui-select-item-option-content')).toHaveText([option])
      const lines = await group.locator('.ui-select-item-option-content').evaluate(element => {
        const range = document.createRange()
        range.selectNodeContents(element)
        return range.getClientRects().length
      })
      expect(lines, 'The short model name stays on one line in the expanded picker').toBe(1)
      const heading = await group.locator('.ui-select-group-label').boundingBox()
      const row = await group.getByRole('option').boundingBox()
      expect(heading!.y + heading!.height).toBeLessThanOrEqual(row!.y + 1)
      await expect(dropdown.locator('.ui-select-option-group')).toHaveCount(0)
    }
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').click()
  }
  await page.goto(vista.origin + '/settings/models')
  const defaultSelector = page.locator('.default-model-setting .ui-select')
  await expect(defaultSelector).toContainText(label)
  await checkSearch(defaultSelector, model.modelName)
  await expect(defaultSelector).toContainText(label)
  await page.reload()
  await expect(defaultSelector).toContainText(label)
  await closeSettings(page)
  const sender = page.locator('.sender-card')
  await expect(sender.locator('.composer-model')).toHaveCount(0)
  const viewport = page.viewportSize()!
  await expect(sender.getByRole('combobox', { name: 'Reasoning effort' })).toHaveCount(0)
  if (viewport.width <= 830) {
    await page.setViewportSize({ ...viewport, width: 320 })
    await expect(sender.getByRole('button', { name: 'Send', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320)
    await page.setViewportSize(viewport)
  }
  await sender.locator('textarea').fill('Hi')
  await sender.getByRole('button', { name: 'Send', exact: true }).click()
  const reply = page.locator('article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.reply-footer .message-model-name')).toContainText(label)
  const session = new URL(page.url()).pathname
  const messages = await vista.api(`${session}/messages`)
  expect(messages.find((item: { role: string }) => item.role === 'assistant').modelId).toBe(model.id)
  await page.reload()
  await expect(sender.locator('.composer-model')).toHaveCount(0)
  expect(new URL(page.url()).hash).toBe('')
  expect(await page.evaluate(() => sessionStorage.getItem('vista-token'))).toBeNull()
  expect(authHeaders).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('catalog-model-name.png'), animations: 'disabled' })
  if (viewport.width <= 830) {
    const longName = 'Model with a very long name '.repeat(8).trim()
    await page.route('**/api/models', route => route.fulfill({ json: {
      ...catalog, models: catalog.models.map((item: { key: string }) => item.key === model.key ? { ...item, modelName: longName } : item),
    } }))
    await page.goto(vista.origin + '/settings/models')
    await expect(defaultSelector.locator('.ui-select-selection-item')).toContainText(longName)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(viewport.width)
  }
})
