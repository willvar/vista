import { test, expect } from './fixtures'

test.use({ backgroundProcess: true })

test('real process survives foreground completion, persists metadata and logs, and stops from the monitor', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  await page.locator('.sender-card textarea').fill('Start process')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(page.locator('article.assistant').last()).toContainText('Process started.')
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  const readJobs = () => vista.api(`/sessions/${id}/jobs`)
  const [job] = await readJobs()
  expect(job.status).toBe('running')
  expect(job.startedAt).toBeTruthy()
  expect(job.expiresAt).toBeTruthy()
  const sessionRow = page.getByRole('button', { name: /Open session/ }).first()
  if (testInfo.project.name === 'desktop') {
    await expect(sessionRow).toContainText('Background running')
    await expect(sessionRow).not.toContainText('Executing')
  }
  await page.getByRole('button', { name: 'Background processes 1', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Background processes', exact: true })
  const output = dialog.locator('.process-output-viewport')
  await expect(output).toContainText('monitor ready')
  const bytes = (await readJobs())[0].bytes
  await expect.poll(async () => (await readJobs())[0].bytes).toBeGreaterThan(bytes)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  expect((await readJobs())[0].status).toBe('running')
  // Closing the view leaves the process alive, including across page reloads.
  await page.reload()
  await page.getByRole('button', { name: 'Background processes 1', exact: true }).click()
  await expect(output).toContainText('live tick')
  await expect(dialog).toContainText('Expires')
  await dialog.getByRole('button', { name: 'Stop process', exact: true }).click()
  await expect(dialog.locator('.process-state')).toHaveText('Stopped')
  await expect.poll(async () => (await readJobs())[0].terminationReason).toBe('stopped')
  await expect(dialog).toContainText('SIGKILL')
  await page.reload()
  await page.getByRole('button', { name: 'Background processes 0', exact: true }).click()
  await expect(output).toContainText('monitor ready')
  await expect(dialog.locator('.process-state')).toHaveText('Stopped')
  await page.screenshot({ path: testInfo.outputPath('persisted-process.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  // A failed shell command still returns its evidence to the model, which
  // handles the failure in a subsequent model request.
  await page.locator('.sender-card textarea').fill('Fail command')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(page.locator('article.assistant').last()).toContainText('Handled exit 7 with command evidence.')
  const artifacts = await vista.api(`/sessions/${id}/artifacts`)
  expect(artifacts.some((artifact: { status: string; error?: string }) => artifact.status === 'failed' && artifact.error?.includes('exit code 7'))).toBe(true)
})
