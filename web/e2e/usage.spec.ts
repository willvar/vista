import { coreSnapshot, installConversationStream } from './conversation-fixture'
import { scrollToConversationEntry } from './conversation-scroll'
import { test, expect } from './fixtures'

test.beforeEach(async ({ vista }) => {
})

test('cache usage distinguishes unavailable, measured zero and writes @responsive', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const usage = { inputTokens: 120, outputTokens: 10, cacheReadTokens: 0, reasoningTokens: 0 }
  const scenarios = [
    { id: 'unknown', title: 'Cache telemetry unavailable', usages: [usage], read: '—', write: undefined },
    { id: 'known', title: 'Cache telemetry measured', usages: [
      { ...usage, cacheReadKnown: true, cacheWriteTokens: 0 },
      { ...usage, cacheReadTokens: 80, cacheReadKnown: true, cacheWriteTokens: 20 },
    ], read: '80', write: '20' },
    { id: 'mixed', title: 'Cache telemetry partially available', usages: [
      usage, { ...usage, cacheReadKnown: true, cacheWriteTokens: 0 },
    ], read: '—', write: '—' },
  ]
  const messages = scenarios.map((scenario, index) => ({
    id: scenario.id, sequence: 25 + index * 10, role: 'assistant', content: scenario.title,
    runId: scenario.id, createdAt: new Date().toISOString(), outputTurn: scenario.usages.length,
    provider: 'openai-chat', modelId: vista.modelId, modelKey: 'test',
    modelResults: scenario.usages.map((reported, turn) => ({
      provider: 'openai-chat', id: vista.modelId, depth: 0, runId: scenario.id,
      sequence: 20 + index * 10 + turn, turn: turn + 1, finishReason: 'completed', usage: reported,
    })),
  }))
  const snapshot = coreSnapshot(session, messages)
  await installConversationStream(page, snapshot)
  await page.route('**/api/sessions/**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() !== 'GET') return route.continue()
    if (path === `/api/sessions/${session.id}/snapshot`) return route.fulfill({ json: snapshot })
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: messages })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: [] })
    if (path.includes('/runs/') && path.endsWith('/overview')) {
      const message = messages.find(message => message.runId === path.split('/')[5])!
      return route.fulfill({ json: { modelCalls: message.modelResults.length, firstSequence: message.modelResults[0]!.sequence,
        throughSequence: message.sequence, startedAt: message.createdAt, finishedAt: message.createdAt, events: [] } })
    }
    if (path.includes('/runs/') && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  for (const scenario of scenarios) {
    const reply = page.locator('.messages article.assistant').filter({ hasText: scenario.title })
    await expect(reply.locator('.token-usage')).toContainText(`cached ${scenario.read}`)
    const trigger = reply.getByRole('button', { name: 'Model usage details', exact: true })
    await trigger.scrollIntoViewIfNeeded()
    await trigger.click()
    const details = page.locator('.usage-popover:visible .usage-details')
    const header = details.getByRole('columnheader', { name: 'Cache writes', exact: true })
    if (scenario.write === undefined) await expect(header).toHaveCount(0)
    else {
      await expect(header).toBeVisible()
      await expect(details.locator('.usage-summary dl > div').filter({ has: page.locator('dt', { hasText: /^Cache writes$/ }) }).locator('dd')).toHaveText(scenario.write)
    }
    for (const [index, reported] of scenario.usages.entries()) {
      const cells = details.locator(`tbody tr[data-turn="${index + 1}"] td`)
      await expect(cells.nth(4)).toHaveText('cacheReadKnown' in reported ? String(reported.cacheReadTokens) : '—')
      if (scenario.write !== undefined) await expect(cells.nth(5)).toHaveText('cacheWriteTokens' in reported ? String(reported.cacheWriteTokens) : '—')
    }
    await page.waitForTimeout(350)
    const bounds = await details.evaluate(element => {
      const panel = element.getBoundingClientRect()
      return { left: panel.left, right: panel.right, top: panel.top, bottom: panel.bottom,
        width: innerWidth, height: innerHeight, overflow: element.scrollHeight - element.clientHeight }
    })
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(bounds.width + 2)
    expect(bounds.top).toBeGreaterThanOrEqual(0)
    expect(bounds.bottom).toBeLessThanOrEqual(bounds.height + 2)
    expect(bounds.overflow).toBeLessThanOrEqual(2)
    await page.keyboard.press('Escape')
    await expect(details).toHaveCount(0)
  }
})

