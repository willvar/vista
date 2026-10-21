import type { Locator, Page } from '@playwright/test'
import { test, expect } from './fixtures'
import { closeSettings } from './settings-navigation'

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

const eightLines = Array.from({ length: 8 }, (_, index) => `Draft line ${index + 1}`).join('\n')
const longTool = 'mcp_browser_read_document_with_full_metadata'

// Wait for the component library's entrance animation and textarea auto sizing.
async function settleLayout(page: Page) {
  await page.waitForTimeout(350)
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

async function expectControlReachable(control: Locator) {
  await expect(control).toBeVisible()
  await control.scrollIntoViewIfNeeded()
  const reachable = await control.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    return rect.width > 0 && rect.height > 0
      && rect.x >= -1 && rect.right <= innerWidth + 1
      && rect.y >= -1 && rect.bottom <= innerHeight + 1
      && !!hit && (hit === element || element.contains(hit))
  })
  expect(reachable, 'Control must fit within the viewport and remain uncovered').toBe(true)
}

async function expectNoConversationOverflow(page: Page) {
  const geometry = await page.evaluate(() => {
    const selectors = ['.messages', '.run-trace', '.antd-think-status-wrapper', '.antd-think-status-text',
      '.antd-thought-chain-node-box', '.antd-thought-chain-node-title', '.sender-card', '.sender-footer', '.composer-submit']
    const model = document.querySelector('.composer-model')?.getBoundingClientRect()
    return {
      pageWidth: document.documentElement.scrollWidth,
      pageHeight: document.documentElement.scrollHeight,
      viewportWidth: innerWidth,
      viewportHeight: innerHeight,
      modelWidth: model?.width ?? 0,
      overflowing: selectors.flatMap(selector => [...document.querySelectorAll(selector)]
        .filter(element => element.getBoundingClientRect().width > 0 && element.scrollWidth > element.clientWidth + 2)
        .map(element => ({ selector, width: element.clientWidth, scrollWidth: element.scrollWidth }))),
    }
  })
  expect(geometry.pageWidth, 'Page must not scroll horizontally').toBeLessThanOrEqual(geometry.viewportWidth + 2)
  expect(geometry.pageHeight, 'Short screens must scroll within the conversation').toBeLessThanOrEqual(geometry.viewportHeight + 2)
  expect(geometry.modelWidth, 'Model picker must remain usable').toBeGreaterThanOrEqual(96)
  expect(geometry.overflowing, 'Conversation components must not overflow horizontally').toEqual([])
}

