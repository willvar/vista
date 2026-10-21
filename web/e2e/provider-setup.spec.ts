import { closeSettings } from './settings-navigation'
import { createServer } from 'node:http'
import { test, expect } from './fixtures'

test.describe('custom provider dialogs', () => {
  test.use({ providerOutput: 'perplexity' })

  test('connect and edit use a second dialog and return to the provider list', async ({ page, vista }, testInfo) => {
    await vista.api('/provider-config', { method: 'DELETE', body: JSON.stringify({ key: 'test' }) })
    await page.goto(vista.origin + '/settings/models')
    const settings = page.locator('.settings-page')
    const dialog = page.locator('.provider-connection-modal')
    const backdrop = page.locator('.ant-modal-wrap').filter({ has: dialog })
    const form = dialog.locator('.model-form')
    const connect = settings.getByRole('button', { name: 'Connect provider', exact: true })
    await expect(connect).toBeVisible()
    await expect(form).toHaveCount(0)
    await connect.click()
    await expect(page.getByRole('dialog', { name: 'Connect provider', exact: true })).toBeVisible()
    await expect(settings.locator('.model-form')).toHaveCount(0)
    await form.locator('.ant-select input').fill('Custom provider')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom provider$/ }).click()
    const name = form.locator('label').filter({ hasText: /^Provider name/ }).locator('input')
    await name.fill('Discarded draft')
    await page.keyboard.press('Escape')
    await expect(form).toHaveCount(0)
    await expect(settings).toBeVisible()
    await connect.click()
    await expect(name).toHaveCount(0)
    await backdrop.click({ position: { x: 4, y: 4 } })
    await expect(form).toHaveCount(0)
    await expect(settings).toBeVisible()

    await connect.click()
    await form.locator('.ant-select input').fill('Custom provider')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom provider$/ }).click()
    await name.fill('Modal provider')
    await form.locator('label').filter({ hasText: /^Base URL/ }).locator('input').fill(vista.modelEndpoint)
    await form.locator('input[type=password]').fill('test-key')
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const connection = settings.locator('.configured-provider').filter({ hasText: 'Modal provider' })
    await expect(connection).toBeVisible()
    await expect(form).toHaveCount(0)
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(page.getByRole('dialog', { name: 'Edit provider connection', exact: true })).toBeVisible()
    await expect(form.locator('.provider-kind-value')).toHaveText('Custom provider')
    await name.fill('Unsaved rename')
    await page.screenshot({ path: testInfo.outputPath('provider-edit-dialog.png'), animations: 'disabled' })
    await backdrop.click({ position: { x: 4, y: 4 } })
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

