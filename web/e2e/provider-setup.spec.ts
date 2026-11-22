import { closeSettings } from './settings-navigation'
import { createServer } from 'node:http'
import type { Page } from '@playwright/test'
import { test, expect, type VistaFixture } from './fixtures'
import { expectHeaderRow } from './layout-ready'

async function azureConnection(page: Page, vista: VistaFixture) {
  const input = { name: 'Draft Azure', provider: 'catalog', providerId: 'azure', credentialMode: 'apiKey', apiKey: 'azure-fixture', resourceName: 'fixture-resource', deploymentId: 'fixture-deployment', ids: ['gpt-4.1'] }
  await vista.api('/provider-config', { method: 'POST', body: JSON.stringify(input) })
  const saved = (await vista.api('/model-config')).models.find((model: { name: string }) => model.name === input.name)
  const setup = (await vista.api('/provider-config')).find((provider: { id: string }) => provider.id === 'azure')
  const dialog = page.locator('.provider-connection-modal')
  const form = dialog.locator('.model-form')
  const picker = form.locator('.catalog-model-field .ui-select')
  const discard = page.getByRole('dialog', { name: 'Discard unsaved changes', exact: true })
  const label = (id: string) => setup.models.find((model: { id: string }) => model.id === id).name || id
  return {
    input, saved, dialog, form, picker, discard, label,
    open: () => page.locator('.configured-provider').filter({ hasText: input.name }).getByRole('button', { name: 'Edit', exact: true }).click(),
    select: async (id: string) => {
      await picker.click()
      await picker.locator('input').fill(id)
      await page.getByRole('option', { name: label(id), exact: true }).click()
      await expect(picker.locator('.ui-select-selection-item')).toHaveText(label(id))
    },
  }
}

test('model-only drafts require confirmation and reset after reverting, discarding or saving', async ({ page, vista }) => {
  const connection = await azureConnection(page, vista)
  const { dialog, form, picker, discard, label } = connection
  await page.goto(vista.origin + '/settings/models')
  const writes: string[] = []
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/provider-config' && request.method() === 'PUT') writes.push(request.method()) })
  await connection.open()
  await connection.select('gpt-4.1-mini')
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
  await connection.select('gpt-4.1')
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect(discard).toHaveCount(0)
  await connection.open()
  await connection.select('gpt-4.1-mini')
  await form.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Discard unsaved changes', exact: true }).click()
  await expect(form).toHaveCount(0)
  await connection.open()
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1'))
  expect(writes).toEqual([])
  await connection.select('gpt-4.1-mini')
  await form.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect.poll(async () => (await vista.api('/model-config')).models.find((model: { name: string }) => model.name === connection.input.name)?.id).toBe('gpt-4.1-mini')
  expect(writes).toEqual(['PUT'])
  await connection.open()
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect(discard).toHaveCount(0)
})

test('saved model selection is ready to reopen while shared-state reads are delayed', async ({ page, vista }) => {
  const connection = await azureConnection(page, vista)
  const { form, picker, label } = connection
  await page.goto(vista.origin + '/settings/models')
  await connection.open()
  await connection.select('gpt-4.1-mini')
  let reads = 0
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/model-config', async route => {
    reads++
    const response = await route.fetch()
    await gate
    await route.fulfill({ response })
  })
  try {
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    await connection.open()
    await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
    release()
    await page.unrouteAll({ behavior: 'wait' })
    await expect(form.getByRole('button', { name: 'Reload current settings', exact: true })).toHaveCount(0)
    expect(reads).toBeLessThanOrEqual(1)
  } finally { release() }
})

