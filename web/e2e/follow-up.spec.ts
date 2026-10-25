import { coreSnapshot } from './conversation-fixture'
import { closeSettings } from './settings-navigation'
import type { Page } from '@playwright/test'
import type { Message } from '../src/client/types'
import { test, expect } from './fixtures'

type Submission = { content: string; mode: 'now' | 'later' }
type FixtureAPI = {
  origin: string
  modelId: string
  api: (path: string, options?: RequestInit) => Promise<any>
}

async function mockConversation(page: Page, vista: FixtureAPI, options: { cancelFailures?: number; compactionStatus?: boolean; messages?: Pick<Message, 'role' | 'content'>[] } = {}) {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const runId = 'follow-up-fixture-run'
  const user = {
    id: 'follow-up-fixture-user', runId, sequence: 1, role: 'user',
    content: 'Controlled follow-up fixture', createdAt: new Date().toISOString(),
  }
  const messages = options.messages?.map((message, index) => ({ ...user, ...message, id: `recall-message-${index}`, sequence: index + 1 })) || [user]
  const modelRequest = {
    sequence: 2, kind: 'model_request',
    data: { runId, turn: 1, provider: 'openai-chat', id: vista.modelId, key: 'test' },
  }
  const sent: Submission[] = []
  let executing = true
  let cancelRequests = 0
  let cancelFailures = options.cancelFailures || 0
  const cancelPaths: string[] = []

  await page.route('**/api/sessions**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'POST' && path === `/api/sessions/${session.id}/messages`) {
      const input = request.postDataJSON() as Submission
      sent.push(input)
      if (executing) return route.fulfill({ json: {
        input: { id: `follow-up-input-${sent.length}`, mode: input.mode, status: 'queued', content: input.content },
      } })
      return route.fulfill({ json: {
        message: { ...user, id: `idle-input-${sent.length}`, sequence: sent.length + 3, content: input.content },
      } })
    }
    if (request.method() === 'POST' && path === `/api/sessions/${session.id}/cancel`) {
      cancelRequests++
      cancelPaths.push(path)
      if (cancelFailures > 0) { cancelFailures--; return route.fulfill({ status: 503, body: 'Synthetic cancel failure' }) }
      executing = false
      return route.fulfill({ status: 204, body: '' })
    }
    // Keep every model submission local to this mock, including unexpected ones.
    if (request.method() === 'POST' && /\/(messages|cancel)$/.test(path)) {
      if (path.endsWith('/cancel')) cancelPaths.push(path)
      return route.abort()
    }
    if (request.method() !== 'GET') return route.continue()
    if (path === '/api/sessions') {
      const original = await route.fetch()
      return route.fulfill({ response: original, json: (await original.json()).map((row: { id: string }) =>
        row.id === session.id ? { ...row, executing } : row) })
    }
    if (path === `/api/sessions/${session.id}/snapshot`) return route.fulfill({ json: coreSnapshot({ ...session, executing }, messages, [modelRequest]) })
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: messages })
    if (path.includes('/runs/') && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    if (options.compactionStatus && path === `/api/sessions/${session.id}/context`) return route.fulfill({ json: { runId, manual: false, phase: 'compacted', sequence: 2 } })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: [modelRequest] })
    if (path === `/api/sessions/${session.id}/events`) return route.fulfill({ contentType: 'text/event-stream', body: executing ? '' :
      `data: ${JSON.stringify({ sequence: 3, kind: 'run_finished', data: { runId, status: 'canceled' } })}\n\n` })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const invitation = page.getByRole('button', { name: 'Skip for now', exact: true })
  if (await invitation.isVisible()) await invitation.click()
  await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeEnabled()
  return { session, sent, cancelPaths, cancellations: () => cancelRequests, setExecuting: (value: boolean) => { executing = value } }
}