test('message actions and usage retain their footer alignment and spacing', async ({ page, vista }, testInfo) => {
  await page.goto(vista.origin)
  const sender = page.locator('.sender-card')
  await sender.locator('textarea').fill('Check the message footer')
  await sender.getByRole('button', { name: 'Send', exact: true }).click()
  const reply = page.locator('.messages article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.model-call-count')).toHaveText('· 1 model call')
  const geometry = await reply.locator('.reply-footer').evaluate(footer => {
    const actions = footer.querySelector('.message-actions')!
    const usage = footer.querySelector('.message-model-results')!
    const a = actions.getBoundingClientRect()
    const u = usage.getBoundingClientRect()
    const buttons = [...actions.querySelectorAll(':scope > button, :scope > span')]
      .map(element => element.getBoundingClientRect()).filter(rect => rect.width > 0)
    return { width: innerWidth, gap: u.left - a.right, left: u.left - a.left, aligned: Math.abs(a.top + a.height / 2 - u.top - u.height / 2),
      usageTop: u.top, actionsBottom: a.bottom, actionGap: getComputedStyle(actions).gap,
      overlaps: buttons.some((rect, index) => index > 0 && rect.left < buttons[index - 1]!.right - 1) }
  })
  expect(geometry.actionGap).toBe('8px')
  expect(geometry.overlaps, 'Action buttons must not overlap').toBe(false)
  if (geometry.width > 830 || geometry.usageTop < geometry.actionsBottom) {
    expect(geometry.gap, 'Usage follows the actions instead of being pushed to the far edge').toBeCloseTo(12, 0)
    expect(geometry.aligned).toBeLessThanOrEqual(1)
  } else {
    expect(geometry.usageTop).toBeGreaterThanOrEqual(geometry.actionsBottom)
    expect(geometry.left).toBeCloseTo(0, 0)
  }
  const user = page.locator('.messages article.user').last()
  const userActions = user.locator('.message-actions')
  const copy = userActions.getByRole('button', { name: 'Copy reply', exact: true })
  if (testInfo.project.use.hasTouch) {
    // The user's footer can be above the latest reply on a short screen.
    await userActions.scrollIntoViewIfNeeded()
    await expect(userActions).toHaveCSS('opacity', '1') // Not merely in the DOM before the first tap.
    await expect(userActions.getByRole('button', { name: 'Edit message', exact: true })).toBeInViewport()
    await copy.tap()
    await expect(copy).toContainText('Copied')
  } else {
    await page.mouse.move(1, 1)
    await expect(userActions).toHaveCSS('opacity', '0')
    await user.hover()
    await expect(userActions).toHaveCSS('opacity', '1')
  }
  const alignment = await user.evaluate(article => {
    const content = article.querySelector('.ui-bubble-content')!.getBoundingClientRect()
    const actions = article.querySelector('.message-actions')!.getBoundingClientRect()
    return Math.abs(content.right - actions.right)
  })
  expect(alignment, 'User actions align with the right edge of the user message').toBeLessThanOrEqual(1)
  await page.screenshot({ path: testInfo.outputPath('message-footer.png'), animations: 'disabled' })
})