test('a delayed save response preserves newer shared settings and the active draft', async ({ page, vista }) => {
  const connection = await azureConnection(page, vista)
  const { form, picker, label } = connection
  await page.goto(vista.origin + '/settings/models')
  await connection.open()
  await connection.select('gpt-4.1-mini')
  let release!: () => void
  let received!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const saved = new Promise<void>(resolve => { received = resolve })
  await page.route('**/api/provider-config', async route => {
    if (route.request().method() !== 'PUT') { await route.continue(); return }
    const response = await route.fetch()
    received()
    await gate
    await route.fulfill({ response })
  })
  try {
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await saved
    const settings = await vista.api('/model-config')
    const current = settings.models.find((model: { name: string }) => model.name === connection.input.name)
    const name = 'Draft Azure saved elsewhere'
    await vista.api('/provider-config', { method: 'PUT', body: JSON.stringify({ ...connection.input, key: current.key, apiKey: '', name, revision: settings.settingsRevision }) })
    await expect(page.locator('.configured-provider').filter({ hasText: name })).toBeVisible()
    await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
    release()
    await expect(form).toHaveCount(0)
    await connection.open()
    await expect(form.getByLabel(/^Provider name/)).toHaveValue(name)
    await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1'))
  } finally { release() }
})

test('model-only drafts survive navigation cancellation and reload from the accepted connection', async ({ page, vista }) => {
  const connection = await azureConnection(page, vista)
  const { dialog, form, picker, discard, label } = connection
  await page.goto(vista.origin + '/settings/general')
  await page.locator('.settings-nav').getByRole('link', { name: 'Service connections', exact: true }).click()
  await connection.open()
  await connection.select('gpt-4.1-mini')
  const cancelledBack = page.goBack()
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Cancel', exact: true }).click()
  await cancelledBack
  await expect(page).toHaveURL(url => url.pathname === '/settings/models')
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
  const discardedBack = page.goBack()
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Discard unsaved changes', exact: true }).click()
  await discardedBack
  await expect(page).toHaveURL(url => url.pathname === '/settings/general')
  await expect(form).toHaveCount(0)
  await page.locator('.settings-nav').getByRole('link', { name: 'Service connections', exact: true }).click()
  await connection.open()
  await connection.select('gpt-4.1-mini')
  const settings = await vista.api('/model-config')
  await vista.api('/provider-config', { method: 'PUT', body: JSON.stringify({ ...connection.input, key: connection.saved.key, apiKey: '', name: 'Draft Azure updated', revision: settings.settingsRevision }) })
  const reload = form.getByRole('button', { name: 'Reload current settings', exact: true })
  await expect(reload).toBeVisible()
  await reload.click()
  await expect(discard).toBeVisible()
  await discard.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1-mini'))
  await reload.click()
  await discard.getByRole('button', { name: 'Discard unsaved changes', exact: true }).click()
  await expect(form.getByLabel(/^Provider name/)).toHaveValue('Draft Azure updated')
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(label('gpt-4.1'))
  await expect(reload).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect(discard).toHaveCount(0)
})

test('custom cache format controls its defaults without a separate cache-key option @responsive', async ({ page, vista }) => {
  await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
    name: 'Cache gateway', provider: 'openai-responses', providerId: 'custom',
    baseUrl: 'http://127.0.0.1:1/v1', credentialMode: 'none', ids: ['opaque-cache-model'],
    promptCaching: { format: 'openai', key: false, retention: '24h' }, responsesRoute: 'codex-compatible',
  }) })
  await page.goto(vista.origin + '/settings/models')
  const connection = page.locator('.configured-provider').filter({ hasText: 'Cache gateway' })
  await connection.getByRole('button', { name: 'Edit', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  const format = form.getByRole('combobox', { name: 'Prompt caching compatibility', exact: true })
  const key = form.locator('[data-field-path=cacheKey]')
  await expect(format).toHaveValue('OpenAI')
  await expect(key).toHaveCount(0)
  await expect(form.locator('[data-field-path=responsesRoute]')).toHaveCount(0)
  const misplacedMarkers = await form.locator('.schema-control > label:has(.schema-required)').evaluateAll(labels => labels.flatMap(label => {
    const marker = label.querySelector('.schema-required')!.getBoundingClientRect()
    const text = document.createRange()
    text.selectNodeContents(label.firstChild!)
    const lastLine = Array.from(text.getClientRects()).at(-1)!
    return Math.abs(marker.top - lastLine.top) > 2 ? [label.textContent] : []
  }))
  expect(misplacedMarkers, 'Required markers stay on the last line of their label').toEqual([])
  await form.getByLabel(/^Provider name/).fill('Cache gateway saved')
  await form.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect(connection).toContainText('Cache gateway saved')
  const options = async () => (await vista.api('/model-config')).models.find((model: { name: string }) => model.name === 'Cache gateway saved').promptCaching
  // Ordinary edits preserve existing advanced API overrides.
  await expect.poll(options).toEqual({ format: 'openai', key: false, retention: '24h' })
  await connection.getByRole('button', { name: 'Edit', exact: true }).click()
  await format.scrollIntoViewIfNeeded()
  await format.click()
  await expect(page.getByRole('option')).toHaveText(['Omit cache hints', 'OpenAI'])
  await page.getByRole('option', { name: 'Omit cache hints', exact: true }).click()
  await expect(key).toHaveCount(0)
  await format.click()
  await page.getByRole('option', { name: 'OpenAI', exact: true }).click()
  await expect(key).toHaveCount(0)
  await form.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(form).toHaveCount(0)
  // An explicit strategy selection clears the old independent override and
  // lifetime, so the chosen OpenAI strategy uses its default cache key.
  await expect.poll(options).toEqual({ format: 'openai' })
  await connection.getByRole('button', { name: 'Edit', exact: true }).click()
  await form.getByLabel(/^Base URL/).fill('http://127.0.0.1:2/v1')
  await expect(format).toHaveValue('Omit cache hints')
  await expect(key).toHaveCount(0)
})