test('custom model entry appears only after discovery fails and retry returns to automatic discovery', async ({ page, vista }, testInfo) => {
  let inventory: { id: string }[] | undefined
  let holdDiscovery = false
  let releaseDiscovery: (() => void) | undefined
  const upstream = createServer((request, response) => {
    if (request.url === '/v1/chat/completions') { response.writeHead(405).end(); return }
    if (request.url === '/v1/models') {
      if (holdDiscovery) {
        releaseDiscovery = () => { releaseDiscovery = undefined; response.writeHead(503).end() }
        return
      }
      if (!inventory) { response.writeHead(503).end(); return }
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: inventory }))
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
    const picker = form.locator('label').first().locator('.ant-select')
    await picker.locator('input').fill('Custom provider')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom provider$/ }).click()
    await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('Discovery fallback')
    await form.locator('label').filter({ hasText: /^Base URL/ }).locator('input').fill(`http://127.0.0.1:${address.port}/v1`)
    const apiKey = form.locator('label').filter({ hasText: /^Provider API key/ }).locator('input')
    await apiKey.fill('first-test-key')
    const fallback = form.locator('.model-discovery-fallback')
    const connect = form.getByRole('button', { name: 'Connect provider', exact: true })
    await expect(fallback).toHaveCount(0)
    await connect.click()
    await expect(form.getByRole('alert')).toHaveText('model list HTTP 503')
    await expect(fallback).toBeVisible()
    await expect(connect).toBeDisabled()
    await fallback.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('model-discovery-fallback.png'), animations: 'disabled' })

    // A changed credential must get a fresh discovery attempt.
    await apiKey.fill('second-test-key')
    await expect(fallback).toHaveCount(0)
    await expect(form.getByRole('alert')).toHaveCount(0)
    holdDiscovery = true
    await connect.click()
    await expect.poll(() => !!releaseDiscovery).toBe(true)
    await apiKey.fill('current-test-key')
    await form.locator('label').filter({ hasText: /^Conversation API format/ }).locator('.ant-select').click()
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^OpenAI Responses$/ }).click()
    await form.locator('label').filter({ hasText: /^Authentication/ }).locator('.ant-select').click()
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^x-api-key$/ }).click()
    const staleResponse = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/provider-config')
    releaseDiscovery!()
    await staleResponse
    await expect(connect).toBeEnabled()
    await expect(fallback).toHaveCount(0)
    await expect(form.getByRole('alert')).toHaveCount(0)
    holdDiscovery = false
    inventory = []
    await connect.click()
    await expect(form.getByRole('alert')).toHaveText('provider returned an empty model list')
    const modelIDs = fallback.locator('.ant-select input')
    await modelIDs.fill('manually-entered-model')
    await modelIDs.press('Enter')
    await modelIDs.press('Escape')

    inventory = [{ id: 'automatically-discovered-model' }]
    const submitted = page.waitForRequest(request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/provider-config')
    await fallback.getByRole('button', { name: 'Retry model discovery', exact: true }).click()
    expect((await submitted).postDataJSON()).toMatchObject({ provider: 'openai-responses', providerId: 'custom', credentialMode: 'x-api-key', ids: [], apiKey: 'current-test-key' })
    const connection = modal.locator('.configured-provider').filter({ hasText: 'Discovery fallback' })
    await expect(connection).toBeVisible()
    expect((await vista.api('/model-config')).models.find((model: { name: string }) => model.name === 'Discovery fallback'))
      .toMatchObject({ id: 'automatically-discovered-model' })
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(fallback).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
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
  const picker = form.locator('label').first().locator('.ant-select')
  await picker.click()
  await picker.locator('input').fill('百炼')
  const choices = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
  await expect(choices).toHaveCount(1)
  await expect(choices.first()).toContainText('Alibaba')
  await picker.locator('input').fill('z.ai')
  await expect(choices).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('search-providers.png') })
  await choices.first().click()
  await expect(form.getByText('Base URL', { exact: true })).toHaveCount(0)
  await expect(form.locator('.catalog-model-field, .model-list-field')).toHaveCount(0)
  await form.locator('label').filter({ hasText: /^Provider API key/ }).locator('input').fill('private-test-key')
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
  await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('Renamed Z.ai')
  await form.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(modal.locator('.configured-provider').filter({ hasText: 'Renamed Z.ai' })).toBeVisible()
  await page.reload()
  const reloaded = (await vista.api('/model-config')).models.find((item: { key: string }) => item.key === configured.key)
  expect(reloaded).toMatchObject({ connectionId: configured.connectionId, name: 'Renamed Z.ai', hasApiKey: true })
  await expect(page.locator('.settings-page')).toBeVisible()
  const refreshed = (await vista.api('/models')).models.filter((item: { connectionId: string }) => item.connectionId === configured.connectionId)
  expect(refreshed.map((item: { key: string }) => item.key)).toEqual(available.map((item: { key: string }) => item.key))
  await closeSettings(page)
  const modelPicker = page.locator('.composer-model')
  await modelPicker.click()
  await modelPicker.locator('input').fill(model.id)
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: model.modelName || model.id }).first().click()
  await expect(modelPicker).toContainText('Renamed Z.ai')
  await expect(modelPicker).toContainText(model.modelName || model.id)
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
      const picker = form.locator('label').first().locator('.ant-select')
      await picker.locator('input').fill(name)
      await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: name }).first().click()
    }
    await chooseProvider('Azure OpenAI')
    await form.locator('label').filter({ hasText: /^Azure resource name/ }).locator('input').fill('test-resource')
    await form.locator('label').filter({ hasText: /^Provider API key/ }).locator('input').fill('azure-secret')
    const setups = await vista.api('/provider-config')
    const azure = setups.find((setup: { id: string }) => setup.id === 'azure')
    const model = azure.models.find((item: { id: string }) => item.id === 'gpt-4.1')
    await form.locator('.catalog-model-field input').fill(model.id)
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: model.name || model.id }).first().click()
    await form.locator('label').filter({ hasText: /^Deployment name/ }).locator('input').fill('production-gpt')
    await page.screenshot({ path: testInfo.outputPath('azure-connection.png') })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(modal.locator('.configured-provider').filter({ hasText: 'Azure OpenAI' })).toBeVisible()
    expect((await vista.api('/model-config')).models.find((item: { providerId: string }) => item.providerId === 'azure')).toMatchObject({ resourceName: 'test-resource', deploymentId: 'production-gpt', id: model.id })
    await chooseProvider('vLLM')
    await expect(form.locator('label').filter({ hasText: /^Provider API key/ })).toHaveCount(0)
    await form.locator('label').filter({ hasText: /^Base URL/ }).locator('input').fill(`http://127.0.0.1:${address.port}/v1`)
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(modal.locator('.configured-provider').filter({ hasText: 'vLLM' })).toBeVisible()
    const local = (await vista.api('/model-config')).models.find((item: { providerId: string }) => item.providerId === 'vllm')
    expect(local).toMatchObject({ id: 'local-model', hasApiKey: false, credentialMode: 'default', editable: true })
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
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'gateway-test-model', supported_endpoints: ['/v1/chat/completions'] }] }))
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
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.goto(vista.origin + '/settings/models')
    const modal = page.locator('.settings-page')
    await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const picker = form.locator('label').first().locator('.ant-select')
    await picker.locator('input').fill('Custom provider')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom provider$/ }).click()
    const format = form.locator('label').filter({ hasText: /^Conversation API format/ }).locator('.ant-select')
    const auth = form.locator('label').filter({ hasText: /^Authentication/ }).locator('.ant-select')
    const key = form.locator('label').filter({ hasText: /^Provider API key/ }).locator('input')
    await expect(format).toContainText('OpenAI Chat Completions')
    await expect(auth).toContainText('Bearer')
    await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('Messages gateway')
    await form.locator('label').filter({ hasText: /^Base URL/ }).locator('input').fill(`http://127.0.0.1:${address.port}`)
    await key.fill('custom-test-key')
    await format.click()
    const choices = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
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
    await closeSettings(page)
    const modelPicker = page.locator('.composer-model')
    await modelPicker.click()
    await modelPicker.locator('input').fill('gateway-test-model')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: 'gateway-test-model' }).first().click()
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