async function assertSend(
  page: Page, sent: Submission[], content: string, mode: Submission['mode'], action: 'click' | 'Enter' | 'Control+Enter' | 'alternate',
) {
  const previous = sent.length
  const textarea = page.locator('.sender-card textarea')
  await textarea.fill(content)
  const send = page.getByRole('button', { name: 'Send', exact: true })
  await expect(send).toBeEnabled()
  if (action === 'click') await send.click()
  else if (action === 'alternate') await page.locator('.alternate-delivery').click()
  else await textarea.press(action)
  await expect.poll(() => sent.length).toBe(previous + 1)
  expect(sent.at(-1)).toMatchObject({ content, mode })
  await expect(textarea).toHaveValue('')
  await expect(page.locator('.error')).toHaveCount(0)
}

test('running sends default to Steer, with Queue as the one-message override', async ({ page, vista }) => {
  const fixture = await mockConversation(page, vista)
  const textarea = page.locator('.sender-card textarea')
  await textarea.fill('First line')
  await textarea.press('End')
  await textarea.press('Shift+Enter')
  await textarea.pressSequentially('Second line')
  await expect(textarea).toHaveValue('First line\nSecond line')
  expect(fixture.sent).toHaveLength(0)

  const send = page.getByRole('button', { name: 'Send', exact: true })
  await expect(send).toHaveAttribute('title', /steer/i)
  await expect(page.locator('.alternate-delivery')).toHaveText('Queue')
  await expect(page.locator('.alternate-delivery')).toHaveAttribute('title', /Ctrl\/Cmd\+Enter/i)
  await assertSend(page, fixture.sent, 'Default Send steers', 'now', 'click')
  await assertSend(page, fixture.sent, 'Default Enter steers', 'now', 'Enter')
  await assertSend(page, fixture.sent, 'Keyboard queues this message', 'later', 'Control+Enter')
  await assertSend(page, fixture.sent, 'Alternate button queues this message', 'later', 'alternate')
  expect(fixture.cancellations()).toBe(0)
})

test('sending from older history returns the conversation to the latest position', async ({ page, vista }) => {
  const history = Array.from({ length: 40 }, (_, index) => ({
    role: index % 2 ? 'assistant' : 'user',
    content: `History entry ${index + 1}. `.repeat(4),
  }))
  const fixture = await mockConversation(page, vista, { messages: history })
  const scroller = page.locator('.messages')
  const distance = () => scroller.evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight)
  const scrollable = () => scroller.evaluate(element => element.scrollHeight - element.clientHeight)
  await expect.poll(() => scrollable()).toBeGreaterThan(0)
  await scroller.evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
  await expect.poll(() => scroller.evaluate(element => element.scrollTop)).toBe(0)

  await page.locator('.sender-card textarea').fill('Return to the newest message')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect.poll(() => distance()).toBeLessThanOrEqual(3)
  expect(fixture.sent.at(-1)).toMatchObject({ content: 'Return to the newest message', mode: 'now' })
})

test('stale Queue preferences cannot change Send or Enter after refresh', async ({ page, vista }) => {
  const fixture = await mockConversation(page, vista)
  await page.evaluate(() => {
    localStorage.setItem('vista-follow-up-behavior', 'queue')
    document.cookie = 'vista-follow-up-behavior=queue; Path=/; SameSite=Strict'
  })
  const preferenceWrites: Record<string, unknown>[] = []
  await page.route('**/api/preferences', async route => {
    if (route.request().method() === 'GET') {
      const response = await route.fetch()
      return route.fulfill({ response, json: { ...await response.json(), followUpBehavior: 'queue' } })
    }
    preferenceWrites.push(route.request().postDataJSON())
    return route.continue()
  })
  await page.reload()
  await page.goto(`${vista.origin}/settings/general?session=${fixture.session.id}`)
  await expect(page.getByRole('combobox', { name: 'Follow-up behavior', exact: true })).toHaveCount(0)
  await closeSettings(page)
  await assertSend(page, fixture.sent, 'Send still inserts', 'now', 'click')
  await assertSend(page, fixture.sent, 'Enter still inserts', 'now', 'Enter')
  await assertSend(page, fixture.sent, 'Explicit Queue still queues', 'later', 'alternate')
  await assertSend(page, fixture.sent, 'Shortcut still queues', 'later', 'Control+Enter')
  expect(preferenceWrites.every(patch => !('followUpBehavior' in patch))).toBe(true)
  expect(fixture.cancellations()).toBe(0)
})

