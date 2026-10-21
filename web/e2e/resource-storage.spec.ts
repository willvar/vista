import { openSettingsSection } from './settings-navigation'
import { createHash } from 'node:crypto'
import { test, expect } from './fixtures'

test.use({ imageModel: true })

test('originals survive compaction and closing, and become cleanable only after the last owner is deleted', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const original = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=', 'base64')
  const hash = createHash('sha256').update(original).digest('hex')
  const first = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const second = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const idle = async (id: string) => expect.poll(async () => (await vista.api('/sessions')).find((item: any) => item.id === id)?.executing).toBe(false)
  const send = async (id: string, content: string, name?: string) => {
    await vista.api(`/sessions/${id}/messages`, { method: 'POST', body: JSON.stringify({ content,
      images: name ? [{ mediaType: 'image/png', name, data: original.toString('base64') }] : [],
    }) })
    await idle(id)
  }
  await send(first.id, 'Inspect original details. ' + 'Keep verified details. '.repeat(180), 'first.png')
  await send(first.id, 'Keep working')
  const messages = await vista.api(`/sessions/${first.id}/messages`)
  await page.goto(`${vista.origin}/sessions/${first.id}`)
  await page.getByRole('button', { name: 'Conversation options', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Compact context', exact: true }).click()
  await expect.poll(async () => (await vista.api(`/sessions/${first.id}/context`))?.phase).toBe('compacted')
  expect(await vista.api(`/sessions/${first.id}/messages`)).toEqual(messages)
  await page.reload()
  const thumbnail = page.locator('article.user .message-images img')
  await expect(thumbnail).toHaveCount(1)
  await thumbnail.scrollIntoViewIfNeeded()
  await expect.poll(() => thumbnail.evaluate(image => (image as HTMLImageElement).naturalWidth)).toBe(1)
  const retained = await page.request.get(`${vista.origin}/api/sessions/${first.id}/resources/${hash}`)
  expect(retained.status()).toBe(200)
  expect(await retained.body()).toEqual(original)
  await send(second.id, 'Same original, another session', 'second.png')
  await vista.api(`/sessions/${second.id}/close`, { method: 'POST', body: '{}' })
  let interruptedPolls = 0
  await page.route('**/api/resource-storage', route => {
    if (route.request().method() === 'GET' && interruptedPolls === 0) {
      interruptedPolls++
      return route.fulfill({ status: 503, body: 'Temporary status connection failure' })
    }
    return route.continue()
  })
  await page.goto(`${vista.origin}/settings/general`)
  await openSettingsSection(page, 'storage')
  const cards = page.locator('.storage-card')
  await expect(cards).toHaveCount(3)
  await expect(cards.nth(0)).toContainText('1 file')
  await expect(cards.nth(1)).toContainText('1 file')
  await expect(cards.nth(2)).toContainText('0 files')
  expect(interruptedPolls).toBe(1)
  const clean = page.getByRole('button', { name: 'Clean unreferenced resources', exact: true })
  await expect(clean).toBeDisabled()
  await vista.api(`/sessions/${first.id}`, { method: 'DELETE' })
  await page.getByRole('button', { name: 'Refresh usage', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Refresh usage', exact: true })).toBeEnabled()
  await expect(clean).toBeDisabled()
  const shared = await page.request.get(`${vista.origin}/api/sessions/${second.id}/resources/${hash}`)
  expect(shared.status()).toBe(200)
  expect(await shared.body()).toEqual(original)
  await vista.api(`/sessions/${second.id}`, { method: 'DELETE' })
  await page.getByRole('button', { name: 'Refresh usage', exact: true }).click()
  await expect(cards.nth(1)).toContainText('0 files')
  await expect(cards.nth(2)).toContainText('1 file')
  await expect(clean).toBeEnabled()
  for (const theme of ['light', 'dark']) {
    await page.evaluate(value => localStorage.setItem('vista-appearance', value), theme)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(clean).toBeEnabled()
    await clean.scrollIntoViewIfNeeded()
    const layout = await page.locator('.resource-storage-settings').evaluate(element => {
      const box = element.getBoundingClientRect()
      return { left: box.left, right: box.right, width: innerWidth, overflow: element.scrollWidth - element.clientWidth }
    })
    expect(layout.left).toBeGreaterThanOrEqual(0)
    expect(layout.right).toBeLessThanOrEqual(layout.width)
    expect(layout.overflow).toBeLessThanOrEqual(2)
    await page.screenshot({ path: testInfo.outputPath(`resource-storage-${theme}.png`), animations: 'disabled' })
  }
  await clean.click()
  await expect(page.locator('.storage-progress')).toContainText(`Cleaned 1 file and freed ${original.length} B.`)
  await expect(cards.nth(0)).toContainText('0 files')
  await expect(clean).toBeDisabled()
  await page.reload()
  await expect(cards.nth(0)).toContainText('0 files')
})