test('measured TPS and TTFT match persisted model timing and survive refresh @responsive', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const sender = page.locator('.sender-card')
  await expect(sender.getByPlaceholder('Send a message')).toBeEnabled()
  await sender.getByPlaceholder('Send a message').fill('Hi')
  const send = sender.getByRole('button', { name: 'Send', exact: true })
  await send.scrollIntoViewIfNeeded()
  await expect(send).toBeEnabled()
  await send.click()
  const reply = page.locator('.messages article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.model-tps')).toHaveText(/TPS \d+\.\d/)
  await expect(reply.locator('.model-call-count')).toHaveText('· 1 model call')
  await expect(reply.locator('.model-call-count svg')).toHaveCount(0)
  const messages = await vista.api(`/sessions/${session.id}/messages`)
  const result = messages.find((message: { role: string }) => message.role === 'assistant')?.modelResults?.[0]
  expect(result?.durationMs, 'New model results must retain their measured request time').toBeGreaterThan(0)
  expect(result?.usage?.outputTokens).toBe(5)
  expect(result?.firstTokenMs).toBeGreaterThan(0)
  expect(result.firstTokenMs).toBeLessThanOrEqual(result.durationMs)
  const expectedTTFT = ` · TTFT ${result.firstTokenMs < 1000 ? `${Math.round(result.firstTokenMs)}ms` : `${(result.firstTokenMs / 1000).toFixed(2)}s`}`
  await expect(reply.locator('.model-ttft')).toHaveText(expectedTTFT)
  await expect(reply.locator('.model-ttft')).toHaveAttribute('title', /first text or visible reasoning/)
  const expected = ` · TPS ${(result.usage.outputTokens * 1000 / result.durationMs).toFixed(1)}`
  await expect(reply.locator('.model-tps')).toHaveText(expected)
  await expect(reply.locator('.model-tps')).toHaveAttribute('title', /including time to first token and excluding tool execution/)
  await page.reload()
  await expect(reply.locator('.model-tps')).toHaveText(expected)
  await expect(reply.locator('.model-ttft')).toHaveText(expectedTTFT)
})

