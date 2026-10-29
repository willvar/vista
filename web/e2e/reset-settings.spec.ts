import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { test, expect } from './fixtures'

test('reset is debug-only, requires confirmation, and retains a visible retry after failure', async ({ page, vista }, testInfo) => {
  await page.goto(vista.origin + '/settings/about')
  const entry = page.locator('.reset-vista-setting')
  const debug = page.getByRole('switch', { name: 'Debug mode', exact: true })
  await expect(entry).toHaveCount(0)
  await debug.click()
  await expect(entry).toBeVisible()
  expect(await entry.evaluate(element => element.previousElementSibling?.classList.contains('debug-mode-setting'))).toBe(true)
  let calls = 0
  await page.route('**/api/reset', async route => {
    calls++
    expect(route.request().method()).toBe('POST')
    expect(route.request().postDataJSON()).toEqual({ confirm: 'reset-all' })
    await route.fulfill({ status: 500, body: 'Reset could not complete. Retry reset.' })
  })
  const button = entry.getByRole('button', { name: 'Reset Vista', exact: true })
  const dialog = page.getByRole('dialog', { name: 'Reset Vista?', exact: true })
  await button.click()
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('This cannot be undone.')
  const color = await dialog.getByRole('button', { name: 'Delete everything and reset', exact: true })
    .evaluate(element => getComputedStyle(element).backgroundColor)
  const [red, green, blue] = color.match(/\d+/g)!.map(Number)
  expect(red).toBeGreaterThan(green!)
  expect(red).toBeGreaterThan(blue!)
  expect(calls).toBe(0)
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  expect(calls).toBe(0)
  await button.click()
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
  await expect(page.locator('.settings-page')).toBeVisible()
  await button.click()
  await dialog.getByRole('button', { name: 'Delete everything and reset', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Reset could not complete. Retry reset.')
  await expect(dialog.getByRole('button', { name: 'Delete everything and reset', exact: true })).toBeEnabled()
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeEnabled()
  expect(calls).toBe(1)
  expect((await vista.api('/models')).models.some((model: { key: string }) => model.key === 'test')).toBe(true)
  const bounds = await dialog.evaluate(element => {
    const rect = element.getBoundingClientRect()
    return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(bounds.right).toBeLessThanOrEqual(bounds.width)
  expect(bounds.top).toBeGreaterThanOrEqual(0)
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.height)
  await page.screenshot({ path: testInfo.outputPath('reset-confirmation.png'), animations: 'disabled' })
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await debug.click()
  await expect(entry).toHaveCount(0)
})

test.describe('full reset', () => {
  test.use({ backgroundProcess: true, imageModel: true })

  test('stops a real Worker and background process, deletes data and restarts the runtime', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    const image = await vista.api(`/sessions/${session.id}/resources?name=reset.png`, { method: 'POST', headers: { 'Content-Type': 'image/png' }, body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=', 'base64') })
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({
      content: 'Keep this image', images: [image],
    }) })
    await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id)?.executing).toBe(false)
    await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Start process' }) })
    await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === session.id)?.executing).toBe(false)
    await expect.poll(async () => (await vista.api('/status')).managedBackgroundProcesses).toBe(1)
    const [job] = await vista.api(`/sessions/${session.id}/jobs`)
    const pid = (await vista.api('/status')).workerMetrics[0].pid
    expect(existsSync(job.outputPath)).toBe(true)
    const config = resolve(vista.workspacePath, '../vista')
    const skill = join(config, 'skills/reset-example')
    mkdirSync(skill, { recursive: true })
    writeFileSync(join(skill, 'SKILL.md'), '---\nname: reset-example\ndescription: Test skill\n---\nTest instructions.')
    await vista.api('/agent-settings', { method: 'PUT', body: JSON.stringify({ instructions: 'Old global instructions.' }) })
    await vista.api('/user-agent', { method: 'PUT', body: JSON.stringify({ preset: 'custom', custom: 'OldClient/1' }) })
    const outside = join(vista.workspacePath, 'keep.txt')
    writeFileSync(outside, 'user file')
    await page.goto(vista.origin + '/settings/about')
    await page.evaluate(() => {
      localStorage.setItem('vista-session-draft:old', 'old private draft')
      sessionStorage.setItem('vista-question:old:run', 'old answer')
      localStorage.setItem('unrelated-app', 'keep')
    })
    await page.getByRole('switch', { name: 'Debug mode', exact: true }).click()
    await page.locator('.reset-vista-setting').getByRole('button', { name: 'Reset Vista', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Reset Vista?', exact: true })
    // Hold only the browser response so the progress state can be checked even
    // when the isolated native reset itself completes very quickly.
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    await page.route('**/api/reset', async route => {
      const result = await route.fetch()
      await gate
      await route.fulfill({ response: result })
    })
    // Simulate drafts already present at the destination origin after a port
    // change. The startup reset must remove them before the application loads.
    await page.addInitScript(() => { localStorage.setItem('vista-stale-destination-draft', 'old draft') })
    try {
      await dialog.getByRole('button', { name: 'Delete everything and reset', exact: true }).click()
      await expect(dialog.getByRole('status')).toContainText('Stopping tasks and resetting Vista')
      await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
      await page.keyboard.press('Escape')
      await expect(dialog).toBeVisible()
    } finally { release() }
    await expect(page).toHaveURL(vista.origin + '/')
    await expect(page.locator('.workspace.welcome')).toBeVisible()
    await expect(page.locator('.settings-page')).toHaveCount(0)
    expect(await vista.api('/sessions')).toEqual([])
    const status = await vista.api('/status')
    expect(status.sessions.workers).toBe(0)
    expect(status.managedBackgroundProcesses).toBe(0)
    expect(status.address).toBe(vista.origin)
    expect(existsSync(job.outputPath)).toBe(false)
    expect(existsSync(skill)).toBe(false)
    expect((await vista.api('/agent-settings')).instructions).toBe('')
    expect((await vista.api('/user-agent')).preset).toBe('vista')
    expect(readFileSync(outside, 'utf8')).toBe('user file')
    await expect.poll(() => { try { process.kill(pid, 0); return true } catch { return false } }).toBe(false)
    const browser = await page.evaluate(() => ({
      draft: localStorage.getItem('vista-session-draft:old'), question: sessionStorage.getItem('vista-question:old:run'),
      destinationDraft: localStorage.getItem('vista-stale-destination-draft'),
      debug: localStorage.getItem('vista-debug-mode'), other: localStorage.getItem('unrelated-app'), cookie: document.cookie,
    }))
    expect(browser.draft).toBeNull()
    expect(browser.question).toBeNull()
    expect(browser.destinationDraft).toBeNull()
    expect(browser.debug).toBeNull()
    expect(browser.other).toBe('keep')
    expect(browser.cookie).not.toContain('vista-debug-mode=true')
    expect(browser.cookie).not.toContain('vista-reset-pending')
    await page.goto(vista.origin + '/settings/about')
    await expect(page.getByRole('switch', { name: 'Debug mode', exact: true })).not.toBeChecked()
    await expect(page.locator('.reset-vista-setting')).toHaveCount(0)
  })
})