async function expectModalLayout(page: Page, desktopWidth?: number) {
  const geometry = await page.locator('.modal-page:visible').evaluate(section => {
    const container = section.closest('.ant-modal-container')!
    const body = section.closest('.ant-modal-body')!
    const rect = (element: Element) => {
      const bounds = element.getBoundingClientRect()
      return { x: bounds.x, y: bounds.y, right: bounds.right, bottom: bounds.bottom, width: bounds.width, height: bounds.height }
    }
    return {
      viewportWidth: innerWidth,
      viewportHeight: innerHeight,
      pageWidth: document.documentElement.scrollWidth,
      container: rect(container),
      section: rect(section),
      body: rect(body),
      padding: getComputedStyle(container).padding,
      border: parseFloat(getComputedStyle(container).borderLeftWidth),
      navigationWidth: section.querySelector('.settings-nav')?.getBoundingClientRect().width ?? 0,
      settingsWidth: section.querySelector('.settings-content')?.getBoundingClientRect().width ?? 0,
      radius: getComputedStyle(container).borderRadius,
      horizontalOverflow: container.scrollWidth - container.clientWidth,
      verticalOverflow: container.scrollHeight - container.clientHeight,
      overflowingContent: [...section.querySelectorAll('.settings-nav,.settings-content,.session-manager-content,.agent-instructions-footer')]
        .filter(element => element.scrollWidth > element.clientWidth + 2).map(element => element.className),
    }
  })
  const { container, section, body, border } = geometry
  expect(geometry.padding, 'Library container padding must not create a second frame').toBe('0px')
  expect(Math.abs(section.x - container.x - border)).toBeLessThanOrEqual(1)
  expect(Math.abs(section.y - container.y - border)).toBeLessThanOrEqual(1)
  expect(Math.abs(container.right - section.right - border)).toBeLessThanOrEqual(1)
  expect(Math.abs(container.bottom - section.bottom - border)).toBeLessThanOrEqual(1)
  expect(Math.abs(body.width - section.width)).toBeLessThanOrEqual(1)
  expect(Math.abs(body.height - section.height)).toBeLessThanOrEqual(1)
  expect(container.x).toBeGreaterThanOrEqual(0)
  expect(container.y).toBeGreaterThanOrEqual(0)
  expect(container.right).toBeLessThanOrEqual(geometry.viewportWidth + 2)
  expect(container.bottom).toBeLessThanOrEqual(geometry.viewportHeight + 2)
  expect(Math.abs(container.x + container.width / 2 - geometry.viewportWidth / 2), 'Modal must be horizontally centered').toBeLessThanOrEqual(2)
  expect(Math.abs(container.y + container.height / 2 - geometry.viewportHeight / 2), 'Modal must be vertically centered').toBeLessThanOrEqual(2)
  expect(geometry.pageWidth).toBeLessThanOrEqual(geometry.viewportWidth + 2)
  expect(geometry.horizontalOverflow).toBeLessThanOrEqual(2)
  expect(geometry.verticalOverflow).toBeLessThanOrEqual(2)
  expect(geometry.overflowingContent).toEqual([])
  if (geometry.viewportWidth <= 830) {
    expect(container.x).toBe(0)
    expect(container.y).toBe(0)
    expect(container.width).toBe(geometry.viewportWidth)
    expect(container.height).toBe(geometry.viewportHeight)
    expect(border).toBe(0)
    expect(geometry.radius).toBe('0px')
    await expect(page.locator('.modal-page .modal-close')).toBeHidden()
    await expectControlReachable(page.locator('.modal-page .page-back'))
  }
  if (geometry.viewportWidth > 830 && geometry.navigationWidth) {
    expect(geometry.settingsWidth, 'The settings pane keeps its width as the navigation changes').toBe(626)
    expect(Math.abs(container.width - geometry.navigationWidth - geometry.settingsWidth - 2 * border), 'Settings modal width follows the navigation plus the fixed content pane').toBeLessThanOrEqual(1)
  }
  if (geometry.viewportWidth >= 1280 && desktopWidth !== undefined) {
    expect(Math.abs(container.width - desktopWidth), 'Modal width must follow its content layout, without viewport CSS overrides').toBeLessThanOrEqual(1)
  }
}

