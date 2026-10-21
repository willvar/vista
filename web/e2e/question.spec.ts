import { test, expect } from './fixtures'

test.use({ replyWithPrompt: true, questionTool: true })

test('a plain question is shown once and continues through the ordinary composer', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  const sender = page.locator('.sender-card textarea')
  await sender.fill('Which person do you mean?')
  await sender.press('Enter')
  const reply = page.getByText('Mock reply: Which person do you mean?', { exact: true })
  await expect(reply).toHaveCount(1)
  await expect(page.locator('.question-form')).toHaveCount(0)
  const sessionId = new URL(page.url()).pathname.split('/').at(-1)!
  await expect.poll(async () => (await vista.api('/sessions')).find((session: { id: string }) => session.id === sessionId)?.pendingReply ?? null).toBeNull()
  await page.reload()
  await expect(reply).toHaveCount(1)
  await sender.fill('The writer I mentioned')
  await sender.press('Enter')
  await expect(page.getByText('Mock reply: The writer I mentioned', { exact: true })).toHaveCount(1)
  await expect.poll(() => vista.modelRequests.length).toBe(2)
  expect(vista.modelRequests[1]?.messages?.some(message => message.role === 'assistant' && message.content === 'Mock reply: Which person do you mean?')).toBe(true)
})