for (const legacy of [
  { name: 'Default cache policy', promptCaching: { format: 'auto' }, format: 'Omit cache hints', route: '' },
  { name: 'Legacy routing key', promptCaching: { format: 'auto' }, format: 'OpenAI', route: 'codex-compatible' },
  { name: 'Explicit cache key', promptCaching: { format: 'auto', key: true }, format: 'OpenAI', route: '' },
  { name: 'Disabled legacy key', promptCaching: { format: 'auto', key: false }, format: 'Omit cache hints', route: 'codex-compatible' },
  { name: 'Existing Gemini resource', promptCaching: { format: 'auto', cachedContent: { name: 'cachedContents/existing', sha256: '0'.repeat(64), messages: 0 } }, format: 'Gemini', route: '', provider: 'gemini' },
]) {
  test(`legacy cache settings retain their behavior when editing: ${legacy.name}`, async ({ page, vista }) => {
    await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
      name: legacy.name, provider: legacy.provider || 'openai-responses', providerId: 'custom',
      baseUrl: 'http://127.0.0.1:1/v1', credentialMode: 'none', ids: ['opaque-cache-model'],
      promptCaching: legacy.promptCaching, responsesRoute: legacy.route,
    }) })
    await page.goto(vista.origin + '/settings/models')
    const connection = page.locator('.configured-provider').filter({ hasText: legacy.name })
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    await expect(form.locator('[data-field-path=cacheFormat] .ui-select')).toContainText(legacy.format)
    await expect(form.locator('[data-field-path=cacheKey]')).toHaveCount(0)
    await form.getByLabel(/^Provider name/).fill(legacy.name + ' renamed')
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    const saved = (await vista.api('/model-config')).models.find((model: { name: string }) => model.name === legacy.name + ' renamed')
    expect(saved.promptCaching).toEqual(legacy.promptCaching)
    expect(saved.responsesRoute || '').toBe(legacy.route)
  })
}