test('empty running composer only stops, and idle shortcuts start immediately', async ({ page, vista }) => {
  const fixture = await mockConversation(page, vista)
  const textarea = page.locator('.sender-card textarea')
  await expect(textarea).toHaveValue('')
  await expect(page.locator('.alternate-delivery')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toHaveCount(0)
  const stop = page.getByRole('button', { name: 'Stop current execution', exact: true })
  await expect(stop).toBeEnabled()
  await stop.click()
  await expect.poll(fixture.cancellations).toBe(1)
  await expect(stop).toHaveCount(0)
  expect(fixture.sent).toHaveLength(0)
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()

  await textarea.fill('Idle line one')
  await textarea.press('End')
  await textarea.press('Shift+Enter')
  await textarea.pressSequentially('Idle line two')
  await expect(textarea).toHaveValue('Idle line one\nIdle line two')
  expect(fixture.sent).toHaveLength(0)
  await expect(page.locator('.alternate-delivery')).toHaveCount(0)
  await assertSend(page, fixture.sent, 'Idle Send starts immediately', 'now', 'click')
  await assertSend(page, fixture.sent, 'Idle Enter starts immediately', 'now', 'Enter')
  await assertSend(page, fixture.sent, 'Idle Ctrl+Enter starts immediately', 'now', 'Control+Enter')
  await expect(page.locator('.alternate-delivery')).toHaveCount(0)
  expect(fixture.cancellations()).toBe(1)
})


test('Escape stops only the current running session, preserves drafts, and ignores idle, repeated, or IME keys', async ({ page, vista }) => {
  const fixture = await mockConversation(page, vista)
  const textarea = page.locator('.sender-card textarea')
  const original = page.locator('.messages article.user .markdown')
  for (const draft of ['', 'Keep this unsent draft\n第二行仍然保留']) {
    if (fixture.cancellations()) {
      fixture.setExecuting(true)
      await page.reload()
      await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeEnabled()
    }
    await textarea.fill(draft)
    await textarea.focus()
    const before = fixture.cancellations()
    await textarea.dispatchEvent('keydown', { key: 'Escape', code: 'Escape', repeat: true })
    await textarea.dispatchEvent('compositionstart')
    await textarea.press('Escape')
    await textarea.dispatchEvent('compositionend')
    await textarea.press('Escape')
    await textarea.dispatchEvent('keydown', { key: 'Escape', code: 'Escape', isComposing: true })
    await textarea.dispatchEvent('keydown', { key: 'Escape', code: 'Escape', keyCode: 229, which: 229 })
    await page.waitForTimeout(250)
    expect(fixture.cancellations(), 'Repeated or IME Escape must not cancel a task').toBe(before)
    await textarea.press('Escape')
    await expect.poll(fixture.cancellations).toBe(before + 1)
    await expect(page.locator('.alternate-delivery')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toHaveCount(0)
    await expect(textarea).toHaveValue(draft)
    await expect(original).toHaveText('Controlled follow-up fixture')
    await textarea.press('Escape')
    await page.waitForTimeout(100)
    expect(fixture.cancellations(), 'Idle Escape must not create another cancellation').toBe(before + 1)
  }
  expect(fixture.cancelPaths).toEqual(Array(2).fill(`/api/sessions/${fixture.session.id}/cancel`))
  expect(fixture.sent).toHaveLength(0)
})

test('Escape dismisses a dropdown, settings, context popup, or title editor before stopping execution', async ({ page, vista }) => {
  test.setTimeout(60_000)
  const fixture = await mockConversation(page, vista, { compactionStatus: true })
  const textarea = page.locator('.sender-card textarea')
  const header = page.locator('.workspace:not(.modal-page) > .conversation-header')
  for (const overlay of ['model', 'settings', 'context', 'title'] as const) {
    if (fixture.cancellations()) {
      fixture.setExecuting(true)
      await page.reload()
      await expect(page.locator('.alternate-delivery')).toBeVisible()
    }
    const draft = `Preserve draft while closing ${overlay}`
    await textarea.fill(draft)
    const before = fixture.cancellations()
    if (overlay === 'model') {
      await page.locator('.composer-model').click()
      await expect(page.locator('.ant-select-dropdown:visible')).toHaveCount(1)
    } else if (overlay === 'settings') {
      const mobileDrawer = page.locator('.mobile-sessions-trigger:visible')
      if (await mobileDrawer.count()) {
        await mobileDrawer.click()
        await expect(page.locator('.sessions-panel')).toHaveClass(/mobile-open/)
      }
      await page.getByRole('button', { name: 'Settings', exact: true }).click()
      await expect(page.locator('.settings-page')).toBeVisible()
    } else if (overlay === 'context') {
      await page.locator('.sender-card .context-usage').click()
      await expect(page.locator('.context-usage-hint:visible')).toHaveCount(1)
    } else {
      await header.getByRole('button', { name: 'Rename', exact: true }).click()
      await header.getByRole('textbox', { name: 'Session title', exact: true }).fill('Do not save this title')
    }
    await page.keyboard.press('Escape')
    if (overlay === 'model') await expect(page.locator('.ant-select-dropdown:visible')).toHaveCount(0)
    else if (overlay === 'settings') await expect(page.locator('.settings-page')).toBeHidden()
    else if (overlay === 'context') await expect(page.locator('.context-usage-hint:visible')).toHaveCount(0)
    else await expect(header.locator('.session-rename')).toHaveCount(0)
    expect(fixture.cancellations(), `First Escape closes ${overlay} without stopping the run`).toBe(before)
    await expect(textarea).toHaveValue(draft)
    await textarea.focus()
    await page.keyboard.press('Escape')
    await expect.poll(fixture.cancellations).toBe(before + 1)
    await expect(page.locator('.alternate-delivery')).toHaveCount(0)
    await expect(textarea).toHaveValue(draft)
    await expect(page.locator('.messages article.user .markdown')).toHaveText('Controlled follow-up fixture')
  }
  expect(fixture.cancelPaths).toEqual(Array(4).fill(`/api/sessions/${fixture.session.id}/cancel`))
  expect(fixture.sent).toHaveLength(0)
})

test('Escape can retry cancellation after a failed request without losing the draft', async ({ page, vista }) => {
  const fixture = await mockConversation(page, vista, { cancelFailures: 1 })
  const textarea = page.locator('.sender-card textarea')
  await textarea.fill('Keep the draft after failed cancellation')
  await textarea.press('Escape')
  await expect.poll(fixture.cancellations).toBe(1)
  await expect(page.locator('.error')).toContainText('Synthetic cancel failure')
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeEnabled()
  await expect(textarea).toHaveValue('Keep the draft after failed cancellation')
  await textarea.press('Escape')
  await expect.poll(fixture.cancellations).toBe(2)
  await expect(page.locator('.alternate-delivery')).toHaveCount(0)
  await expect(textarea).toHaveValue('Keep the draft after failed cancellation')
  expect(fixture.cancelPaths).toEqual(Array(2).fill(`/api/sessions/${fixture.session.id}/cancel`))
  expect(fixture.sent).toHaveLength(0)
})


test('ArrowUp recalls only the latest user message into the current empty draft without sending or changing history', async ({ page, vista }) => {
  const latest = 'Last user request\n最后一行，请保留。'
  const history = [
    { role: 'user', content: 'First user request' },
    { role: 'assistant', content: 'Reply to the first user request' },
    { role: 'user', content: latest },
    { role: 'assistant', content: 'Newest assistant reply is not a user draft' },
  ]
  const fixture = await mockConversation(page, vista, { messages: history })
  const requests: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    // Opening a conversation can mark its replies as read without changing its messages.
    if (request.method() === 'POST' && !path.endsWith('/read')) requests.push(path)
  })
  const textarea = page.locator('.sender-card textarea')
  const messages = page.locator('.messages article .markdown')
  await expect(messages).toHaveText(history.map(message => message.content))
  await expect(textarea).toHaveValue('')
  await expect(textarea).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(textarea).toHaveValue(latest)
  const cursor = await textarea.evaluate(element => ({ start: (element as HTMLTextAreaElement).selectionStart, end: (element as HTMLTextAreaElement).selectionEnd, focused: document.activeElement === element }))
  expect(cursor).toEqual({ start: latest.length, end: latest.length, focused: true })
  await expect(messages).toHaveText(history.map(message => message.content))
  await textarea.fill('')
  const second = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const secondText = 'Only the second session request'
  await page.route(`**/api/sessions/${second.id}/messages`, route => route.fulfill({ json: [{ id: 'second-user', runId: 'second-run', sequence: 1, role: 'user', content: secondText, createdAt: new Date().toISOString() }] }))
  await page.route(`**/api/sessions/${second.id}/snapshot`, route => route.fulfill({ json: coreSnapshot(second, [{ id: 'second-user', runId: 'second-run', sequence: 1, role: 'user', content: secondText, createdAt: new Date().toISOString() }]) }))
  await page.goto(`${vista.origin}/sessions/${second.id}`)
  await expect(page.locator('.messages article.user .markdown')).toHaveText(secondText)
  await expect(textarea).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(textarea).toHaveValue(secondText)
  await textarea.fill('')
  await page.goto(vista.origin + '/')
  await expect(page.locator('.welcome')).toBeVisible()
  await textarea.press('ArrowUp')
  await expect(textarea).toHaveValue('')
  await page.goto(`${vista.origin}/sessions/${fixture.session.id}`)
  await expect(messages).toHaveText(history.map(message => message.content))
  await expect(textarea).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(textarea).toHaveValue(latest)
  await expect(messages).toHaveText(history.map(message => message.content))
  expect(requests).toEqual([])
  expect(fixture.sent).toHaveLength(0)
  expect(fixture.cancellations()).toBe(0)
})

