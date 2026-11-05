import { test, expect } from './fixtures'
import type { Page } from '@playwright/test'

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

function trackCloseRequests(page: Page) {
  const closed: string[] = []
  page.on('request', request => {
    const match = new URL(request.url()).pathname.match(/^\/api\/sessions\/([^/]+)\/close$/)
    if (request.method() === 'POST' && match) closed.push(match[1]!)
  })
  return closed
}

async function openSidebar(page: Page) {
  const trigger = page.locator('.mobile-sessions-trigger:visible')
  if (await trigger.count()) await trigger.click()
}

test('idle sidebar sessions require confirmation and manager sessions retain their history', async ({ page, vista }) => {
  const sidebarSession = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Idle sidebar' }) })
  const managerSession = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Idle manager' }) })
  const closeRequests = trackCloseRequests(page)
  await page.goto(`${vista.origin}/sessions/${sidebarSession.id}`)
  await openSidebar(page)
  await page.locator(`.session-card[data-session-id="${sidebarSession.id}"] .session-menu`).click()
  await page.getByRole('menuitem', { name: 'Close', exact: true }).click()
  const confirmation = page.getByRole('dialog', { name: 'Close session?', exact: true })
  await expect(confirmation).toBeVisible()
  expect(closeRequests).toEqual([])
  await confirmation.locator('.ui-modal-footer').getByRole('button', { name: 'Close', exact: true }).click()
  await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === sidebarSession.id)?.open).toBe(false)
  await expect(page.getByRole('dialog', { name: 'Close session?', exact: true })).toBeHidden()

  await page.goto(`${vista.origin}/sessions`)
  const row = page.locator('.managed-session').filter({ hasText: 'Idle manager' })
  await row.getByRole('button', { name: 'Close', exact: true }).click()
  await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === managerSession.id)?.open).toBe(false)
  await expect(page.getByRole('dialog', { name: 'Close session?', exact: true })).toBeHidden()
  await expect(page.locator('.session-manager-page')).toBeVisible()
  expect(closeRequests).toEqual([sidebarSession.id, managerSession.id])
  expect(await vista.api('/sessions')).toHaveLength(2)
})

test('closing an executing session waits for confirmation and Escape leaves the task and manager open', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Executing task' }) })
  const release = vista.pauseModelReplies()
  const closeRequests = trackCloseRequests(page)
  const stops: string[] = []
  page.on('request', request => { if (new URL(request.url()).pathname.endsWith('/cancel')) stops.push(request.url()) })
  try {
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    await page.locator('.sender-card textarea').fill('Keep this task running')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeVisible()
    await openSidebar(page)
    const close = page.locator(`.session-card[data-session-id="${session.id}"] .session-menu`)
    await close.click()
    await page.getByRole('menuitem', { name: 'Close', exact: true }).click()
    const confirmation = page.getByRole('dialog', { name: 'Close session?', exact: true })
    await expect(confirmation).toContainText('Running tasks and background processes will stop.')
    await expect(confirmation).toContainText('Chat history will be kept')
    await expect(confirmation).toContainText('Unfinished tasks will not resume automatically.')
    expect(closeRequests).toEqual([])
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(confirmation).toBeHidden()
    expect((await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id)?.executing).toBe(true)

    await page.goto(`${vista.origin}/sessions?session=${session.id}`)
    const manager = page.locator('.session-manager-page')
    const row = manager.locator('.managed-session').filter({ hasText: 'Executing task' })
    await row.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(confirmation).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(confirmation).toBeHidden()
    await expect(manager).toBeVisible()
    expect(closeRequests).toEqual([])
    expect(stops).toEqual([])
    expect((await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id)?.executing).toBe(true)

    await row.getByRole('button', { name: 'Close', exact: true }).click()
    await confirmation.getByRole('button', { name: 'Close and stop', exact: true }).click()
    await expect(confirmation).toBeHidden()
    await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id)?.open).toBe(false)
    expect(closeRequests).toEqual([session.id])
    const messages = await vista.api(`/sessions/${session.id}/messages`)
    expect(messages.some((message: { content: string }) => message.content === 'Keep this task running')).toBe(true)
    await expect(manager).toBeVisible()
  } finally { release() }
})