test.describe('custom provider dialogs', () => {
  test.use({ providerOutput: 'perplexity' })

  test('connect and edit use a second dialog and return to the provider list', async ({ page, vista }, testInfo) => {
    await vista.api('/provider-config', { method: 'DELETE', body: JSON.stringify({ key: 'test' }) })
    await page.goto(vista.origin + '/settings/models')
    const settings = page.locator('.settings-page')
    const dialog = page.locator('.provider-connection-modal')
    const backdrop = page.locator('.ui-modal-wrap').filter({ has: dialog })
    const form = dialog.locator('.model-form')
    const connect = settings.getByRole('button', { name: 'Connect provider', exact: true })
    await expect(connect).toBeVisible()
    await expect(form).toHaveCount(0)
    await connect.click()
    await expect(page.getByRole('dialog', { name: 'Connect provider', exact: true })).toBeVisible()
    await expect(settings.locator('.model-form')).toHaveCount(0)
    await form.getByRole('combobox', { name: 'Provider', exact: true }).click()
    await form.getByRole('combobox', { name: 'Provider', exact: true }).fill('Custom provider')
    await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
    const name = form.getByLabel(/^Provider name/)
    await name.fill('Discarded draft')
    await page.keyboard.press('Escape')
    const discard = page.getByRole('dialog', { name: 'Discard unsaved changes', exact: true })
    await expect(discard).toBeVisible()
    await discard.getByRole('button', { name: 'Discard unsaved changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    await expect(settings).toBeVisible()
    await connect.click()
    await expect(name).toHaveCount(0)
    await backdrop.click({ position: { x: 4, y: 4 } })
    await expect(form).toHaveCount(0)
    await expect(settings).toBeVisible()

    await connect.click()
    await form.getByRole('combobox', { name: 'Provider', exact: true }).click()
    await form.getByRole('combobox', { name: 'Provider', exact: true }).fill('Custom provider')
    await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
    await name.fill('Modal provider')
    await form.getByLabel(/^Base URL/).fill(vista.modelEndpoint)
    await form.locator('input[type=password]').fill('test-key')
    const redundantReads: string[] = []
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'GET' && ['/api/models', '/api/sessions', '/api/status', '/api/provider-config'].includes(path)) redundantReads.push(path)
    })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const connection = settings.locator('.configured-provider').filter({ hasText: 'Modal provider' })
    await expect(connection).toBeVisible()
    await expect(form).toHaveCount(0)
    expect(redundantReads).toEqual([])
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(page.getByRole('dialog', { name: 'Edit provider connection', exact: true })).toBeVisible()
    await expect(form.locator('.provider-kind-value')).toHaveText('Custom provider')
    await name.fill('Unsaved rename')
    await page.screenshot({ path: testInfo.outputPath('provider-edit-dialog.png'), animations: 'disabled' })
    await backdrop.click({ position: { x: 4, y: 4 } })
    await expect(discard).toBeVisible()
    await discard.getByRole('button', { name: 'Discard unsaved changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    await expect(connection).toBeVisible()
    expect((await vista.api('/model-config')).models[0].name).toBe('Modal provider')
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(name).toHaveValue('Modal provider')
    await dialog.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(form).toHaveCount(0)
    await expect(settings).toBeVisible()

    await connection.getByRole('button', { name: 'Remove provider connection', exact: true }).click()
    await page.locator('.provider-disconnect-popover').getByRole('button', { name: 'Remove provider connection', exact: true }).click()
    await expect(settings.locator('.configured-provider')).toHaveCount(0)
    await expect(connect).toBeVisible()
    await expect(form).toHaveCount(0)
  })
})

test('a custom connection saves before discovery and accepts explicit model IDs @responsive', async ({ page, vista }, testInfo) => {
  let holdDiscovery = true
  let releaseDiscovery: (() => void) | undefined
  const upstream = createServer((request, response) => {
    if (request.url !== '/v1/models') { response.writeHead(404).end(); return }
    if (holdDiscovery && !releaseDiscovery) {
      releaseDiscovery = () => { releaseDiscovery = undefined; response.writeHead(503).end() }
      return
    }
    response.writeHead(503).end()
  })
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve))
  try {
    const address = upstream.address()
    if (!address || typeof address === 'string') throw new Error('Missing custom test address')
    await page.goto(vista.origin + '/settings/models')
    const settings = page.locator('.settings-page')
    await settings.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const picker = form.locator('.provider-kind-select .ui-select')
    await picker.click()
    await picker.locator('input').fill('Custom provider')
    await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
    await form.getByLabel(/^Provider name/).fill('Discovery pending')
    await form.getByLabel(/^Base URL/).fill(`http://127.0.0.1:${address.port}/v1`)
    await form.getByLabel(/^Provider API key/).fill('fixture-key')
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const connection = settings.locator('.configured-provider').filter({ hasText: 'Discovery pending' })
    await expect(connection).toBeVisible()
    await expect(form).toHaveCount(0)
    await expect.poll(() => !!releaseDiscovery).toBe(true)
    const stored = (await vista.api('/model-config')).models.find((model: { name: string }) => model.name === 'Discovery pending')
    expect(stored).toMatchObject({ id: '', hasApiKey: true })
    expect(stored.connectionId).toBeTruthy()
    expect((await vista.api('/models')).models.some((model: { connectionId: string }) => model.connectionId === stored.connectionId)).toBe(false)

    holdDiscovery = false
    releaseDiscovery?.()
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await form.getByRole('button', { name: 'Enter model IDs manually', exact: true }).click()
    const modelIDs = form.locator('.model-list-field input')
    await modelIDs.fill('manually-entered-model')
    await modelIDs.press('Enter')
    await modelIDs.press('Escape')
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    const selected = (await vista.api('/model-config')).models.find((model: { connectionId: string }) => model.connectionId === stored.connectionId)
    expect(selected).toMatchObject({ id: 'manually-entered-model', connectionId: stored.connectionId, hasApiKey: true })
  } finally {
    releaseDiscovery?.()
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()))
  }
})

