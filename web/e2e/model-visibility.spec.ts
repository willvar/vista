import { createServer } from 'node:http'
import { test, expect, type VistaFixture } from './fixtures'
import { closeSettings } from './settings-navigation'

async function inventory(vista: VistaFixture, rows: object[]) {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: rows }))
  })
  await new Promise<void>(done => upstream.listen(0, '127.0.0.1', done))
  const address = upstream.address()
  if (!address || typeof address === 'string') throw new Error('Missing inventory address')
  try {
    await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
      provider: 'openai-chat', providerId: 'custom', name: 'Metadata test',
      baseUrl: `http://127.0.0.1:${address.port}/v1`, credentialMode: 'none',
    }) })
  } catch (error) {
    upstream.closeAllConnections()
    upstream.close()
    throw error
  }
  return () => new Promise<void>(done => { upstream.closeAllConnections(); upstream.close(() => done()) })
}

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
    const popup = page.locator('.ant-select-dropdown:visible')
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
