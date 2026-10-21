import { closeSettings } from './settings-navigation'
import { test, expect } from './fixtures'

test('catalog names appear in both selectors while raw model IDs remain searchable and usable', async ({ page, vista }, testInfo) => {
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
      await expect(page.locator('.ant-select-dropdown:visible .ant-select-item-option-content')).toHaveText([option])
    }
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').click()
  }
  await page.goto(vista.origin + '/settings/models')
  const defaultSelector = page.locator('.default-model-setting .ant-select')
  await expect(defaultSelector).toContainText(label)
  await checkSearch(defaultSelector, model.modelName)
  await expect(defaultSelector).toContainText(label)
  await page.reload()
  await expect(defaultSelector).toContainText(label)
  await closeSettings(page)
  const sender = page.locator('.sender-card')
  await expect(sender.locator('.composer-model')).toContainText(label)
  // The picker option displays the catalog model name; the provider label is
  // kept in the group heading and in the selected control.
  await checkSearch(sender.locator('.composer-model'), model.modelName)
  await sender.locator('textarea').fill('Hi')
  await sender.getByRole('button', { name: 'Send', exact: true }).click()
  const reply = page.locator('article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.reply-footer .message-model-name')).toContainText(label)
  const session = new URL(page.url()).pathname
  const messages = await vista.api(`${session}/messages`)
  expect(messages.find((item: { role: string }) => item.role === 'assistant').modelId).toBe(model.id)
  expect(new URL(page.url()).hash).toBe('')
  expect(await page.evaluate(() => sessionStorage.getItem('vista-token'))).toBeNull()
  expect(authHeaders).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('catalog-model-name.png'), animations: 'disabled' })
})
