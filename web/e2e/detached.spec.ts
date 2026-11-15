import { test, expect } from './fixtures'

test.use({ detachedTask: true, replyWithPrompt: true })

test('background child survives refresh and the next reply, then saves its tool and report', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Start background' }) })
  await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === session.id)?.executing).toBe(false)
  expect((await vista.api('/sessions')).find((s: any) => s.id === session.id)?.running).toBe(true)
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const child = page.locator('.trace-agent-message-heading[data-task-id="main/background"]')
  await expect(child).toHaveAttribute('data-task-status', 'started')
  await expect(child).toContainText('Running in background')
  await page.reload()
  await expect(child).toContainText('Running in background')
  await page.locator('.sender-card textarea').fill('Next question')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(page.locator('article.assistant').last()).toContainText('Mock reply: Next question')
  vista.finishDetachedTask()
  await expect(child).toHaveAttribute('data-task-status', 'succeeded')
  await expect(page.locator('article.assistant').last()).toContainText('Background completion received: succeeded.')
  await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === session.id)?.running).toBe(false)
  const messages = await vista.api(`/sessions/${session.id}/messages`)
  expect(messages.filter((message: any) => message.role === 'user').map((message: any) => message.content)).toEqual(['Start background', 'Next question'])
  expect(messages.filter((message: any) => message.role === 'assistant').map((message: any) => message.content)).toEqual([
    'Mock reply: Start background', 'Mock reply: Next question', 'Background completion received: succeeded.',
  ])
  const events = await vista.api(`/sessions/${session.id}/event-page`)
  const completionEvents = events.filter((event: any) => event.kind === 'runtime_event'
    && event.data.kind === 'agent' && event.data.sourceId === 'main/background')
  expect(completionEvents).toHaveLength(1)
  expect(completionEvents[0].data).toMatchObject({ recipient: 'main', status: 'succeeded' })
  const notices = vista.modelRequests.flatMap(request => request.messages || [])
    .filter(message => String(message.content).startsWith('Runtime completion event.'))
  expect(notices.every(message => message.role === 'user')).toBe(true)
  expect(notices.filter(message => JSON.parse(String(message.content).split('\n').slice(1).join('\n')).id === completionEvents[0].data.id)).toHaveLength(1)
  const toolEvents = events.filter((event: any) => event.kind === 'runtime_event' && event.data.kind === 'tool'
    && event.data.recipient === 'main/background')
  expect(toolEvents).toHaveLength(1)
  expect(toolEvents[0].data).toMatchObject({ status: 'succeeded' })
  expect(toolEvents[0].data.sourceId).toMatch(/(?:^|\/)background-shell$/)
  const childRequests = vista.modelRequests.filter(request => request.messages?.some(message => message.role === 'user' && message.content === 'Background check'))
  const pendingRequests = childRequests.filter(request => {
    const last = request.messages?.at(-1)
    if (last?.role !== 'tool') return false
    try { return JSON.parse(String(last.content)).status === 'running' } catch { return false }
  })
  expect(pendingRequests.length).toBeLessThanOrEqual(1)
  expect(childRequests).toHaveLength(2 + pendingRequests.length)
  const report = events.findLast((event: any) => event.kind === 'agent_task' && event.data.task?.id === 'main/background').data.task.report
  const output = await vista.api(`/sessions/${session.id}/runs/${report.runId}/model-output?turn=${report.turn}&full=1`)
  expect(output.content).toBe('Background report complete.')
  expect(output.complete).toBe(true)
  const childTools = events.filter((event: any) => event.kind === 'tool' && event.data.taskId === 'main/background')
  expect(childTools.filter((event: any) => event.data.status === 'started')).toHaveLength(1)
  expect(childTools.filter((event: any) => event.data.status === 'succeeded')).toHaveLength(1)
  const requests = vista.modelRequests.length
  await page.reload()
  await expect(page.locator('article.assistant').last()).toContainText('Background completion received: succeeded.')
  expect(vista.modelRequests.length).toBe(requests)
  await child.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('detached-complete.png'), animations: 'disabled' })
})

test('background child can be stopped explicitly after the foreground reply', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Start background' }) })
  await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === session.id)?.executing).toBe(false)
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const child = page.locator('.trace-agent-message-heading[data-task-id="main/background"]')
  await child.locator('.trace-agent-heading').click()
  await child.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('detached-running.png'), animations: 'disabled' })
  await child.getByRole('button', { name: 'Stop child session', exact: true }).click()
  await expect(child).toHaveAttribute('data-task-status', 'canceled')
  await expect(child.getByRole('button', { name: 'Stop child session', exact: true })).toHaveCount(0)
  await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === session.id)?.running).toBe(false)
  await page.reload()
  await expect(child).toHaveAttribute('data-task-status', 'canceled')
  const messages = await vista.api(`/sessions/${session.id}/messages`)
  expect(messages.filter((message: any) => message.role === 'user')).toHaveLength(1)
  expect(messages.filter((message: any) => message.role === 'assistant').map((message: any) => message.content)).toEqual([
    'Mock reply: Start background', 'Background completion received: canceled.',
  ])
})

test('closing the session stops background work without replay on reopen', async ({ vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Start background' }) })
  await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === session.id)?.executing).toBe(false)
  expect((await vista.api('/sessions')).find((s: any) => s.id === session.id)?.running).toBe(true)
  await vista.api(`/sessions/${session.id}/close`, { method: 'POST', body: '{}' })
  const tasks = (await vista.api(`/sessions/${session.id}/event-page`)).filter((event: any) => event.kind === 'agent_task' && event.data.task.id === 'main/background')
  expect(['canceled', 'interrupted']).toContain(tasks.at(-1).data.task.status)
  const requests = vista.modelRequests.length
  await vista.api(`/sessions/${session.id}/open`, { method: 'POST', body: '{}' })
  expect((await vista.api('/sessions')).find((s: any) => s.id === session.id)?.running).toBe(false)
  expect((await vista.api(`/sessions/${session.id}/messages`)).filter((message: any) => message.role === 'assistant')).toHaveLength(1)
  expect(vista.modelRequests.length).toBe(requests)
})