for (const modal of [
  { route: '/settings/general', name: 'general settings', close: 'Close settings' },
  { route: '/settings/personalization', name: 'personalization settings', close: 'Close settings' },
  { route: '/settings/models', name: 'provider settings', close: 'Close settings' },
  { route: '/settings/about', name: 'about settings', close: 'Close settings' },
  { route: '/sessions', name: 'session manager', close: 'Close session manager' },
]) {
  test(`${modal.name} adapts to the viewport after refresh`, async ({ page, vista }, testInfo) => {
    const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    await vista.api(`/sessions/${session.id}`, {
      method: 'PATCH', body: JSON.stringify({ title: 'Long modal sizing title '.repeat(8) }),
    })
    await page.goto(`${vista.origin}${modal.route}?session=${session.id}`)
    await expect(page.locator('.modal-page:visible')).toBeVisible()
    await page.reload()
    await expect(page.locator('.modal-page:visible')).toBeVisible()
    expect(new URL(page.url()).pathname).toBe(modal.route)
    await settleLayout(page)
    await expectModalLayout(page, modal.route === '/sessions' ? 960 : undefined)
    const screenshot = testInfo.outputPath('workspace-layout.png')
    await page.screenshot({ path: screenshot })
    await testInfo.attach('modal-layout', { path: screenshot, contentType: 'image/png' })

    const content = page.locator('.modal-page:visible .settings-content, .modal-page:visible .session-manager-content')
    const header = page.locator('.modal-page:visible > .conversation-header')
    const headerBefore = await header.boundingBox()
    await content.evaluate(element => { element.scrollTop = element.scrollHeight })
    expect(await header.boundingBox(), 'Header remains fixed while page content scrolls').toEqual(headerBefore)
    if (modal.route === '/settings/personalization') {
      for (const form of ['.agent-instructions-form', '.delegation-depth-form']) {
        await expectControlReachable(content.locator(form).getByRole('button', { name: 'Save changes', exact: true }))
      }
    }
    if (modal.route === '/settings/general') {
      await expectControlReachable(content.getByRole('combobox', { name: 'Model selection', exact: true }))
      if (page.viewportSize()!.width > 830) {
        const viewport = page.viewportSize()!
        const widths = () => page.locator('.settings-page').evaluate(section => ({
          modal: section.closest('.ant-modal-container')!.getBoundingClientRect().width,
          navigation: section.querySelector('.settings-nav')!.getBoundingClientRect().width,
          content: section.querySelector('.settings-content')!.getBoundingClientRect().width,
        }))
        const initial = await widths()
        const picker = page.locator('.language-setting .ant-select')
        for (const [language, locale] of [['French', 'fr'], ['zh-CN', 'zh-CN'], ['English', 'en']]) {
          await picker.scrollIntoViewIfNeeded()
          await picker.click()
          await picker.locator('input').fill(language!)
          await page.locator('.ant-select-dropdown:visible .ant-select-item-option').click()
          await expect(page.locator('html')).toHaveAttribute('lang', locale!)
          const current = await widths()
          expect(current.content, 'Changing the language must not squeeze the settings pane').toBe(initial.content)
          expect(Math.abs((current.modal - initial.modal) - (current.navigation - initial.navigation))).toBeLessThanOrEqual(1)
          await expectModalLayout(page)
          if (locale === 'fr') {
            expect(current.modal, 'Longer labels widen the modal').toBeGreaterThan(initial.modal)
            await page.screenshot({ path: testInfo.outputPath('french-settings-width.png') })
            for (const width of [1024, 831]) {
              await page.setViewportSize({ width, height: viewport.height })
              await expectModalLayout(page)
              expect((await widths()).content).toBe(initial.content)
              await page.screenshot({ path: testInfo.outputPath(`french-settings-${width}.png`) })
            }
            await page.setViewportSize(viewport)
          }
        }
      }
    }
    if (modal.route === '/sessions') {
      const section = page.locator('.modal-page:visible')
      const before = await section.boundingBox()
      await page.locator('.managed-session').getByRole('button', { name: 'Delete', exact: true }).click()
      const popup = page.locator('.session-delete-popover:visible')
      await expect(popup).toBeVisible()
      await settleLayout(page)
      const layer = await popup.evaluate(element => {
        const rect = element.getBoundingClientRect()
        return {
          insideModal: !!element.closest('.ant-modal'),
          zIndex: Number(getComputedStyle(element).zIndex),
          x: rect.x, y: rect.y, right: rect.right, bottom: rect.bottom,
          viewportWidth: innerWidth, viewportHeight: innerHeight,
        }
      })
      expect(layer.insideModal, 'Confirmation must render outside the parent modal').toBe(false)
      expect(layer.zIndex, 'Confirmation must be above the parent modal').toBeGreaterThan(1000)
      expect(layer.x).toBeGreaterThanOrEqual(0)
      expect(layer.y).toBeGreaterThanOrEqual(0)
      expect(layer.right).toBeLessThanOrEqual(layer.viewportWidth + 2)
      expect(layer.bottom).toBeLessThanOrEqual(layer.viewportHeight + 2)
      const after = await section.boundingBox()
      expect(before).not.toBeNull()
      expect(after).not.toBeNull()
      for (const dimension of ['x', 'y', 'width', 'height'] as const) {
        expect(Math.abs(after![dimension] - before![dimension]), 'Confirmation must not change the parent modal bounds').toBeLessThanOrEqual(1)
      }
      await popup.getByRole('button', { name: 'Cancel', exact: true }).click()
      await expect(popup).toBeHidden()
      await page.locator('.managed-session').getByRole('button', { name: 'Delete', exact: true }).click()
      await expect(popup).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(popup).toBeHidden()
      await expect(section).toBeVisible()
      expect((await vista.api('/sessions')).some((row: { id: string }) => row.id === session.id)).toBe(true)
    }
    if (modal.route.startsWith('/settings/')) await closeSettings(page)
    else await page.getByRole('button', { name: page.viewportSize()!.width <= 830 ? 'Back to conversation' : modal.close, exact: true }).click()
    await expect(page.locator('.modal-page:visible')).toBeHidden()
    expect(new URL(page.url()).pathname).toBe(`/sessions/${session.id}`)
  })
}