test('missing or partial timing remains visible without invented TPS or TTFT @responsive', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const usage = { inputTokens: 20, outputTokens: 10, cacheReadTokens: 0, reasoningTokens: 0 }
  const baseResult = { provider: 'openai-chat', id: vista.modelId, depth: 0, finishReason: 'completed' }
  const createdAt = new Date().toISOString()
  const scenarios = [
    { id: 'historical', title: 'Historical timing fixture', ttft: '—', results: [{ ...baseResult, sequence: 1, turn: 1, usage }] },
    { id: 'mixed', title: 'Partial timing fixture', ttft: '125ms', results: [{ ...baseResult, sequence: 2, turn: 1, usage }, { ...baseResult, sequence: 3, turn: 2, usage, durationMs: 400, firstTokenMs: 125 }] },
    { id: 'missing-usage', title: 'Missing usage fixture', ttft: '1.50s', results: [{ ...baseResult, sequence: 4, turn: 1, durationMs: 2500, firstTokenMs: 1500 }] },
    { id: 'measured', title: 'Measured timing fixture', ttft: '0ms', results: [{ ...baseResult, sequence: 5, turn: 1, usage, durationMs: 400, firstTokenMs: 0 }] },
    { id: 'multi', title: 'Independent request timing fixture', ttft: '250ms', results: [
      { ...baseResult, sequence: 6, turn: 1, depth: 1, taskId: 'main/child', firstTokenMs: 1 },
      { ...baseResult, sequence: 8, turn: 3, firstTokenMs: 1200 },
      { ...baseResult, sequence: 7, turn: 2, firstTokenMs: 250 },
    ] },
    { id: 'invalid', title: 'Invalid timing fixture', ttft: '—', results: [{ ...baseResult, sequence: 9, turn: 1, firstTokenMs: -1 }] },
    { id: 'child-only', title: 'Child timing is not main timing fixture', ttft: '—', results: [
      { ...baseResult, sequence: 10, turn: 1 },
      { ...baseResult, sequence: 11, turn: 2, depth: 1, firstTokenMs: 80 },
    ] },
  ]
  const messages = scenarios.map((scenario, index) => ({
    id: scenario.id, sequence: 10 + index, role: 'assistant', content: scenario.title,
    runId: scenario.id, createdAt, outputTurn: scenario.results.length,
    provider: 'openai-chat', modelId: vista.modelId, modelKey: 'test',
    modelResults: scenario.results.map(result => ({ ...result, runId: scenario.id })),
  }))
  await installConversationStream(page, coreSnapshot(session, messages))
  await page.route('**/api/sessions/**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() !== 'GET') return route.continue()
    if (path === `/api/sessions/${session.id}/snapshot`) return route.fulfill({ json: coreSnapshot(session, messages) })
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: messages })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: [] })
    if (path.includes('/runs/') && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  for (const [index, scenario] of scenarios.entries()) {
    const reply = await scrollToConversationEntry(page, `[data-display-key="${scenario.id}"] article.assistant`, messages[index]!.sequence)
    await expect(reply.locator('.model-ttft')).toHaveText(` · TTFT ${scenario.ttft}`)
    await expect(reply.locator('.model-tps')).toHaveText(scenario.id === 'measured' ? ' · TPS 25.0' : ' · TPS —')
    await expect(reply.locator('.model-tps')).toHaveAttribute('title', scenario.id === 'measured'
      ? /including time to first token and excluding tool execution/
      : /Request timing or token usage is missing/)
    await expect(reply.locator('.model-call-count')).toHaveText(`· ${scenario.results.length} model call${scenario.results.length === 1 ? '' : 's'}`)
  }
  const mixed = await scrollToConversationEntry(page, '[data-display-key="mixed"] article.assistant', messages.find(message => message.id === 'mixed')!.sequence)
  await mixed.getByRole('button', { name: 'Model usage details', exact: true }).click()
  const details = page.locator('.usage-popover:visible .usage-details')
  await expect(details.getByRole('columnheader', { name: 'TTFT', exact: true })).toBeVisible()
  await expect(details.locator('tbody tr[data-turn="1"] td').nth(6)).toHaveText('—')
  await expect(details.locator('tbody tr[data-turn="2"] td').nth(6)).toHaveText('125ms')
  await expect(details.locator('.usage-summary')).toContainText('TTFT 125ms')
  await expect(details.locator('thead th').last()).toHaveText('TPS')
  await expect(details.locator('tbody tr[data-turn="1"] td').last()).toHaveText('—')
  await expect(details.locator('tbody tr[data-turn="2"] td').last()).toHaveText('25.0')
  await expect(details.locator('.usage-summary')).toContainText('TPS —')
  await page.waitForTimeout(350)
  const geometry = await details.evaluate(element => {
    const bounds = element.getBoundingClientRect()
    const table = element.querySelector('.usage-table-scroll')!
    return { x: bounds.x, y: bounds.y, right: bounds.right, bottom: bounds.bottom,
      width: innerWidth, height: innerHeight, overflow: element.scrollHeight - element.clientHeight,
      tableWidth: table.clientWidth, tableScrollWidth: table.scrollWidth }
  })
  expect(geometry.x).toBeGreaterThanOrEqual(0)
  expect(geometry.y).toBeGreaterThanOrEqual(0)
  expect(geometry.right).toBeLessThanOrEqual(geometry.width + 2)
  expect(geometry.bottom).toBeLessThanOrEqual(geometry.height + 2)
  expect(geometry.overflow, 'Usage table must scroll within the bounded panel').toBeLessThanOrEqual(2)
  if (geometry.width < 800) expect(geometry.tableScrollWidth).toBeGreaterThan(geometry.tableWidth)
  await testInfo.attach('usage-table-layout', { body: await page.screenshot(), contentType: 'image/png' })
  await expect(page.locator('.messages')).not.toContainText(/(?:TPS|TTFT) (?:NaN|Infinity)/)
  await details.getByLabel('Filter model calls', { exact: true }).click()
  await page.locator('.ui-select-dropdown:visible').getByText('Failures', { exact: true }).click()
  const empty = details.locator('.ui-empty-description')
  await expect(empty).toHaveText('No matching model calls')
  await expect(details.locator('.usage-pagination')).toHaveCount(0)
  const emptyLayout = await details.evaluate(element => {
    const panel = element.getBoundingClientRect()
    const description = element.querySelector('.ui-empty-description')!.getBoundingClientRect()
    return { clipped: description.bottom - panel.bottom, overflow: element.scrollHeight - element.clientHeight }
  })
  expect(emptyLayout.clipped, 'Empty description must fit inside the usage panel').toBeLessThanOrEqual(2)
  expect(emptyLayout.overflow, 'Empty usage panel must not hide overflowing content').toBeLessThanOrEqual(2)
  await page.screenshot({ path: testInfo.outputPath('usage-empty.png') })
})
