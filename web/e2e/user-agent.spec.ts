import { test, expect } from './fixtures'
import type { UserAgentSettings } from '../src/client/types'

test('UA presets preview exact headers and custom changes reach a resident Worker', async ({ page, vista }, testInfo) => {
  const initial = await vista.api('/user-agent') as UserAgentSettings
  expect(initial.preset).toBe('vista')
  const version = (await vista.api('/status')).version.replace(/^v/, '')
  expect(initial.value.split(' ', 1)[0]).toBe('vista/' + version)
  expect(initial.value).toMatch(/^vista\/\S+ \((Linux|Windows|macOS); (x86_64|arm64)\)$/)
  expect(initial.presets.map(preset => preset.id)).toEqual(expect.arrayContaining(['gemini-cli', 'kimi-code', 'cline', 'github-copilot']))
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  async function send(content: string, value: string) {
    const count = vista.modelUserAgents.length
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content }) })
    await expect.poll(() => vista.modelUserAgents.length).toBeGreaterThan(count)
    await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id).executing).toBe(false)
    expect(vista.modelUserAgents.at(-1)).toBe(value)
  }
  await send('First request', initial.value)
  const workerPID = (await vista.api('/status')).workerMetrics.find((worker: { sessionId: string }) => worker.sessionId === session.id)?.pid
  expect(workerPID).toBeGreaterThan(0)
  await page.goto(vista.origin + '/settings/about')
  const region = page.getByRole('region', { name: 'User-Agent', exact: true })
  await expect(region.locator('.user-agent-hint')).toContainText('Login, token exchange, and special channels keep their own client identities.')
  await expect(region.locator('.user-agent-preview>span')).toHaveText('General request preview')
  const preview = region.locator('.user-agent-preview code')
  const save = region.getByRole('button', { name: 'Save changes', exact: true })
  await expect(preview).toHaveText('User-Agent: ' + initial.value)
  await expect(save).toBeDisabled()
  for (const preset of initial.presets) {
    await region.locator('#user-agent-preset').click()
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: new RegExp('^' + preset.name + '$') }).click()
    await expect(preview).toHaveText('User-Agent: ' + preset.value)
  }
  // Preview alone has not modified the saved selection.
  expect((await vista.api('/user-agent')).preset).toBe('vista')
  await save.click()
  await expect(save).toBeDisabled()
  const selected = initial.presets.at(-1)!
  expect((await vista.api('/user-agent')).preset).toBe(selected.id)
  await send('Preset request', selected.value)
  await region.locator('#user-agent-preset').click()
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: /^Custom$/ }).click()
  await expect(save).toBeDisabled()
  const input = region.getByRole('textbox', { name: 'Custom UA', exact: true })
  await input.fill('客户端/1')
  await expect(save).toBeDisabled()
  await expect(region.locator('#user-agent-invalid')).toBeVisible()
  const custom = 'MyClient/2.0 (' + 'details; '.repeat(50) + 'test)'
  await input.fill(custom)
  await expect(preview).toHaveText('User-Agent: ' + custom)
  await save.click()
  await expect(save).toBeDisabled()
  await send('Custom request', custom)
  const currentPID = (await vista.api('/status')).workerMetrics.find((worker: { sessionId: string }) => worker.sessionId === session.id)?.pid
  expect(currentPID).toBe(workerPID)
  await page.reload()
  await expect(input).toHaveValue(custom)
  await expect(preview).toHaveText('User-Agent: ' + custom)
  await page.emulateMedia({ colorScheme: 'dark' })
  await preview.scrollIntoViewIfNeeded()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  await region.screenshot({ path: testInfo.outputPath('user-agent-dark.png'), animations: 'disabled' })
})

test('UA save errors retain the draft and allow retry', async ({ page, vista }) => {
  let rejected = false
  await page.route('**/api/user-agent', async route => {
    if (route.request().method() === 'PUT' && !rejected) {
      rejected = true
      await route.fulfill({ status: 500, body: 'could not save User-Agent settings' })
    } else await route.continue()
  })
  await page.goto(vista.origin + '/settings/about')
  const region = page.getByRole('region', { name: 'User-Agent', exact: true })
  await region.locator('#user-agent-preset').click()
  // From Vista, wrap to Custom and select it without racing virtual scrolling.
  await region.locator('#user-agent-preset').press('ArrowUp')
  await region.locator('#user-agent-preset').press('Enter')
  const input = region.getByRole('textbox', { name: 'Custom UA', exact: true })
  await input.fill('RetainedClient/1')
  const save = region.getByRole('button', { name: 'Save changes', exact: true })
  await save.click()
  await expect(region.getByRole('alert')).toContainText('could not save User-Agent settings')
  await expect(input).toHaveValue('RetainedClient/1')
  expect((await vista.api('/user-agent')).preset).toBe('vista')
  await save.click()
  await expect(save).toBeDisabled()
  await expect(region.getByRole('alert')).toHaveCount(0)
  expect((await vista.api('/user-agent')).value).toBe('RetainedClient/1')
})