test('empty and eight-line drafts keep Send and model selection reachable', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const forbidden: string[] = []
  await page.route('**/api/sessions/**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'POST' && /\/(messages|cancel)$/.test(path)) {
      forbidden.push(path)
      return route.abort()
    }
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const textarea = page.locator('.sender-card textarea')
  await expect(textarea).toBeEnabled()
  await settleLayout(page)
  const idleInputHeight = await page.locator('.sender-card').evaluate(element => element.getBoundingClientRect().height)
  if (page.viewportSize()!.height <= 520) expect(idleInputHeight, 'Idle input area must preserve space for conversation history').toBeLessThanOrEqual(90)
  await testInfo.attach('idle-sender-layout', { body: await page.screenshot(), contentType: 'image/png' })
  for (const draft of ['', eightLines]) {
    await test.step(draft ? 'Eight-line draft' : 'Empty draft', async () => {
      await textarea.fill(draft)
      await settleLayout(page)
      const send = page.getByRole('button', { name: 'Send', exact: true })
      await expectControlReachable(send)
      if (draft) await expect(send).toBeEnabled()
      else await expect(send).toBeDisabled()
      await expectNoConversationOverflow(page)
      await testInfo.attach(draft ? 'eight-line-sender-layout' : 'focused-empty-sender-layout', { body: await page.screenshot(), contentType: 'image/png' })
    })
  }
  expect(forbidden, 'Layout checks must not submit a message or cancel execution').toEqual([])
})

