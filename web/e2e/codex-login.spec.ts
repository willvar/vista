import { test, expect } from './fixtures'

for (const account of [
 { kind: 'codex', providerId: 'openai-codex', providerName: 'OpenAI Codex', service: 'ChatGPT', verify: 'https://auth.openai.com/codex/device' },
 { kind: 'copilot', providerId: 'github-copilot', providerName: 'GitHub Copilot', service: 'GitHub', verify: 'https://github.com/login/device' },
]) {
test(`${account.providerName} sign-in uses a device code and connects without a key or model choice`, async ({ page, context, vista }, testInfo) => {
  let authorized = false
  let connected = false
  let canceled = 0
  const pending = { id: 'login-fixture', userCode: 'ABCD-EFGH', verificationUrl: account.verify, status: 'pending' }
  const model = { baseUrl: account.kind === 'copilot' ? 'https://api.githubcopilot.com' : '', key: 'codex-fixture', connectionId: 'codex-account', kind: account.providerId, name: 'My Codex', provider: 'catalog', providerId: account.providerId, id: 'gpt-6.1-sol', editable: true, hasApiKey: account.kind === 'copilot', credentialMode: 'oauth' }
  await context.route(account.verify, route => route.fulfill({ contentType: 'text/html', body: '<h1>Isolated authorization page</h1>' }))
  await page.route(`**/api/provider-auth/${account.kind}**`, async route => {
    if (route.request().method() === 'POST') {
      expect(route.request().postDataJSON()).toEqual({ key: '', name: 'My Codex', ...(account.kind === 'copilot' ? { copilotAuth: { enterpriseUrl: '', clientId: '' } } : {}) })
      await route.fulfill({ json: pending })
    } else if (route.request().method() === 'DELETE') {
      canceled++
      await route.fulfill({ json: { ...pending, status: 'canceled' } })
    } else {
      if (authorized) connected = true
      await route.fulfill({ json: { ...pending, status: connected ? 'connected' : 'pending' } })
    }
  })
  await page.route('**/api/model-config', async route => {
    const response = await route.fetch()
    const json = await response.json()
    if (connected) json.models.push(model)
    await route.fulfill({ response, json })
  })
  await page.route('**/api/models', async route => {
    const response = await route.fetch()
    const json = await response.json()
    if (connected) json.models.push({ ...model, provider: 'openai-responses', modelName: 'GPT-6.1 Sol', reasoningOptions: ['low', 'medium', 'high', 'xhigh', 'max'] })
    await route.fulfill({ response, json })
  })
  await page.goto(vista.origin + '/settings/models')
  const modal = page.locator('.settings-page')
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  await form.locator('label').first().locator('input').fill(account.kind)
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: account.providerName }).click()
  await expect(form.locator('input[type=password], .catalog-model-field, .model-list-field')).toHaveCount(0)
  await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('My Codex')
  await form.getByRole('button', { name: `Sign in with ${account.service}`, exact: true }).click()
  await expect(form.locator('.provider-device-code')).toHaveText('ABCD-EFGH')
  await expect(form.getByRole('status')).toContainText('Waiting for authorization')
  const opened = page.waitForEvent('popup')
  await form.getByRole('link', { name: `Open ${account.service} sign-in` }).click()
  const popup = await opened
  await expect(popup.getByRole('heading')).toHaveText('Isolated authorization page')
  await popup.close()
  await page.screenshot({ path: testInfo.outputPath(`${account.kind}-device-login.png`) })
  await form.getByRole('button', { name: 'Cancel sign-in' }).click()
  await expect(form.getByRole('button', { name: `Sign in with ${account.service}`, exact: true })).toBeVisible()
  expect(canceled).toBe(1)
  expect(connected).toBe(false)
  await form.getByRole('button', { name: `Sign in with ${account.service}`, exact: true }).click()
  await expect(form.locator('.provider-device-code')).toBeVisible()
  await form.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(form).toHaveCount(0)
  await expect(modal).toBeVisible()
  await expect.poll(() => canceled).toBe(2)
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  await form.locator('label').first().locator('input').fill(account.kind)
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: account.providerName }).click()
  await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill('My Codex')
  await form.getByRole('button', { name: `Sign in with ${account.service}`, exact: true }).click()
  await expect(form.locator('.provider-device-code')).toBeVisible()
  authorized = true
  await expect(modal.locator('.configured-provider').filter({ hasText: 'My Codex' })).toBeVisible()
  await expect(form).toHaveCount(0)
  await page.reload()
  const connection = modal.locator('.configured-provider').filter({ hasText: 'My Codex' })
  await expect(connection).toBeVisible()
  await connection.getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(form.getByRole('button', { name: 'Sign in again', exact: true })).toBeVisible()
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
  await expect(form.locator('input[type=password]')).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
})

}

