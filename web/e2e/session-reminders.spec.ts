import { closeSettings } from './settings-navigation'
import { test, expect } from './fixtures'
import type { Session } from '../src/client/types'

test.setTimeout(45_000)

test.describe('reply reminders', () => {
  test.use({ replyWithPrompt: true })

  test('background replies stay unread behind settings and read positions survive reload', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const first = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Background one' }) })
    const second = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Background two' }) })
    for (const session of [first, second]) {
      await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Which project do you mean?', modelKey: 'test' }) })
    }
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).filter(session => session.latestReplySequence).length).toBe(2)
    await page.reload()
    const firstCard = page.locator(`.sessions-panel .session-card[data-session-id="${first.id}"]`)
    const secondCard = page.locator(`.sessions-panel .session-card[data-session-id="${second.id}"]`)
    await expect(firstCard.locator('.session-indicator.unread')).toHaveCount(1)
    await expect(secondCard.locator('.session-indicator.unread')).toHaveCount(1)
    await page.goto(`${vista.origin}/settings/general?session=${second.id}`)
    await expect(page.locator('.settings-page')).toBeVisible()
    expect((await vista.api('/sessions') as Session[]).find(session => session.id === second.id)?.readSequence).toBe(0)
    await closeSettings(page)
    await expect(secondCard.locator('.session-indicator.unread')).toHaveCount(0)
    await expect(firstCard.locator('.session-indicator.unread')).toHaveCount(1)
    const readBeforeHidden = (await vista.api('/sessions') as Session[]).find(session => session.id === second.id)!.readSequence
    await page.evaluate(() => Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }))
    await vista.api(`/sessions/${second.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Reply while tab is hidden', modelKey: 'test' }) })
    await expect(secondCard.locator('.session-indicator.unread')).toHaveCount(1)
    expect((await vista.api('/sessions') as Session[]).find(session => session.id === second.id)?.readSequence).toBe(readBeforeHidden)
    await page.evaluate(() => { Reflect.deleteProperty(document, 'visibilityState'); document.dispatchEvent(new Event('visibilitychange')) })
    await expect(secondCard.locator('.session-indicator.unread')).toHaveCount(0)
    await page.reload()
    await expect(secondCard.locator('.session-indicator.unread')).toHaveCount(0)
    await expect(firstCard.locator('.session-indicator.unread')).toHaveCount(1)
    if (testInfo.project.name !== 'desktop') {
      await expect(page.locator('.mobile-sessions-trigger .session-attention-dot')).toBeVisible()
      await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
    }
    await expect(firstCard).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath('unread-reminders.png'), animations: 'disabled' })
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  })

  test('a new reply stays unread while reading earlier messages until it enters the viewport', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Reading history' }) })
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Long history line.\n\n'.repeat(100), modelKey: 'test' }) })
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === session.id)?.latestRunStatus).toBe('succeeded')
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    const card = page.locator(`.sessions-panel .session-card[data-session-id="${session.id}"]`)
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === session.id)?.readSequence || 0).toBeGreaterThan(0)
    const firstRead = (await vista.api('/sessions') as Session[]).find(item => item.id === session.id)!.readSequence
    await page.locator('.messages').evaluate(element => { element.scrollTop = 0; element.dispatchEvent(new Event('scroll')) })
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'A newer reply', modelKey: 'test' }) })
    await expect(card.locator('.session-indicator.unread')).toHaveCount(1)
    expect((await vista.api('/sessions') as Session[]).find(item => item.id === session.id)?.readSequence).toBe(firstRead)
    await page.getByRole('button', { name: 'Jump to latest', exact: true }).click()
    await expect(card.locator('.session-indicator.unread')).toHaveCount(0)
  })
})

test.describe('action reminders', () => {
  test.use({ questionTool: true })

  test('viewing a question or a paused queue does not dismiss its action reminder', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const question = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Pending question' }) })
    const queue = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Paused queue' }) })
    await vista.api(`/sessions/${question.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Ask me about the setup', modelKey: 'test' }) })
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === question.id)?.pendingReply?.kind).toBe('question')
    const release = vista.pauseModelReplies()
    try {
      await vista.api(`/sessions/${queue.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'In progress', modelKey: 'test' }) })
      await vista.api(`/sessions/${queue.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Keep for later', mode: 'later', modelKey: 'test' }) })
      await vista.api(`/sessions/${queue.id}/cancel`, { method: 'POST' })
      await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === queue.id)?.executing).toBe(false)
    } finally { release() }
    await page.goto(`${vista.origin}/sessions/${question.id}`)
    const questionCard = page.locator(`.sessions-panel .session-card[data-session-id="${question.id}"]`)
    const queueCard = page.locator(`.sessions-panel .session-card[data-session-id="${queue.id}"]`)
    await expect(questionCard.locator('.session-indicator.waiting')).toHaveText('Waiting for your answer')
    await expect(queueCard.locator('.session-indicator.waiting')).toHaveText('Messages paused')
    await page.goto(`${vista.origin}/sessions/${queue.id}`)
    await expect(queueCard.locator('.session-indicator.waiting')).toHaveText('Messages paused')
    const releaseRetry = vista.pauseModelReplies()
    try {
      await page.locator('.sender-card textarea').fill('New task after cancellation')
      await page.locator('.sender-card textarea').press('Enter')
      await expect(page.locator('article.user .markdown').filter({ hasText: 'New task after cancellation' })).toBeVisible()
      await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeEnabled()
      await vista.api(`/sessions/${queue.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'New follow-up', mode: 'later', modelKey: 'test' }) })
      const oldInput = page.locator('.pending-input').filter({ hasText: 'Keep for later' })
      const newInput = page.locator('.pending-input').filter({ hasText: 'New follow-up' })
      for (const reload of [false, true]) {
        if (reload) await page.reload()
        await expect(oldInput.locator('.pending-input-label')).toHaveText('Paused')
        await expect(newInput.locator('.pending-input-label')).toHaveText('After the current task')
        await expect(page.locator('.pending-inputs-heading')).toContainText('Pending input')
        await expect(queueCard.locator('.session-indicator.waiting')).toHaveText('Messages paused')
      }
      await page.screenshot({ path: testInfo.outputPath('mixed-pending-inputs.png'), animations: 'disabled' })
    } finally { releaseRetry() }
    await expect(page.locator('article.user .markdown').filter({ hasText: 'New follow-up' })).toBeVisible()
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === queue.id)?.executing).toBe(false)
    await expect(page.locator('.pending-input')).toHaveCount(1)
    await expect(page.locator('.pending-input')).toContainText('Keep for later')
    await expect(page.locator('article.user .markdown').filter({ hasText: 'Keep for later' })).toHaveCount(0)
    await page.reload()
    await expect(page.locator('.pending-input-label')).toHaveText('Paused')
    await expect(queueCard.locator('.session-indicator.waiting')).toHaveText('Messages paused')
    if (testInfo.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
    await page.screenshot({ path: testInfo.outputPath('action-reminders.png'), animations: 'disabled' })
    if (testInfo.project.name !== 'desktop') await page.getByRole('button', { name: 'Close session list', exact: true }).click()
    await page.getByRole('button', { name: 'Resume', exact: true }).click()
    await expect(queueCard.locator('.session-indicator.waiting')).toHaveCount(0)
    await expect(page.locator('article.user .markdown').filter({ hasText: 'Keep for later' })).toBeVisible()
    await expect(questionCard.locator('.session-indicator.waiting')).toHaveText('Waiting for your answer')
    // Reading the ordinary reply does not clear the question in the other session.
    await page.reload()
    await expect(questionCard.locator('.session-indicator.waiting')).toHaveText('Waiting for your answer')
  })
})

test.describe('failure reminders', () => {
  test.use({ progressCalls: 1 })

  test('execution failure and worker unavailability remain distinct in light and dark themes', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Failed execution' }) })
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Fail progress', modelKey: 'test' }) })
    await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === session.id)?.latestRunStatus).toBe('failed')
    await page.route('**/api/status', async route => {
      const response = await route.fetch()
      const status = await response.json()
      await route.fulfill({ response, json: { ...status, workerMetrics: status.workerMetrics.filter((worker: { sessionId: string }) => worker.sessionId !== session.id) } })
    })
    await page.reload()
    const card = page.locator(`.sessions-panel .session-card[data-session-id="${session.id}"]`)
    await expect(card.locator('.session-indicator.failed')).toHaveText('Execution failed')
    await expect(card.locator('.session-indicator.unavailable')).toHaveText('Worker unavailable')
    if (testInfo.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
    await expect(card.getByRole('button', { name: 'Retry Worker', exact: true })).toBeVisible()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await page.screenshot({ path: testInfo.outputPath(`failure-reminders-${colorScheme}.png`), animations: 'disabled' })
    }
    await page.unroute('**/api/status')
    await page.reload()
    await expect(card.locator('.session-indicator.unavailable')).toHaveCount(0)
    await expect(card.locator('.session-indicator.failed')).toHaveText('Execution failed')
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Try again', modelKey: 'test' }) })
    await expect(card.locator('.session-indicator.failed')).toHaveCount(0)
  })
})