test('ArrowUp leaves existing text, whitespace, attachments, and modified or IME input untouched', async ({ page, vista }) => {
  await page.route('**/api/models', async route => {
    const response = await route.fetch()
    const catalog = await response.json()
    return route.fulfill({ response, json: { ...catalog, models: catalog.models.map((model: object) => ({ ...model, inputModalities: ['text', 'image'] })) } })
  })
  const fixture = await mockConversation(page, vista)
  const textarea = page.locator('.sender-card textarea')
  for (const value of ['Existing draft\nDo not overwrite', '   ', '\n']) {
    await textarea.fill(value)
    await textarea.press('ArrowUp')
    await expect(textarea).toHaveValue(value)
  }
  await textarea.fill('')
  for (const key of ['Shift+ArrowUp', 'Control+ArrowUp', 'Meta+ArrowUp', 'Alt+ArrowUp']) {
    await textarea.press(key)
    await expect(textarea).toHaveValue('')
  }
  await textarea.dispatchEvent('keydown', { key: 'ArrowUp', code: 'ArrowUp', repeat: true })
  await expect(textarea).toHaveValue('')
  await textarea.dispatchEvent('compositionstart')
  await textarea.press('ArrowUp')
  await expect(textarea).toHaveValue('')
  await textarea.dispatchEvent('compositionend')
  await textarea.press('ArrowUp')
  await expect(textarea).toHaveValue('')
  await textarea.dispatchEvent('keydown', { key: 'ArrowUp', code: 'ArrowUp', isComposing: true })
  await textarea.dispatchEvent('keydown', { key: 'ArrowUp', code: 'ArrowUp', keyCode: 229, which: 229 })
  await expect(textarea).toHaveValue('')
  await page.waitForTimeout(250)
  await textarea.press('ArrowUp')
  await expect(textarea).toHaveValue('Controlled follow-up fixture')
  await textarea.fill('')
  await page.locator('.sender-attach input[type=file]').setInputFiles({ name: 'synthetic.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl8CE8AAAAASUVORK5CYII=', 'base64') })
  await expect(page.locator('.sender-attachment')).toHaveCount(1)
  await textarea.press('ArrowUp')
  await expect(textarea).toHaveValue('')
  await expect(page.locator('.sender-attachment')).toHaveCount(1)
  await expect(page.locator('.messages article.user .markdown')).toHaveText('Controlled follow-up fixture')
  expect(fixture.sent).toHaveLength(0)
  expect(fixture.cancellations()).toBe(0)
})