test('custom connection supports anonymous access and advanced headers through save, edit and a real reply', async ({ page, vista }, testInfo) => {
  const received: { path: string; auth?: string; access?: string }[] = []
  const upstream = createServer(async (request, response) => {
    const path = new URL(request.url || '/', 'http://localhost').pathname
    received.push({ path, auth: request.headers.authorization, access: String(request.headers['x-access'] || '') })
    if (request.headers.authorization || request.headers['x-access'] !== 'private-header-value') {
      response.writeHead(401).end(); return
    }
    if (request.method === 'GET' && path === '/v1/models') {
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ data: [{ id: 'anonymous-test-model', supported_endpoints: ['/v1/chat/completions'] }] }))
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
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.goto(vista.origin + '/settings/models')
    const modal = page.locator('.settings-page')
    await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const picker = form.locator('label').first().locator('.ant-select')
    await picker.locator('input').fill('Custom provider')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom provider$/ }).click()
    await form.locator('label').filter({ hasText: /^Base URL/ }).locator('input').fill(`http://127.0.0.1:${address.port}`)
    const auth = form.locator('label').filter({ hasText: /^Authentication/ }).locator('.ant-select')
    await auth.click()
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^No authentication$/ }).click()
    await expect(form.locator('label').filter({ hasText: /^Provider API key/ })).toHaveCount(0)
    await expect(form.locator('.model-list-field')).toHaveCount(0)
    await expect(form.locator('label').filter({ hasText: /^Conversation API format/ }).locator('.ant-select')).toContainText('OpenAI Chat Completions')
    await form.locator('.provider-advanced summary').click()
    await form.getByRole('button', { name: 'Add request header', exact: true }).click()
    await form.getByRole('textbox', { name: 'Header name 1', exact: true }).fill('X-Access')
    await form.locator('input[aria-label="Header value 1"]').fill('private-header-value')
    await form.locator('.provider-advanced').scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('custom-advanced-connection.png'), animations: 'disabled' })
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.screenshot({ path: testInfo.outputPath('custom-advanced-dark.png'), animations: 'disabled' })
    await page.emulateMedia({ colorScheme: 'light' })
    await form.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const connection = modal.locator('.configured-provider').filter({ hasText: '127.0.0.1' }).last()
    await expect(form).toHaveCount(0)
    const settings = await vista.api('/model-config')
    const model = settings.models.find((item: { id: string }) => item.id === 'anonymous-test-model')
    expect(model).toMatchObject({ name: '127.0.0.1', provider: 'openai-chat', providerId: 'custom', credentialMode: 'none', hasApiKey: false, headerNames: ['X-Access'] })
    expect(JSON.stringify(settings)).not.toContain('private-header-value')
    await connection.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(auth).toContainText('No authentication')
    await expect(form.locator('input[aria-label="Header value 1"]')).toHaveValue('')
    await expect(form.locator('input[aria-label="Header value 1"]')).toHaveAttribute('placeholder', 'Leave blank to keep saved value')
    await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('Anonymous gateway')
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(form).toHaveCount(0)
    await page.reload()
    const reloaded = (await vista.api('/model-config')).models.find((item: { key: string }) => item.key === model.key)
    expect(reloaded).toMatchObject({ name: 'Anonymous gateway', credentialMode: 'none', headerNames: ['X-Access'] })
    await closeSettings(page)
    const modelPicker = page.locator('.composer-model')
    await modelPicker.click()
    await modelPicker.locator('input').fill('anonymous-test-model')
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: 'anonymous-test-model' }).first().click()
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