test.describe('background work', () => {
  test.use({ backgroundProcess: true })

  test('background processes require confirmation and a mixed batch closes only the confirmed selection', async ({ page, vista }) => {
    const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Background worker' }) })
    const idle = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Idle selected' }) })
    const untouched = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Unselected' }) })
    const closeRequests = trackCloseRequests(page)
    await page.goto(`${vista.origin}/sessions/${active.id}`)
    await page.locator('.sender-card textarea').fill('Start process')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('article.assistant').last()).toContainText('Process started.')
    const readJob = async () => (await vista.api(`/sessions/${active.id}/jobs`))[0]
    expect((await readJob()).status).toBe('running')
    await expect.poll(async () => {
      const session = (await vista.api('/sessions')).find((item: { id: string }) => item.id === active.id)
      return { running: session.running, executing: session.executing }
    }).toEqual({ running: true, executing: false })
    const sidebar = page.locator(`.session-card[data-session-id="${active.id}"]`)
    await expect(sidebar.locator('.session-indicator.running')).toHaveText('Background running')
    await expect(sidebar.locator('.session-execution-loading')).toHaveCount(0)

    await page.goto(`${vista.origin}/sessions?session=${active.id}`)
    const manager = page.locator('.session-manager-page')
    const row = manager.locator('.managed-session').filter({ hasText: 'Background worker' })
    await row.getByRole('button', { name: 'Close', exact: true }).click()
    const singleConfirmation = page.getByRole('dialog', { name: 'Close session?', exact: true })
    await expect(singleConfirmation).toBeVisible()
    expect(closeRequests).toEqual([])
    await singleConfirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect((await readJob()).status).toBe('running')

    const activeCheckbox = manager.getByRole('checkbox', { name: 'Select Background worker', exact: true })
    const idleCheckbox = manager.getByRole('checkbox', { name: 'Select Idle selected', exact: true })
    await activeCheckbox.check()
    await idleCheckbox.check()
    const closeSelected = manager.getByRole('button', { name: 'Close selected (2)', exact: true })
    await closeSelected.click()
    const confirmation = page.getByRole('dialog', { name: 'Close 2 sessions?', exact: true })
    await expect(confirmation).toBeVisible()
    expect(closeRequests).toEqual([])
    expect((await vista.api('/sessions')).filter((item: { id: string; open: boolean }) => [active.id, idle.id].includes(item.id) && item.open)).toHaveLength(2)
    await page.keyboard.press('Escape')
    await expect(confirmation).toBeHidden()
    await expect(manager).toBeVisible()
    await expect(activeCheckbox).toBeChecked()
    await expect(idleCheckbox).toBeChecked()
    expect((await readJob()).status).toBe('running')

    await closeSelected.click()
    await expect(confirmation).toBeVisible()
    const later = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Created during confirmation' }) })
    await confirmation.getByRole('button', { name: 'Close and stop', exact: true }).click()
    await expect(confirmation).toBeHidden()
    await expect.poll(async () => (await vista.api('/sessions')).filter((item: { id: string; open: boolean }) => [active.id, idle.id].includes(item.id) && item.open).length).toBe(0)
    expect(closeRequests.slice().sort()).toEqual([active.id, idle.id].sort())
    expect((await vista.api('/sessions')).filter((item: { id: string; open: boolean }) => [untouched.id, later.id].includes(item.id) && item.open)).toHaveLength(2)
    await expect.poll(async () => ['starting', 'running'].includes((await readJob()).status)).toBe(false)
    await expect(manager).toBeVisible()

    const modelCalls = vista.modelRequests.length
    await row.getByRole('button', { name: 'Open', exact: true }).click()
    await expect(page.locator('article.assistant').last()).toContainText('Process started.')
    expect(['starting', 'running']).not.toContain((await readJob()).status)
    expect(vista.modelRequests).toHaveLength(modelCalls)
  })
})