test('connect Z.ai with only a key, then choose models in the conversation', async ({ page, vista }, testInfo) => {
  const setups = await vista.api('/provider-config')
  const ids = setups.map((setup: { id: string }) => setup.id)
  expect(ids).toContain('custom')
  for (const removed of ['cohere', 'alibaba', 'alibaba-cn', 'zhipuai', 'siliconflow', 'alibaba-coding-plan-cn']) expect(ids).not.toContain(removed)

  await page.goto(vista.origin + '/settings/models')
  const modal = page.locator('.settings-page')
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  const picker = form.locator('.provider-kind-select .ui-select')
  await picker.click()
  await picker.locator('input').fill('百炼')
  const choices = page.locator('.ui-select-dropdown:visible .ui-select-item-option')
  await expect(choices).toHaveCount(1)
  await expect(choices.first()).toContainText('Alibaba')
  await picker.locator('input').fill('z.ai')
  await expect(choices).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('search-providers.png') })
  await choices.first().click()
  await expect(form.getByText('Base URL', { exact: true })).toHaveCount(0)
  await expect(form.locator('.catalog-model-field, .model-list-field')).toHaveCount(0)
  await form.getByLabel(/^Provider API key/).fill('private-test-key')
  await expect(form.getByRole('button', { name: 'Connect provider', exact: true })).toBeEnabled()
  const submitted = page.waitForRequest(request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/provider-config')
  await page.screenshot({ path: testInfo.outputPath('key-only-connection.png') })
  await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
  expect((await submitted).postDataJSON()).toMatchObject({ providerId: 'zai', ids: [], name: '' })
  const connection = modal.locator('.configured-provider').filter({ hasText: 'Z.ai' })
  await expect(connection).toBeVisible()
  const configured = (await vista.api('/model-config')).models.find((item: { providerId?: string }) => item.providerId === 'zai')
  expect(configured).toMatchObject({ provider: 'catalog', providerId: 'zai', kind: 'zai', hasApiKey: true, editable: true })
  expect(configured).not.toHaveProperty('apiKey')
  const available = (await vista.api('/models')).models.filter((item: { connectionId: string }) => item.connectionId === configured.connectionId)
  expect(available.length).toBeGreaterThan(1)
  expect(available.every((item: { provider: string }) => item.provider !== 'catalog')).toBe(true)
  const model = available.find((item: { id: string }) => item.id !== configured.id)
  await connection.getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(picker).toHaveCount(0)
  await expect(form.locator('.provider-kind-value')).toHaveText('Z.ai')
  await expect(form.locator('.catalog-model-field, .model-list-field')).toHaveCount(0)
  await expect(form.getByPlaceholder('Leave blank to reuse the saved key')).toHaveValue('')
  await form.getByLabel(/^Provider name/).fill('Renamed Z.ai')
  await form.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(modal.locator('.configured-provider').filter({ hasText: 'Renamed Z.ai' })).toBeVisible()
  await page.reload()
  const reloaded = (await vista.api('/model-config')).models.find((item: { key: string }) => item.key === configured.key)
  expect(reloaded).toMatchObject({ connectionId: configured.connectionId, name: 'Renamed Z.ai', hasApiKey: true })
  await expect(page.locator('.settings-page')).toBeVisible()
  const refreshed = (await vista.api('/models')).models.filter((item: { connectionId: string }) => item.connectionId === configured.connectionId)
  expect(refreshed.map((item: { key: string }) => item.key)).toEqual(available.map((item: { key: string }) => item.key))
  const modelPicker = page.locator('.default-model-setting .ui-select')
  await modelPicker.click()
  await modelPicker.locator('input').fill(model.id)
  await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: model.modelName || model.id }).first().click()
  await expect(modelPicker.locator('.ui-select-selection-item')).toHaveText(`Renamed Z.ai · ${model.modelName || model.id}`)
  await closeSettings(page)
  await expect(page.locator('.composer-model')).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
})

