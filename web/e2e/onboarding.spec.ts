import { closeSettings, openAboutAdvanced, saveAboutAdvanced, openSettingsSection } from './settings-navigation'
import { test, expect } from './fixtures'

for (const locale of ['zh-CN', 'fr-FR']) {
  test.describe(`browser language ${locale}`, () => {
    test.use({ locale })
    test('browser language, explicit override, and reset persist through reload', async ({ page, vista }) => {
      const initial = locale === 'zh-CN' ? 'zh-CN' : 'fr'
      const override = initial === 'zh-CN' ? 'en' : 'zh-CN'
      await expect(page.locator('html')).toHaveAttribute('lang', initial)
      await page.goto(vista.origin + '/settings/general')
      const picker = page.locator('.language-setting .ui-select')
      await picker.click()
      await picker.locator('input').fill(override === 'en' ? 'eNgLiSh' : 'zh-CN')
      const options = page.locator('.ui-select-dropdown:visible .ui-select-item-option')
      await expect(options).toHaveText([override === 'en' ? 'English' : '简体中文'])
      await options.click()
      await expect(page.locator('html')).toHaveAttribute('lang', override)
      await page.reload()
      await expect(page.locator('html')).toHaveAttribute('lang', override)
      expect(await page.evaluate(() => document.cookie)).toContain(`vista-language=${override}`)
      await picker.click()
      await picker.locator('input').fill('auto')
      await page.locator('.ui-select-dropdown:visible .ui-select-item-option')
        .filter({ hasText: override === 'en' ? /^Auto$/ : /^自动$/ }).click()
      await page.reload()
      await expect(page.locator('html')).toHaveAttribute('lang', initial)
    })
  })
}

test('provider recovery follows error codes and keeps the draft through a settings conflict', async ({ page, vista }) => {
  const failures = [
    { status: 400, code: 'invalid_settings', message: 'choose Chat Completions or Responses manually; enter model IDs manually' },
    { status: 400, code: 'model_ids_required', message: 'The inventory needs your help.' },
    { status: 409, code: 'settings_conflict', message: 'Another connection was saved. Please retry this draft.' },
  ]
  const attempts: Record<string, unknown>[] = []
  await page.route('**/api/provider-config', async route => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    attempts.push(route.request().postDataJSON())
    const failure = failures[attempts.length - 1]
    if (!failure) { await route.continue(); return }
    const { status, ...body } = failure
    await route.fulfill({ status, json: body })
  })
  await page.goto(vista.origin + '/settings/models')
  const modal = page.locator('.settings-page')
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  const providerPicker = form.locator('.provider-kind-select .ui-select')
  await providerPicker.click()
  await providerPicker.locator('input').fill('Custom provider')
  await page.locator('.ui-select-dropdown:visible .ui-select-item-option')
    .filter({ hasText: /^Custom provider$/ }).click()
  const name = form.getByLabel(/^Provider name/)
  const baseURL = form.getByLabel(/^Base URL/)
  const apiKey = form.getByLabel(/^Provider API key/)
  await name.fill('Recovery Provider')
  await baseURL.fill(vista.modelEndpoint)
  await apiKey.fill('test-key')
  const connect = form.getByRole('button', { name: 'Connect provider', exact: true })
  const format = form.locator('[data-field-path=apiFormat] .ui-select')
  const modelIDs = form.locator('.model-list-field .ui-select')
  const submit = async (message: string) => {
    await connect.scrollIntoViewIfNeeded()
    await connect.click()
    await expect(form.getByRole('alert')).toHaveText(message)
  }

  // Old wording alone must not control the form.
  await submit(failures[0]!.message)
  await expect(format).toContainText('OpenAI Chat Completions')
  await expect(modelIDs).toHaveCount(0)
  await submit(failures[1]!.message)
  await expect(modelIDs).toHaveCount(0)
  await form.getByRole('button', { name: 'Enter model IDs manually', exact: true }).click()
  await expect(modelIDs).toBeVisible()
  await expect(connect).toBeDisabled()
  await modelIDs.locator('input').fill(vista.modelId)
  await modelIDs.locator('input').press('Enter')
  await modelIDs.locator('input').press('Escape')
  await submit(failures[2]!.message)
  await expect(name).toHaveValue('Recovery Provider')
  await expect(baseURL).toHaveValue(vista.modelEndpoint)
  await expect(apiKey).toHaveValue('test-key')
  await expect(format).toContainText('OpenAI Chat Completions')
  const existingCatalog = await vista.api('/models')
  await expect(modelIDs).toContainText(existingCatalog.models.find((model: { id: string }) => model.id === vista.modelId).modelName || vista.modelId)
  await expect(connect).toBeEnabled()
  expect(attempts[2]).toMatchObject({ name: 'Recovery Provider', provider: 'openai-chat', providerId: 'custom', credentialMode: 'bearer', baseUrl: vista.modelEndpoint, apiKey: 'test-key', ids: [vista.modelId] })

  await connect.click()
  await expect(modal.locator('.configured-provider').filter({ hasText: 'Recovery Provider' })).toBeVisible()
  await expect(form).toHaveCount(0)
  await expect(modal.getByRole('alert')).toHaveCount(0)
  expect(attempts).toHaveLength(4)
  expect(attempts[3]).toEqual(attempts[2])
  await expect.poll(async () => {
    const catalog = await vista.api('/models') as { models: { name?: string; id: string }[] }
    return catalog.models.some(model => model.name === 'Recovery Provider' && model.id === vista.modelId)
  }).toBe(true)
})