test('long running tool and completed process keep Stop and Queue reachable', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const completedRun = 'layout-completed-run'
  const activeRun = 'layout-active-run'
  const createdAt = new Date().toISOString()
  const user = { id: 'layout-first-user', sequence: 1, role: 'user', content: 'Layout fixture', runId: completedRun, createdAt }
  const assistant = { id: 'layout-reply', sequence: 6, role: 'assistant', content: 'Fixture reply.', runId: completedRun, createdAt, outputTurn: 2, provider: 'openai-chat', modelId: vista.modelId, modelKey: 'test' }
  const currentUser = { id: 'layout-current-user', sequence: 8, role: 'user', content: 'UI fixture; no model request', runId: activeRun, createdAt }
  const modelResult = { provider: 'openai-chat', id: vista.modelId, finishReason: 'completed', durationMs: 250, usage: { inputTokens: 50, outputTokens: 10, cacheReadTokens: 0, reasoningTokens: 0 } }
  const events = [
    { sequence: 1, kind: 'message', data: user },
    { sequence: 2, kind: 'tool', data: { runId: completedRun, status: 'started', tool: { callId: 'completed-call', name: longTool, summary: 'UI geometry only' } } },
    { sequence: 3, kind: 'tool', data: { runId: completedRun, status: 'succeeded', content: 'Fixture result', tool: { callId: 'completed-call', name: longTool, summary: 'UI geometry only' } } },
    { sequence: 4, kind: 'model_result', data: { runId: completedRun, turn: 1, modelResult } },
    { sequence: 5, kind: 'model_result', data: { runId: completedRun, turn: 2, modelResult } },
    { sequence: 6, kind: 'message', data: assistant },
    { sequence: 7, kind: 'run_finished', data: { runId: completedRun, status: 'succeeded', message: assistant } },
    { sequence: 8, kind: 'message', data: currentUser },
    { sequence: 9, kind: 'model_result', data: { runId: activeRun, turn: 1, modelResult } },
    { sequence: 10, kind: 'tool', data: { runId: activeRun, status: 'started', tool: { callId: 'active-call', name: longTool, summary: 'UI geometry only' } } },
  ]
  const forbidden: string[] = []
  await page.route('**/api/sessions**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'POST' && /\/(messages|cancel)$/.test(path)) {
      forbidden.push(path)
      return route.abort()
    }
    if (request.method() !== 'GET') return route.continue()
    if (path === '/api/sessions') {
      const original = await route.fetch()
      const rows = await original.json()
      return route.fulfill({ response: original, json: rows.map((row: { id: string }) => row.id === session.id ? { ...row, executing: true } : row) })
    }
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: [user, assistant, currentUser] })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: events })
    if (path === `/api/sessions/${session.id}/events`) return route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
    if (path.includes('/runs/') && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    if (path === `/api/sessions/${session.id}/runs/${completedRun}/overview`) return route.fulfill({ json: { modelCalls: 2, throughSequence: 7, startedAt: createdAt, finishedAt: createdAt, events: events.filter(event => event.data.runId === completedRun) } })
    // An active overview has no through watermark; supplying one would make
    // the client treat the live segment as completed and hide its activity.
    if (path === `/api/sessions/${session.id}/runs/${activeRun}/overview`) return route.fulfill({ json: { modelCalls: 1, startedAt: createdAt, events: events.filter(event => event.data.runId === activeRun) } })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const activeTrace = page.locator(`.run-trace[data-run-id="${activeRun}"]`)
  const completedTrace = page.locator(`.run-trace[data-run-id="${completedRun}"]`)
  await expect(activeTrace.locator('.trace-last-activity')).toContainText('Last activity: Use tool · UI geometry only')
  await expect(activeTrace.locator('.antd-thought-chain-node-title')).toBeHidden()
  await expect(completedTrace.getByText('2 model calls', { exact: true })).toBeVisible()
  await completedTrace.locator('.antd-think-status-wrapper').first().click()
  await expect(completedTrace.locator('.antd-thought-chain-node-title')).toHaveText('Use tool · UI geometry only')
  await activeTrace.locator('.antd-think-status-wrapper').first().click()
  await expect(activeTrace.locator('.antd-thought-chain-node-title')).toHaveText('Use tool · UI geometry only')

  const textarea = page.locator('.sender-card textarea')
  for (const draft of ['', eightLines]) {
    await test.step(draft ? 'Draft during tool execution' : 'Empty composer during tool execution', async () => {
      await textarea.fill(draft)
      await settleLayout(page)
      const queue = page.getByRole('button', { name: 'Queue', exact: true })
      if (draft) {
        await expectControlReachable(queue)
        await expect(queue).toBeEnabled()
      } else await expect(queue).toBeHidden()
      const button = page.getByRole('button', { name: draft ? 'Send' : 'Stop current execution', exact: true })
      await expectControlReachable(button)
      await expect(button).toBeEnabled()
      await expectNoConversationOverflow(page)
      await testInfo.attach(draft ? 'running-draft-sender-layout' : 'running-empty-sender-layout', { body: await page.screenshot(), contentType: 'image/png' })
    })
  }
  expect(forbidden, 'Layout checks must not submit a message or cancel execution').toEqual([])
})