test('Azure deployment and local services use their own connection fields', async ({ page, vista }, testInfo) => {
  const upstream = createServer((request, response) => {
    if (request.url === '/health') { response.writeHead(200).end(); return }
    if (request.url === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'local-model', owned_by: 'vllm', max_model_len: 32768 }] }))
      return
    }
    response.writeHead(404).end()
  })
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve))
  try {
    const address = upstream.address()
    if (!address || typeof address === 'string') throw new Error('Missing local test address')
    await page.goto(vista.origin + '/settings/models')
    const modal = page.locator('.settings-page')
    const form = page.locator('.provider-connection-modal .model-form')
    const chooseProvider = async (name: string) => {
      await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
      const picker = form.locator('.provider-kind-select .ui-select')
      await picker.click()
      await picker.locator('input').fill(name)
      await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: name }).first().click()
    }
    await chooseProvider('Azure OpenAI')
    await form.getByLabel(/^Azure resource name/).fill('test-resource')
    await form.getByLabel(/^Provider API key/).fill('azure-secret')
    const setups = await vista.api('/provider-config')
    const azure = setups.find((setup: { id: string }) => setup.id === 'azure')
    const model = azure.models.find((item: { id: string }) => item.id === 'gpt-4.1')
    await form.locator('.catalog-model-field input').fill(model.id)
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: model.name || model.id }).first().click()
    await form.getByLabel(/^Deployment name/).fill('production-gpt')
    await page.screenshot({ path: testInfo.outputPath('azure-connection.png') })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(modal.locator('.configured-provider').filter({ hasText: 'Azure OpenAI' })).toBeVisible()
    expect((await vista.api('/model-config')).models.find((item: { providerId: string }) => item.providerId === 'azure')).toMatchObject({ resourceName: 'test-resource', deploymentId: 'production-gpt', id: model.id })
    await chooseProvider('vLLM')
    await expect(form.getByLabel('Provider API key', { exact: true })).toHaveCount(0)
    await form.getByLabel(/^Base URL/).fill(`http://127.0.0.1:${address.port}/v1`)
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(modal.locator('.configured-provider').filter({ hasText: 'vLLM' })).toBeVisible()
    const local = (await vista.api('/model-config')).models.find((item: { providerId: string }) => item.providerId === 'vllm')
    expect(local).toMatchObject({ id: '', hasApiKey: false, credentialMode: 'default', editable: true })
    await expect.poll(async () => (await vista.api('/models')).models.some((item: { connectionId: string; id: string }) => item.connectionId === local.connectionId && item.id === 'local-model')).toBe(true)
    await page.reload()
    await expect(modal.locator('.configured-provider').filter({ hasText: 'vLLM' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  } finally {
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()))
  }
})