test.describe('first launch without a provider', () => {
  const providerEfforts = ['low', 'high', 'max']
  const reasoningOptions = ['default', ...providerEfforts]
  test.use({ configuredProvider: false, advertisedReasoningEfforts: providerEfforts })
  test('connect a provider and create the first session only when sending Hi', { tag: '@smoke' }, async ({ page, vista }, testInfo) => {
    let creationRequests = 0
    let messageRequests = 0
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'POST' && path === '/api/sessions') creationRequests++
      if (request.method() === 'POST' && /^\/api\/sessions\/[^/]+\/messages$/.test(path)) messageRequests++
    })
    expect(await vista.api('/sessions')).toHaveLength(0)
    expect(await vista.api('/models')).toMatchObject({ default: '', models: [] })
    expect(await vista.api('/status')).toMatchObject({ provider: '', model: '', defaultModel: '', defaultModelKey: '' })
    const sender = page.locator('.sender-card')
    await expect(sender).toBeVisible()
    await expect(sender).toHaveText('Connect a provider to get started')
    await expect(sender.locator('.sender-picker, textarea')).toHaveCount(0)
    await expect(page.locator('.welcome').getByRole('button', { name: 'Connect provider', exact: true })).toHaveCount(0)
    await expect(page.locator('.panel-heading')).toHaveCount(0)
    const openSidebar = async () => {
      const trigger = page.locator('.mobile-sessions-trigger:visible')
      if (await trigger.count()) await trigger.click()
    }
    const expectNoPageOverflow = async () => {
      const bounds = await sender.evaluate(element => {
        const box = element.getBoundingClientRect()
        return { left: box.left, right: box.right, top: box.top, bottom: box.bottom, width: innerWidth, height: innerHeight, pageWidth: document.documentElement.scrollWidth }
      })
      expect(bounds.left).toBeGreaterThanOrEqual(-1)
      expect(bounds.top).toBeGreaterThanOrEqual(-1)
      expect(bounds.right).toBeLessThanOrEqual(bounds.width + 1)
      expect(bounds.bottom).toBeLessThanOrEqual(bounds.height + 1)
      expect(bounds.pageWidth).toBeLessThanOrEqual(bounds.width + 2)
    }
    await expectNoPageOverflow()
    await page.screenshot({ path: testInfo.outputPath('welcome-without-provider.png'), animations: 'disabled' })
    await sender.click()
    await expect(page).toHaveURL(/\/settings\/models(?:\?.*)?$/)
    const modal = page.locator('.settings-page')
    const content = modal.locator('.settings-content')
    await expect(modal).toBeVisible()
    await expect(modal.locator('.provider-empty-state')).toContainText('Use the ＋ in the top right to add your first provider.')
    await expect(modal.getByRole('button', { name: 'Connect provider', exact: true })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath('model-connections-without-provider.png'), animations: 'disabled' })
    await openSettingsSection(page, 'about')
    const advanced = await openAboutAdvanced(page, 'Debug mode')
    await advanced.getByRole('switch', { name: 'Debug mode', exact: true }).click()
    await saveAboutAdvanced(page)
    await expect(advanced.getByRole('switch', { name: 'Debug mode', exact: true })).toBeChecked()
    await openSettingsSection(page, 'models')
    await expect(page).toHaveURL(/\/settings\/models(?:\?.*)?$/)

    await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const form = page.locator('.provider-connection-modal .model-form')
    const providerPicker = form.locator('.provider-kind-select .ui-select')
    await providerPicker.scrollIntoViewIfNeeded()
    await providerPicker.click()
    await providerPicker.locator('input').fill('Custom provider')
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option')
      .filter({ hasText: /^Custom provider$/ }).click()

    await form.getByLabel(/^Provider name/).fill('E2E Provider')
    await form.getByLabel(/^Base URL/).fill(vista.modelEndpoint)
    await form.getByLabel(/^Provider API key/).fill('test-key')
    const connect = form.getByRole('button', { name: 'Connect provider', exact: true })
    await connect.scrollIntoViewIfNeeded()
    await expect(connect).toBeEnabled()
    const modalGeometry = await page.locator('.provider-connection-modal').evaluate(section => {
      const rect = section.getBoundingClientRect()
      const scroll = section.querySelector('.ui-modal-body')!
      return {
        width: innerWidth, height: innerHeight,
        left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom,
        pageWidth: document.documentElement.scrollWidth,
        pageHeight: document.documentElement.scrollHeight,
        contentHeight: scroll.clientHeight, contentScrollHeight: scroll.scrollHeight,
        contentScrollTop: scroll.scrollTop,
      }
    })
    expect(modalGeometry.left).toBeGreaterThanOrEqual(-1)
    expect(modalGeometry.top).toBeGreaterThanOrEqual(-1)
    expect(modalGeometry.right).toBeLessThanOrEqual(modalGeometry.width + 1)
    expect(modalGeometry.bottom).toBeLessThanOrEqual(modalGeometry.height + 1)
    expect(modalGeometry.pageWidth).toBeLessThanOrEqual(modalGeometry.width + 2)
    expect(modalGeometry.pageHeight).toBeLessThanOrEqual(modalGeometry.height + 2)
    if (modalGeometry.height <= 400) {
      expect(modalGeometry.contentScrollHeight).toBeGreaterThan(modalGeometry.contentHeight)
      expect(modalGeometry.contentScrollTop).toBeGreaterThan(0)
    }
    await connect.click()
    const connection = modal.locator('.configured-provider').filter({ has: page.getByText('E2E Provider', { exact: true }) })
    await expect(connection).toBeVisible()
    await expect(form).toHaveCount(0)
    await expect(modal.locator('.model-list-field')).toHaveCount(0)
    // The model ID alone is not a capability declaration. Wait for automatic discovery of its advertised levels.
    await expect.poll(async () => {
      const catalog = await vista.api('/models') as { models: { name?: string; id: string; reasoningOptions?: string[] }[] }
      return catalog.models.find(model => model.name === 'E2E Provider' && model.id === vista.modelId)?.reasoningOptions
    }).toEqual(reasoningOptions)
    await closeSettings(page)
    await expect(modal).toBeHidden()

    await expect(page).toHaveURL(url => url.pathname === '/')
    await expect(page.locator('.welcome')).toBeVisible()
    await expect(sender).toBeVisible()
    await expect(sender.locator('.composer-model')).toHaveCount(0)

    const reasoningEffort = reasoningOptions.at(-1)!
    const reasoningLabel = reasoningEffort.charAt(0).toUpperCase() + reasoningEffort.slice(1)
    const responseOptions = page.getByRole('button', { name: 'Response options', exact: true })
    const responsePanel = page.getByRole('dialog', { name: 'Response options', exact: true })
    const reasoningPicker = responsePanel.getByRole('combobox', { name: 'Reasoning effort', exact: true })
    await expect(sender.getByRole('combobox', { name: 'Reasoning effort' })).toHaveCount(0)
    await responseOptions.click()
    const reasoningList = page.locator('.ui-select-dropdown:visible')
    await reasoningPicker.click()
    await expect(reasoningList.getByRole('option')).toHaveText(['Service default', 'Low', 'High', 'Max'])
    await reasoningList.getByRole('option', { name: reasoningLabel, exact: true }).click()

    await page.keyboard.press('Escape')
    await expect(responsePanel).toBeHidden()
    await sender.getByPlaceholder('Send a message').fill('Hi')
    expect(await vista.api('/sessions')).toHaveLength(0)
    expect(creationRequests).toBe(0)
    await openSidebar()
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    await expect(page).toHaveURL(url => url.pathname === (page.viewportSize()!.width <= 830 ? '/settings' : '/settings/general') && !url.searchParams.has('session'))
    await closeSettings(page)
    await expect(page).toHaveURL(url => url.pathname === '/')
    await expect(sender.getByPlaceholder('Send a message')).toHaveValue('Hi')
    await expect(sender.locator('.composer-model')).toHaveCount(0)
    await expect(responsePanel).toBeHidden()
    await responseOptions.click()
    await expect(reasoningPicker).toHaveValue(reasoningLabel)
    await responseOptions.click()
    await expect(responsePanel).toBeHidden()
    expect(await vista.api('/sessions')).toHaveLength(0)
    expect(creationRequests).toBe(0)
    await expectNoPageOverflow()
    await page.screenshot({ path: testInfo.outputPath('welcome-before-first-send.png'), animations: 'disabled' })
    const send = sender.getByRole('button', { name: 'Send', exact: true })
    await send.scrollIntoViewIfNeeded()
    await expect(send).toBeEnabled()
    const submitted = page.waitForRequest(request => request.method() === 'POST'
      && /^\/api\/sessions\/[^/]+\/messages$/.test(new URL(request.url()).pathname))
    await send.click()
    expect((await submitted).postDataJSON()).toMatchObject({ content: 'Hi', reasoningEffort })
    await expect(page).toHaveURL(/\/sessions\/[^/?#]+$/)
    const sessionPath = new URL(page.url()).pathname
    await expect.poll(() => creationRequests).toBe(1)
    await expect.poll(async () => (await vista.api('/sessions')).length).toBe(1)
    await expect(page.locator('.messages article.user .markdown')).toHaveText('Hi')
    const reply = page.locator('.messages article.assistant').filter({ hasText: 'Mock reply: Hi' })
    await expect(reply.locator('.markdown')).toHaveText('Mock reply: Hi')
    expect(vista.modelRequests.at(-1)?.reasoning_effort).toBe(reasoningEffort)
    await expect(reply.locator('.reply-footer .message-model-name')).toHaveText('E2E Provider · GPT-6 Luna')
    await expect(reply.locator('.message-model-results')).toBeVisible()
    await expect(sender.getByPlaceholder('Send a message')).toHaveValue('')
    expect(messageRequests).toBe(1)

    await page.reload()
    await expect(page).toHaveURL(url => url.pathname === sessionPath)
    await expect(reply.locator('.markdown')).toHaveText('Mock reply: Hi')
    await expect(reply.locator('.reply-footer .message-model-name')).toHaveText('E2E Provider · GPT-6 Luna')
    await reply.getByRole('button', { name: 'Model usage details', exact: true }).click()
    await expect(page.locator('.usage-popover:visible .usage-details')).toContainText('Reply totals')
    await page.keyboard.press('Escape')
    await expect(page.locator('.usage-popover:visible')).toHaveCount(0)
    await responseOptions.click()
    const defaultReasoningLabel = 'Service default'
    await reasoningPicker.click()
    await reasoningList.getByRole('option', { name: defaultReasoningLabel, exact: true }).click()
    await expect(reasoningPicker).toHaveValue(defaultReasoningLabel)
    await page.keyboard.press('Escape')
    await expect(responsePanel).toBeHidden()
    await expect(reply.locator('.reply-footer .message-model-name')).toHaveText('E2E Provider · GPT-6 Luna')
    expect(messageRequests).toBe(1)
    await expect(page.locator('.error')).toHaveCount(0)
    await expect(page.locator('.workspace > small')).toHaveCount(0)
    const worker = page.locator('.conversation-header .worker-status-trigger')
    const metrics = page.locator('.worker-metrics-detail:visible')
    await expect(worker).toBeVisible()
    await expect(worker).toHaveText('')
    await expect(metrics).toHaveCount(0)
    const headerBefore = await page.locator('.conversation-header').boundingBox()
    const senderBefore = await sender.boundingBox()
    await worker.click()
    await expect(metrics).toContainText('Worker heap')
    await expect(metrics).toContainText('RSS')
    await expect(metrics).toContainText('Sampled')
    expect(await page.locator('.conversation-header').boundingBox()).toEqual(headerBefore)
    expect(await sender.boundingBox()).toEqual(senderBefore)
    await sender.locator('textarea').click()
    await expect(metrics).toHaveCount(0)
    await worker.click()
    await expect(metrics).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(metrics).toHaveCount(0)
    const headerGeometry = await page.locator('.conversation-header-tools').evaluate(element => {
      const metric = element.querySelector('.worker-status-trigger')!
      const bounds = element.getBoundingClientRect()
      return { left: bounds.left, right: bounds.right, top: bounds.top, bottom: bounds.bottom, width: innerWidth, height: innerHeight, overflow: metric.scrollWidth - metric.clientWidth }
    })
    expect(headerGeometry.left).toBeGreaterThanOrEqual(-1)
    expect(headerGeometry.top).toBeGreaterThanOrEqual(-1)
    expect(headerGeometry.right).toBeLessThanOrEqual(headerGeometry.width + 1)
    expect(headerGeometry.bottom).toBeLessThanOrEqual(headerGeometry.height + 1)
    await expect(page.locator('.conversation-header .context-status')).toHaveCount(0)
    await expect(page.locator('.conversation-header .context-compaction')).toBeVisible()
    expect(headerGeometry.overflow).toBeLessThanOrEqual(2)
    await expectNoPageOverflow()
    await page.screenshot({ path: testInfo.outputPath('conversation-after-first-send.png'), animations: 'disabled' })
    await testInfo.attach('conversation-after-refresh', { body: await page.screenshot(), contentType: 'image/png' })
    const savedDraft = 'Keep this draft when activating the conversation again'
    await sender.locator('textarea').fill(savedDraft)
    await openSidebar()
    await page.locator('.session-card.selected').click()
    await expect(page).toHaveURL(url => url.pathname === '/')
    await expect(page.locator('.welcome')).toBeVisible()
    expect(await vista.api('/sessions')).toHaveLength(1)
    expect(creationRequests).toBe(1)
    await openSidebar()
    await page.locator('.session-card').first().click()
    await expect(page).toHaveURL(url => url.pathname === sessionPath)
    await expect(sender.locator('textarea')).toBeFocused()
    await expect(sender.locator('textarea')).toHaveValue(savedDraft)
    await page.keyboard.insertText(' and keep typing')
    await expect(sender.locator('textarea')).toHaveValue(savedDraft + ' and keep typing')
    await openSidebar()
    await page.getByRole('button', { name: 'Start a new conversation', exact: true }).click()
    await expect(page).toHaveURL(url => url.pathname === '/')
    await expect(page.locator('.welcome')).toBeVisible()
    await expect(sender.getByPlaceholder('Send a message')).toHaveValue('')
    await expect(sender.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
    expect(await vista.api('/sessions')).toHaveLength(1)
    expect(creationRequests).toBe(1)
    expect(messageRequests).toBe(1)
    await expectNoPageOverflow()
  })
})