test('Copilot advanced login settings round-trip through connect, reload and reconnect', async ({ page, vista }, testInfo) => {
  let authorized = false
  let connected = false
  let expectedOptions = { enterpriseUrl: 'company.ghe.com', clientId: 'enterprise-app' }
  const savedOptions = { enterpriseUrl: 'https://company.ghe.com', clientId: 'enterprise-app' }
  const model = { key: 'enterprise-copilot', connectionId: 'enterprise-account', kind: 'github-copilot', name: 'Enterprise Copilot', provider: 'openai-responses', providerId: 'github-copilot', id: 'gpt-6-sol', baseUrl: 'https://copilot-api.company.ghe.com', editable: true, hasApiKey: true, credentialMode: 'oauth', copilotAuth: savedOptions }
  await page.route('**/api/provider-auth/copilot**', async route => {
    const pending = { id: 'enterprise-login', userCode: 'ENTERPRISE', verificationUrl: 'https://company.ghe.com/login/device', status: 'pending' }
    if (route.request().method() === 'POST') {
      expect(route.request().postDataJSON()).toEqual({ key: connected ? model.key : '', name: model.name, copilotAuth: expectedOptions })
      await route.fulfill({ json: pending })
    } else {
      const status = route.request().method() === 'DELETE' ? 'canceled' : authorized ? 'connected' : 'pending'
      if (status === 'connected') connected = true
      await route.fulfill({ json: { ...pending, status } })
    }
  })
  await page.route('**/api/model-config', async route => {
    const response = await route.fetch()
    const json = await response.json()
    if (connected) json.models.push(model)
    await route.fulfill({ response, json })
  })
  await page.goto(vista.origin + '/settings/models')
  const modal = page.locator('.settings-page')
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  await form.locator('label').first().locator('input').fill('copilot')
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: 'GitHub Copilot' }).click()
  await form.locator('label').filter({ hasText: /^Provider name/ }).locator('input').fill(model.name)
  await expect(form.getByLabel('GitHub Enterprise domain')).not.toBeVisible()
  await form.locator('.provider-advanced summary').click()
  const enterprise = form.getByLabel('GitHub Enterprise domain')
  const clientID = form.getByLabel('OAuth Client ID')
  await enterprise.fill(expectedOptions.enterpriseUrl)
  await clientID.fill(expectedOptions.clientId)
  await page.screenshot({ path: testInfo.outputPath('copilot-advanced-settings.png'), animations: 'disabled' })
  await form.getByRole('button', { name: 'Sign in with GitHub', exact: true }).click()
  await expect(enterprise).toBeDisabled()
  await expect(clientID).toBeDisabled()
  await expect(form.locator('.provider-device-code')).toHaveText('ENTERPRISE')
  authorized = true
  await expect(form).toHaveCount(0)
  await page.reload()
  await modal.locator('.configured-provider').filter({ hasText: model.name }).getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(enterprise).toBeVisible()
  await expect(enterprise).toHaveValue(savedOptions.enterpriseUrl)
  await expect(clientID).toHaveValue(savedOptions.clientId)
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
  authorized = false
  expectedOptions = { ...savedOptions, clientId: 'another-app' }
  await clientID.fill(expectedOptions.clientId)
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  await form.getByRole('button', { name: 'Sign in again', exact: true }).click()
  await expect(form.locator('.provider-device-code')).toBeVisible()
  await form.getByRole('button', { name: 'Cancel sign-in' }).click()
  await expect(enterprise).toBeEnabled()
  expectedOptions = { enterpriseUrl: '', clientId: '' }
  await enterprise.fill('')
  await clientID.fill('')
  await form.getByRole('button', { name: 'Sign in again', exact: true }).click()
  await expect(form.locator('.provider-device-code')).toBeVisible()
  await form.getByRole('button', { name: 'Cancel sign-in' }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
})

test('Codex expired and abandoned logins allow retry and cancel late results', async ({ page, vista }) => {
  let expires = true
  let canceled = 0
  let releaseStart: (() => void) | undefined
  await page.route('**/api/provider-auth/codex**', async route => {
    if (route.request().method() === 'POST') {
      if (!expires) await new Promise<void>(resolve => { releaseStart = resolve })
      await route.fulfill({ json: { id: 'expiring-login', userCode: 'ABCD-EFGH', verificationUrl: 'https://auth.openai.com/codex/device', status: 'pending' } })
    } else if (route.request().method() === 'DELETE') {
      canceled++
      await route.fulfill({ json: { status: 'canceled' } })
    } else {
      await route.fulfill({ status: 404, json: { code: 'invalid_settings', message: 'Login expired' } })
    }
  })
  await page.goto(vista.origin + '/settings/models')
  const modal = page.locator('.settings-page')
  await modal.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  const picker = form.locator('label').first().locator('input')
  await picker.fill('codex')
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: 'OpenAI Codex' }).click()
  await form.getByRole('button', { name: 'Sign in with ChatGPT', exact: true }).click()
  await expect(form.getByRole('alert')).toContainText('Sign-in expired')
  await expect(form.getByRole('button', { name: 'Sign in with ChatGPT', exact: true })).toBeEnabled()
  expires = false
  await form.getByRole('button', { name: 'Sign in with ChatGPT', exact: true }).click()
  await expect.poll(() => !!releaseStart).toBe(true)
  await picker.fill('Z.ai')
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Z\.ai$/ }).click()
  releaseStart!()
  await expect.poll(() => canceled).toBe(1)
  await expect(form.locator('.provider-login')).toHaveCount(0)
  await expect(form.locator('input[type=password]')).toBeVisible()
  expect((await vista.api('/model-config')).models.some((item: { providerId: string }) => item.providerId === 'openai-codex')).toBe(false)
})