test('custom API format and authentication stay independent through edit, reload and a Messages reply', async ({ page, vista }, testInfo) => {
  const received: { path: string; auth?: string; apiKey?: string; googleKey?: string }[] = []
  const upstream = createServer(async (request, response) => {
    const path = new URL(request.url || '/', 'http://localhost').pathname
    received.push({ path, auth: request.headers.authorization, apiKey: String(request.headers['x-api-key'] || ''), googleKey: String(request.headers['x-goog-api-key'] || '') })
    if (request.headers.authorization !== 'Bearer custom-test-key' || request.headers['x-api-key'] || request.headers['x-goog-api-key']) {
      response.writeHead(401).end(); return
    }
    if (request.method === 'GET' && path === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'gateway-test-model', input_modalities: ['text'], supported_endpoints: ['/v1/chat/completions'] }] }))
      return
    }
    if (request.method === 'POST' && path === '/v1/messages') {
      for await (const _ of request) { /* Consume the request before replying. */ }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' }).end(
        'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Messages with Bearer."}}\n\n'
        + 'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}\n\n'
        + 'event: message_stop\ndata: {"type":"message_stop"}\n\n',
      )
      return
    }
    response.writeHead(404).end()
  })
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve))
  try {
    const address = upstream.address()
    if (!address || typeof address === 'string') throw new Error('Missing custom test address')
    await page.goto(vista.origin + '/settings/models')
    const modal = page.locator('.settings-page')
    await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const picker = form.locator('.provider-kind-select .ui-select')
    await picker.click()
    await picker.locator('input').fill('Custom provider')
    await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
    const format = form.locator('[data-field-path=apiFormat] .ui-select')
    const auth = form.locator('[data-field-path=credentialMode] .ui-select')
    const key = form.getByLabel(/^Provider API key/)
    await expect(format).toContainText('OpenAI Chat Completions')
    await expect(auth).toContainText('Bearer')
    await form.getByLabel(/^Provider name/).fill('Messages gateway')
    await form.getByLabel(/^Base URL/).fill(`http://127.0.0.1:${address.port}`)
    await key.fill('custom-test-key')
    await format.click()
    const choices = page.locator('.ui-select-dropdown:visible .ui-select-item-option')
    await expect(choices).toHaveText(['OpenAI Chat Completions', 'OpenAI Responses', 'Anthropic Messages', 'Google Gemini generateContent', 'Cohere Chat v2'])
    await choices.filter({ hasText: /^Google Gemini generateContent$/ }).click()
    await expect(auth).toContainText('Bearer')
    await expect(key).toHaveValue('custom-test-key')
    await format.click()
    await choices.filter({ hasText: /^Anthropic Messages$/ }).click()
    await auth.click()
    await expect(choices).toHaveText(['Bearer', 'x-api-key', 'x-goog-api-key', 'No authentication'])
    await choices.filter({ hasText: /^x-goog-api-key$/ }).click()
    await expect(format).toContainText('Anthropic Messages')
    await expect(key).toHaveValue('custom-test-key')
    await auth.click()
    await choices.filter({ hasText: /^Bearer$/ }).click()
    await format.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('custom-api-auth-light.png'), animations: 'disabled' })
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.screenshot({ path: testInfo.outputPath('custom-api-auth-dark.png'), animations: 'disabled' })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(form).toHaveCount(0)
    const saved = (await vista.api('/model-config')).models.find((item: { name: string }) => item.name === 'Messages gateway')
    expect(saved).toMatchObject({ provider: 'anthropic', providerId: 'custom', credentialMode: 'bearer', kind: 'custom', editable: true, hasApiKey: true })
    await page.reload()
    const connection = modal.locator('.configured-provider').filter({ hasText: 'Messages gateway' })
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(format).toContainText('Anthropic Messages')
    await expect(auth).toContainText('Bearer')
    await expect(key).toHaveValue('')
    await expect(key).toHaveAttribute('placeholder', 'Leave blank to reuse the saved key')
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    const modelPicker = page.locator('.default-model-setting .ui-select')
    await modelPicker.click()
    await modelPicker.locator('input').fill('gateway-test-model')
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: 'gateway-test-model' }).first().click()
    await closeSettings(page)
    await page.locator('.sender-card textarea').fill('Hello')
    await page.getByRole('button', { name: 'Send', exact: true }).click()
    await expect(page.locator('article.assistant .markdown')).toHaveText('Messages with Bearer.')
    expect(received.some(item => item.path === '/v1/messages')).toBe(true)
    expect(received.every(item => item.auth === 'Bearer custom-test-key' && !item.apiKey && !item.googleKey)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  } finally {
    upstream.closeAllConnections()
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()))
  }
})

