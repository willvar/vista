import { createServer } from 'node:http'
import { test, expect, type VistaFixture } from './fixtures'
import { closeSettings } from './settings-navigation'

async function inventory(vista: VistaFixture, rows: object[], name = 'Metadata test') {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: rows }))
  })
  await new Promise<void>(done => upstream.listen(0, '127.0.0.1', done))
  const address = upstream.address()
  if (!address || typeof address === 'string') throw new Error('Missing inventory address')
  try {
    await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
      provider: 'openai-chat', providerId: 'custom', name,
      baseUrl: `http://127.0.0.1:${address.port}/v1`, credentialMode: 'none',
    }) })
  } catch (error) {
    upstream.closeAllConnections()
    upstream.close()
    throw error
  }
  return () => new Promise<void>(done => { upstream.closeAllConnections(); upstream.close(() => done()) })
}

test('model choices keep connection headings before their models while searching and selecting', async ({ page, vista }, testInfo) => {
  const models = [
    { id: 'private-alpha', name: 'Alpha model', input_modalities: ['text'] },
    { id: 'private-shared', name: 'Shared model', input_modalities: ['text'] },
  ]
  const stopFirst = await inventory(vista, models, 'Connection A')
  const stopSecond = await inventory(vista, models, 'Connection B')
  try {
    await page.goto(vista.origin + '/')
    const picker = page.getByRole('combobox', { name: 'Model', exact: true })
    const popup = page.locator('.ui-select-dropdown:visible')
    await picker.click()
    await expect(popup.locator('.ui-select-group-label')).toHaveText(['Connection A', 'Connection B', 'Layout test'])
    for (const name of ['Connection A', 'Connection B']) {
      const group = popup.getByRole('group', { name, exact: true })
      await expect(group.getByRole('option')).toHaveText(['Alpha model', 'Shared model'])
      const heading = await group.locator('.ui-select-group-label').boundingBox()
      const first = await group.getByRole('option').first().boundingBox()
      expect(heading!.y + heading!.height).toBeLessThanOrEqual(first!.y + 1)
    }
    await page.screenshot({ path: testInfo.outputPath('model-groups.png'), animations: 'disabled' })
    await picker.fill('Connection B')
    await expect(popup.locator('.ui-select-group-label')).toHaveText(['Connection B'])
    await picker.press('Home')
    await picker.press('ArrowDown')
    await picker.press('Enter')
    await expect(page.locator('.composer-model .ui-select-selection-item')).toHaveText(
      page.viewportSize()!.width <= 830 ? 'Shared model' : 'Connection B · Shared model',
    )
    await picker.click()
    await picker.fill('no matching model')
    await expect(popup.locator('.ui-select-group-label')).toHaveCount(0)
    await expect(popup.getByRole('option')).toHaveCount(0)
    await expect(popup.locator('.ui-select-empty')).toBeVisible()
  } finally { await stopSecond(); await stopFirst() }
})

test('visibility synchronizes without redundant reads and removes a hidden selected model', async ({ page, vista }) => {
  const stop = await inventory(vista, [
    { id: 'private-bare' },
    { id: 'private-vision', name: 'Private Vision', input_modalities: ['text', 'image'], supported_reasoning_levels: ['low', 'high'] },
  ])
  try {
    await expect.poll(async () => (await vista.api('/models')).models.length).toBe(3)
    await page.goto(vista.origin + '/settings/models')
    const setting = page.getByRole('switch', { name: 'Show unrecognized models', exact: true })
    await expect(setting).not.toBeChecked()
    await expect(page.locator('.configured-provider')).toHaveCount(2)
    const reads: string[] = []
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'GET' && ['/api/models', '/api/model-config', '/api/sessions', '/api/status'].includes(path)) reads.push(path)
    })
    await setting.click()
    await expect(setting).toBeChecked()
    expect(reads).toEqual([])
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ showUnrecognizedModels: false }) })
    await expect(setting).not.toBeChecked()
    expect(reads).toEqual([])
    await closeSettings(page)
    const picker = page.getByRole('combobox', { name: 'Model', exact: true })
    const popup = page.locator('.ui-select-dropdown:visible')
    await picker.click()
    await expect(popup.getByText('Private Vision', { exact: true })).toBeVisible()
    await expect(popup.getByText('private-bare', { exact: true })).toHaveCount(0)
    await popup.getByText('Private Vision', { exact: true }).click()
    await expect(page.locator('.composer-reasoning')).toBeVisible()
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ showUnrecognizedModels: true }) })
    await picker.click()
    await expect(popup.getByText('private-bare', { exact: true })).toBeVisible()
    await popup.getByText('private-bare', { exact: true }).click()
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ showUnrecognizedModels: false }) })
    // Earlier choices do not bypass the visibility preference.
    await expect(page.locator('.composer-model')).not.toContainText('private-bare')
    await picker.click()
    await expect(popup.getByText('private-bare', { exact: true })).toHaveCount(0)
    await expect(popup.getByText('Private Vision', { exact: true })).toBeVisible()
    await page.keyboard.press('Escape')
  } finally { await stop() }
})

