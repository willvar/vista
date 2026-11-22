import { test, expect } from './fixtures'

test.use({ replyWithPrompt: true })

test('scheduled task UI sends a plain message into a new session and updates the subscribed list', { tag: '@responsive' }, async ({ page, vista }, info) => {
  let snapshots = 0
  page.on('request', request => { if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/schedules') snapshots++ })
  await page.goto(vista.origin)
  if (info.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
  await page.getByRole('button', { name: 'Scheduled tasks', exact: true }).click()
  const panel = page.locator('.schedules-page')
  await expect(panel.getByText('No scheduled tasks yet.')).toBeVisible()
  await panel.getByRole('button', { name: 'New scheduled task' }).click()
  await panel.getByLabel('Task name').fill('Daily report')
  await panel.getByLabel('Message to send').fill('Review the project release notes.')
  await panel.getByLabel('Time zone').fill('UTC')
  await expect(panel.getByRole('status')).toContainText('Next run:')
  await panel.getByRole('button', { name: 'Save and enable' }).click()
  const row = panel.locator('.schedule-row').filter({ hasText: 'Daily report' })
  await expect(row).toBeVisible()
  expect(await vista.api('/sessions')).toEqual([])
  expect(vista.modelRequests).toHaveLength(0)
  const initial = (await vista.api('/schedules')).items[0]
  expect(initial.modelKey).toBe('')
  expect(initial).not.toHaveProperty('prompt')
  await row.getByRole('button', { name: 'Pause schedule' }).click()
  await expect(row.getByText('Schedule paused')).toBeVisible()
  await row.getByRole('button', { name: 'Resume', exact: true }).click()
  await expect(row.getByText('Schedule paused')).toHaveCount(0)
  await row.getByRole('button', { name: 'Run now' }).click()
  await expect(row.getByText('succeeded', { exact: true })).toBeVisible()
  expect((await vista.api('/schedules')).items[0].nextAt).toBe(initial.nextAt)
  expect(snapshots).toBe(1)
  await row.getByRole('button', { name: 'Run history' }).click()
  await panel.getByRole('button', { name: 'Open', exact: true }).click()
  await expect(page.locator('article.user')).toHaveCount(1)
  await expect(page.locator('article.user')).toContainText('Review the project release notes.')
  await expect(page.locator('article.assistant')).toContainText('Review the project release notes.')
  const sessions = await vista.api('/sessions')
  expect(sessions).toHaveLength(1)
  expect(sessions[0].open).toBe(false)
  const sent = vista.modelRequests.flatMap(request => request.messages ?? []).filter(message => message.role === 'user')
  expect(sent.map(message => message.content)).toContain('Review the project release notes.')
})

test('native restart activates due work in a non-default Profile without a client opening it', async ({ vista }) => {
  const profile = await vista.api('/profiles', { method: 'POST', body: JSON.stringify({ name: 'Night shift' }) })
  const scoped = `/profiles/${profile.id}/api`
  const call = async (path: string, body?: object) => {
    const result = await fetch(vista.origin + scoped + path, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {})
    if (!result.ok) throw Error(`${path}: ${result.status} ${await result.text()}`)
    return result.json()
  }
  await call('/provider-config', { name: 'Night shift model', provider: 'openai-chat', providerId: 'custom', baseUrl: vista.modelEndpoint, credentialMode: 'bearer', apiKey: 'test-key', ids: [vista.modelId] })
  const note = 'This message comes from the Night shift task.'
  const at = new Date(Date.now() + 4_000).toISOString().slice(0, 19)
  const plan = await call('/schedules', { name: 'Night shift', prompt: note, rule: { kind: 'once', timezone: 'UTC', at } })
  const before = (await vista.api('/status')).instanceId
  await vista.restart()
  expect((await vista.api('/status')).instanceId).not.toBe(before)
  // Reading the scoped API before this assertion would open the Profile and
  // thus fail to test headless bootstrap. The stub sees the actual dispatch.
  await expect.poll(() => vista.modelRequests.some(request => request.messages?.some(message => message.role === 'user' && message.content === note)), { timeout: 15_000 }).toBe(true)
  await expect.poll(async () => (await call(`/schedules/${plan.id}/history`)).items.map((run: { state: string }) => run.state)).toEqual(['succeeded'])
  expect((await vista.api('/schedules')).items).toEqual([])
  expect(await vista.api('/sessions')).toEqual([])
})

const post = (body: object) => ({ method: 'POST', body: JSON.stringify(body) })
const periodic = { kind: 'interval', minutes: 60, timezone: 'UTC' }

test('a crashed scheduled execution is closed and not replayed after a service restart', async ({ vista }) => {
  const note = 'Hold this scheduled reply for the crash check.'
  const plan = await vista.api('/schedules', post({ name: 'Crash check', prompt: note, rule: periodic }))
  const release = vista.pauseModelReplies()
  try {
    await vista.api(`/schedules/${plan.id}/run-now`, post({ requestId: 'ddc3b8cc-badf-490d-90aa-a96f4c78f393' }))
    await expect.poll(() => vista.modelRequests.some(request => request.messages?.some(message => message.role === 'user' && message.content === note))).toBe(true)
    const { latest } = (await vista.api('/schedules')).items[0]
    expect(latest.state).toBe('running')
    const status = await vista.api('/status')
    const worker = status.workerMetrics.find((item: { sessionId: string }) => item.sessionId === latest.sessionId)
    expect(worker?.pid).toBeGreaterThan(1)
    expect(worker.pid).not.toBe(status.pid)
    process.kill(worker.pid, 'SIGKILL') // An isolated fixture Worker, never the running user's instance.
    await expect.poll(async () => (await vista.api('/schedules')).items[0].latest.state).toBe('interrupted')
    await vista.restart()
    const recovered = (await vista.api('/schedules')).items[0]
    expect(recovered.enabled).toBe(false)
    expect(recovered.latest.sessionId).toBe(latest.sessionId)
    expect((await vista.api(`/sessions/${latest.sessionId}/checkpoint`)).session.open).toBe(false)
    expect((await vista.api(`/schedules/${plan.id}/history`)).items).toHaveLength(1)
    expect(vista.modelRequests.filter(request => request.messages?.some(message => message.role === 'user' && message.content === note))).toHaveLength(1)
  } finally { release() }
})

test.describe('scheduled questions', () => {
  test.use({ questionTool: true })

  test('answering uses the same conversation and occurrence; the plan stays paused', async ({ vista }) => {
    const plan = await vista.api('/schedules', post({ name: 'Question', prompt: 'Ask me about the setup', rule: periodic }))
    const item = async () => (await vista.api('/schedules')).items[0]
    await vista.api(`/schedules/${plan.id}/run-now`, post({ requestId: '65e98967-e3ab-456c-9f21-c1ec51be734c' }))
    await expect.poll(async () => (await item()).latest?.state).toBe('waiting_reply')
    const waiting = await item()
    expect(waiting.enabled).toBe(false)
    const session = waiting.latest.sessionId
    expect((await vista.api(`/sessions/${session}/checkpoint`)).session.open).toBe(true)
    await vista.api(`/sessions/${session}/messages`, post({ content: 'Use the source folder' }))
    await expect.poll(async () => (await item()).latest?.state).toBe('succeeded')
    const answered = await item()
    expect(answered.enabled).toBe(false)
    expect(answered.latest.sessionId).toBe(session)
    expect((await vista.api(`/schedules/${plan.id}/history`)).items).toHaveLength(1)
  })
})

test.describe('scheduled background work', () => {
  test.use({ backgroundProcess: true })

  test('existing background tools run normally and are drained on conversation close', async ({ vista }) => {
    const plan = await vista.api('/schedules', post({ name: 'Background', prompt: 'Start process', rule: periodic }))
    const item = async () => (await vista.api('/schedules')).items[0]
    await vista.api(`/schedules/${plan.id}/run-now`, post({ requestId: '73651c35-d798-4ec0-aacd-af76e331229f' }))
    await expect.poll(async () => (await item()).latest?.state).toBe('succeeded')
    const executed = (await item()).latest.sessionId
    const read = () => vista.api(`/sessions/${executed}/jobs`)
    await expect.poll(async () => (await read()).map((job: { status: string }) => job.status)).not.toContain('running')
    expect(await read()).toHaveLength(1) // The tool was allowed, not filtered out.
    expect((await vista.api(`/sessions/${executed}/checkpoint`)).session.open).toBe(false)
    await expect.poll(async () => (await vista.api('/status')).workerMetrics.some((entry: { sessionId: string }) => entry.sessionId === executed)).toBe(false)
  })
})