test('custom connection supports anonymous access and advanced headers through save, edit and a real reply @responsive', async ({ page, vista }, testInfo) => {
  const received: { path: string; auth?: string; access?: string }[] = []
  const upstream = createServer(async (request, response) => {
    const path = new URL(request.url || '/', 'http://localhost').pathname
    received.push({ path, auth: request.headers.authorization, access: String(request.headers['x-access'] || '') })
    if (request.headers.authorization || request.headers['x-access'] !== 'private-header-value') {
      response.writeHead(401).end(); return
    }
    if (request.method === 'GET' && path === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'anonymous-test-model', input_modalities: ['text'], supported_endpoints: ['/v1/chat/completions'] }] }))
      return
    }
    if (request.method === 'POST' && path === '/v1/chat/completions') {
      for await (const _ of request) { /* Consume the request before replying. */ }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' }).end('data: {"choices":[{"delta":{"content":"Anonymous provider reply."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
      return
    }
    response.writeHead(404).end()
  })
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve))
  try {
    const address = upstream.address()
    if (!address || typeof address === 'string') throw new Error('Missing custom test address')
    await page.goto(vista.origin + '/settings/models')
    const modal = page.locator('.settings-page')
    await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const picker = form.locator('.provider-kind-select .ui-select')
    await picker.click()
    await picker.locator('input').fill('Custom provider')
    await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
    await form.getByLabel(/^Base URL/).fill(`http://127.0.0.1:${address.port}`)
    const auth = form.locator('[data-field-path=credentialMode] .ui-select')
    await auth.click()
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: /^No authentication$/ }).click()
    await expect(form.getByLabel('Provider API key', { exact: true })).toHaveCount(0)
    await expect(form.locator('.model-list-field')).toHaveCount(0)
    await expect(form.locator('[data-field-path=apiFormat] .ui-select')).toContainText('OpenAI Chat Completions')
    const headers = form.getByRole('group', { name: 'Request headers', exact: true })
    await headers.getByRole('button', { name: 'Add item', exact: true }).click()
    await expect(headers.locator('.schema-error')).toHaveCount(0)
    await expectHeaderRow(page, headers.locator('.schema-array-row'))
    await headers.getByLabel('Header name', { exact: true }).fill('X-Access')
    await headers.getByLabel('Header value', { exact: true }).fill('private-header-value')
    await headers.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('custom-advanced-connection.png'), animations: 'disabled' })
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.screenshot({ path: testInfo.outputPath('custom-advanced-dark.png'), animations: 'disabled' })
    await page.emulateMedia({ colorScheme: 'light' })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const connection = modal.locator('.configured-provider').filter({ hasText: '127.0.0.1' }).last()
    await expect(form).toHaveCount(0)
    const settings = await vista.api('/model-config')
    const model = settings.models.find((item: { headerNames?: string[] }) => item.headerNames?.includes('X-Access'))
    expect(model).toMatchObject({ id: '', name: '127.0.0.1', provider: 'openai-chat', providerId: 'custom', credentialMode: 'none', hasApiKey: false, headerNames: ['X-Access'] })
    await expect.poll(async () => (await vista.api('/models')).models.some((item: { connectionId: string; id: string }) => item.connectionId === model.connectionId && item.id === 'anonymous-test-model')).toBe(true)
    expect(JSON.stringify(settings)).not.toContain('private-header-value')
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(auth).toContainText('No authentication')
    await expect(headers.getByLabel('Header value', { exact: true })).toHaveValue('')
    await form.getByLabel(/^Provider name/).fill('Anonymous gateway')
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    await page.reload()
    const reloaded = (await vista.api('/model-config')).models.find((item: { key: string }) => item.key === model.key)
    expect(reloaded).toMatchObject({ name: 'Anonymous gateway', credentialMode: 'none', headerNames: ['X-Access'] })
    const modelPicker = page.locator('.default-model-setting .ui-select')
    await modelPicker.click()
    await modelPicker.locator('input').fill('anonymous-test-model')
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').filter({ hasText: 'anonymous-test-model' }).first().click()
    await closeSettings(page)
    await page.locator('.sender-card textarea').fill('Hello')
    await page.getByRole('button', { name: 'Send', exact: true }).click()
    await expect(page.locator('article.assistant .markdown')).toHaveText('Anonymous provider reply.')
    expect(received.some(item => item.path === '/v1/chat/completions')).toBe(true)
    expect(received.every(item => !item.auth && item.access === 'private-header-value')).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  } finally {
    upstream.closeAllConnections()
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()))
  }
})