test('question choices persist across reload and resume with selected answers', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  const sender = page.locator('.sender-card textarea')
  await sender.fill('Ask me about the setup')
  await sender.press('Enter')
  const form = page.locator('.question-form')
  await expect(form).toBeVisible()
  const runId = await form.getAttribute('data-run-id')
  expect(runId).toBeTruthy()
  const sessionId = new URL(page.url()).pathname.split('/').at(-1)!

  await page.reload()
  await expect(form).toHaveAttribute('data-run-id', runId!)
  await expect(form.locator('.question-item')).toHaveCount(1)
  await expect(form.getByRole('tab')).toHaveCount(3)
  await expect(form.getByRole('tab', { name: 'Deployment', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(form.getByRole('button', { name: 'Previous question', exact: true })).toBeDisabled()
  const next = form.getByRole('button', { name: 'Next question', exact: true })
  const submit = form.getByRole('button', { name: 'Submit answers', exact: true })
  await expect(submit).toHaveCount(0)
  const deployment = form.locator('[data-question-index="0"]')
  await deployment.getByRole('radio', { name: 'Remote host', exact: true }).check()
  await deployment.getByRole('radio', { name: 'Keep local', exact: true }).check()
  await expect(deployment.getByRole('radio', { name: 'Remote host', exact: true })).not.toBeChecked()
  await expect(form.getByRole('tab', { name: 'Deployment', exact: true })).toHaveAttribute('data-answered', 'true')
  await next.click()
  const scope = form.locator('[data-question-index="1"]')
  await expect(scope).toBeVisible()
  await expect(deployment).toHaveCount(0)
  await scope.getByRole('checkbox', { name: 'Messages', exact: true }).check()
  await scope.getByRole('checkbox', { name: 'Sessions', exact: true }).check()
  await next.click()
  const priority = form.locator('[data-question-index="2"]')
  await priority.getByRole('radio', { name: 'Type your own answer', exact: true }).check()
  await expect(submit).toBeDisabled()
  await priority.getByRole('textbox', { name: 'Your answer: What should the implementation prioritize?', exact: true }).fill('Make keyboard navigation predictable')
  await expect(submit).toBeEnabled()
  await page.reload()
  await expect(form).toHaveAttribute('data-run-id', runId!)
  await expect(form.locator('.question-item')).toHaveCount(1)
  await expect(form.getByRole('tab', { name: 'Priority', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(priority.getByRole('textbox', { name: 'Your answer: What should the implementation prioritize?', exact: true })).toHaveValue('Make keyboard navigation predictable')
  await expect(submit).toBeEnabled()
  await form.getByRole('button', { name: 'Previous question', exact: true }).click()
  await expect(scope.getByRole('checkbox', { name: 'Messages', exact: true })).toBeChecked()
  await expect(scope.getByRole('checkbox', { name: 'Sessions', exact: true })).toBeChecked()
  await form.getByRole('button', { name: 'Previous question', exact: true }).click()
  await expect(deployment.getByRole('radio', { name: 'Keep local', exact: true })).toBeChecked()

  // Long question descriptions must wrap inside the message card on small screens.
  const geometry = await form.evaluate(element => {
    const box = element.getBoundingClientRect()
    return {
      left: box.left, right: box.right, viewportWidth: innerWidth,
      pageWidth: document.documentElement.scrollWidth,
      overflow: [...element.querySelectorAll('.question-item, .question-option')].map(item => ({
        width: item.clientWidth, scrollWidth: item.scrollWidth,
      })),
    }
  })
  expect(geometry.left).toBeGreaterThanOrEqual(-1)
  expect(geometry.right).toBeLessThanOrEqual(geometry.viewportWidth + 1)
  expect(geometry.pageWidth).toBeLessThanOrEqual(geometry.viewportWidth + 2)
  for (const item of geometry.overflow) expect(item.scrollWidth).toBeLessThanOrEqual(item.width + 2)
  await deployment.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('question-options.png'), animations: 'disabled' })
  await form.getByRole('tab', { name: 'Priority', exact: true }).click()
  await expect(priority.getByRole('textbox', { name: 'Your answer: What should the implementation prioritize?', exact: true })).toHaveValue('Make keyboard navigation predictable')
  await expect(submit).toBeEnabled()

  const requestPromise = page.waitForRequest(request => request.method() === 'POST'
    && new URL(request.url()).pathname === `/api/sessions/${sessionId}/messages`
    && !!request.postDataJSON()?.questionReply)
  await submit.click()
  const answerRequest = (await requestPromise).postDataJSON()
  expect(answerRequest.questionReply).toEqual({ runId, answers: [
    { options: ['Keep local'] },
    { options: ['Messages', 'Sessions'] },
    { options: [], text: 'Make keyboard navigation predictable' },
  ] })
  await expect(form).toHaveCount(0)
  await expect.poll(() => page.evaluate(key => [sessionStorage.getItem(key), sessionStorage.getItem(key + ':index')], `vista-question:${sessionId}:${runId}`)).toEqual([null, null])
  await expect(page.locator('article.assistant .markdown').filter({ hasText: 'Mock reply:' })).toContainText('Make keyboard navigation predictable')
  await expect.poll(() => vista.modelRequests.length).toBe(2)
  const replay = vista.modelRequests[1]!.messages!
  const calls = replay.flatMap(message => message.tool_calls || [])
  expect(calls).toHaveLength(1)
  expect(calls[0]).toMatchObject({ id: 'setup-question', function: { name: 'question' } })
  expect(JSON.parse(calls[0]!.function.arguments).questions).toHaveLength(3)
  const results = replay.filter(message => message.role === 'tool' && message.tool_call_id === 'setup-question')
  expect(results).toHaveLength(1)
  expect(replay.filter(message => message.role === 'user')).toHaveLength(1)
  const deliveredAnswer = results[0]!.content
  expect(typeof deliveredAnswer).toBe('string')
  for (const text of [
    'Where should the service run?', 'Keep local', 'Which parts should this change include?',
    'Messages', 'Sessions', 'What should the implementation prioritize?', 'Make keyboard navigation predictable',
  ]) expect(deliveredAnswer).toContain(text)
  expect(deliveredAnswer).not.toContain('Remote host')
  expect(deliveredAnswer).not.toContain('Provider settings')
  expect(deliveredAnswer).not.toContain('Answer:')

  const duplicate = await fetch(`${vista.origin}/api/sessions/${sessionId}/messages`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', Origin: vista.origin }, body: JSON.stringify(answerRequest),
  })
  expect(duplicate.status).toBe(409)
  expect(vista.modelRequests).toHaveLength(2)
  await page.reload()
  await expect(form).toHaveCount(0)
  await expect(page.locator('article.assistant .markdown').filter({ hasText: 'Mock reply:' })).toContainText('Make keyboard navigation predictable')
})

test('question tabs switch one panel at a time with keyboard navigation and reduced motion', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.reload()
  const sender = page.locator('.sender-card textarea')
  await sender.fill('Ask me about the setup')
  await sender.press('Enter')
  const form = page.locator('.question-form')
  const deployment = form.getByRole('tab', { name: 'Deployment', exact: true })
  const scope = form.getByRole('tab', { name: 'Scope', exact: true })
  const priority = form.getByRole('tab', { name: 'Priority', exact: true })
  await expect(deployment).toBeVisible()
  await form.evaluate(element => {
    element.setAttribute('data-max-panels', String(element.querySelectorAll('.question-item').length))
    const observer = new MutationObserver(() => element.setAttribute('data-max-panels', String(Math.max(
      Number(element.getAttribute('data-max-panels')), element.querySelectorAll('.question-item').length,
    ))))
    observer.observe(element, { childList: true, subtree: true })
  })
  await deployment.focus()
  await deployment.press('ArrowRight')
  await expect(scope).toBeFocused()
  await expect(form.locator('[data-question-index="1"]')).toBeVisible()
  await scope.press('End')
  await expect(priority).toBeFocused()
  await expect(form.locator('[data-question-index="2"]')).toBeVisible()
  await expect(form.getByRole('button', { name: 'Submit answers', exact: true })).toBeDisabled()
  await priority.press('Home')
  await expect(deployment).toBeFocused()
  await expect(form.locator('[data-question-index="0"]')).toBeVisible()
  await deployment.press('ArrowLeft')
  await expect(priority).toBeFocused()
  await expect(form.locator('[data-question-index="2"]')).toBeVisible()
  await form.getByRole('button', { name: 'Previous question', exact: true }).click()
  await expect(form.locator('[data-question-index="1"] .question-text')).toBeFocused()

  await page.emulateMedia({ reducedMotion: 'reduce' })
  await deployment.click()
  await expect(form.locator('[data-question-index="0"]')).toBeVisible()
  await expect.poll(() => deployment.evaluate(element => getComputedStyle(element).transitionProperty)).toBe('none')
  await priority.click()
  await expect(form.locator('[data-question-index="2"]')).toBeVisible()
  await expect(form).toHaveAttribute('data-max-panels', '1')
  await expect(form.locator('.question-item')).toHaveCount(1)
  expect(vista.modelRequests).toHaveLength(1)
})

test('typing a normal message replaces a pending question and rejects its stale form', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  const sender = page.locator('.sender-card textarea')
  await sender.fill('Ask me about the setup')
  await sender.press('Enter')
  const form = page.locator('.question-form')
  await expect(form).toBeVisible()
  const runId = await form.getAttribute('data-run-id')
  const sessionId = new URL(page.url()).pathname.split('/').at(-1)!
  await sender.fill('Skip those choices and use my text')
  await sender.press('Enter')
  await expect(form).toHaveCount(0)
  await expect(page.locator('article.assistant .markdown').filter({ hasText: 'Mock reply:' })).toHaveText('Mock reply: Skip those choices and use my text')
  const stale = await fetch(`${vista.origin}/api/sessions/${sessionId}/messages`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', Origin: vista.origin },
    body: JSON.stringify({ questionReply: { runId, answers: [
      { options: ['Keep local'] }, { options: ['Messages'] }, { options: ['Fast response'] },
    ] } }),
  })
  expect(stale.status).toBe(409)
  expect(vista.modelRequests).toHaveLength(2)
  const replay = vista.modelRequests[1]!.messages!
  const unanswered = replay.find(message => message.role === 'tool' && message.tool_call_id === 'setup-question')
  expect(JSON.parse(String(unanswered?.content))).toMatchObject({ status: 'unanswered' })
  expect(replay.at(-1)).toMatchObject({ role: 'user', content: 'Skip those choices and use my text' })
})