test('large model inventories render a bounded list and retain keyboard and search selection', async ({ page, vista }) => {
  const stop = await inventory(vista, Array.from({ length: 240 }, (_, index) => ({
    id: `private-${String(index).padStart(3, '0')}`, name: `Private model ${String(index).padStart(3, '0')}`,
    input_modalities: ['text'],
  })))
  try {
    await page.goto(vista.origin + '/')
    const picker = page.getByRole('combobox', { name: 'Model', exact: true })
    const popup = page.locator('.ui-select-dropdown:visible')
    await picker.click()
    await expect(popup).toHaveAttribute('data-virtualized', 'true')
    await expect.poll(() => popup.getByRole('option').count()).toBeGreaterThan(0)
    expect(await popup.getByRole('option').count()).toBeLessThan(40)
    await picker.press('Home')
    await picker.press('ArrowDown')
    await expect(popup.getByRole('option', { name: 'Private model 000', exact: true })).toHaveAttribute('data-highlighted', '')
    await picker.press('End')
    const last = popup.getByRole('option').filter({ hasText: 'Private model 239' })
    await expect(last).toBeVisible()
    await expect(last).toHaveAttribute('data-highlighted', '')
    await expect(popup.getByRole('group', { name: 'Metadata test', exact: true }).getByRole('option').filter({ hasText: 'Private model 239' })).toBeVisible()
    await picker.press('Enter')
    await expect(popup).toBeHidden()
    await expect(page.locator('.composer-model')).toContainText('Private model 239')
    await picker.click()
    await picker.fill('private-005')
    await expect(popup.getByRole('option')).toHaveCount(1)
    await expect(popup.locator('.ui-select-group-label')).toHaveText(['Metadata test'])
    await picker.press('ArrowDown')
    await picker.press('Enter')
    await expect(page.locator('.composer-model')).toContainText('Private model 005')
  } finally { await stop() }
})

test.describe('unrecognized-only connection', () => {
test.use({ configuredProvider: false })
test('an all-hidden inventory disables selection until enabled in settings', async ({ page, vista }) => {
  const stop = await inventory(vista, [{ id: 'only-unknown' }])
  try {
    await page.goto(vista.origin + '/')
    const picker = page.getByRole('combobox', { name: 'Model', exact: true })
    await expect(picker).toBeDisabled()
    await expect(page.getByRole('textbox', { name: 'Send a message', exact: true })).toBeDisabled()
    await page.goto(vista.origin + '/settings/models')
    const setting = page.getByRole('switch', { name: 'Show unrecognized models', exact: true })
    await setting.click()
    await expect(setting).toBeChecked()
    await closeSettings(page)
    await expect(picker).toBeEnabled()
    await expect(page.getByRole('textbox', { name: 'Send a message', exact: true })).toBeEnabled()
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ showUnrecognizedModels: false }) })
    await expect(picker).toBeDisabled()
    await expect(page.getByRole('textbox', { name: 'Send a message', exact: true })).toBeDisabled()
    await page.reload()
    await expect(picker).toBeDisabled()
  } finally { await stop() }
})
})
