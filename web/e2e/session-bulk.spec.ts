import { test, expect } from './fixtures'

test('bulk session actions respect visible selection, keep the manager open and isolate delete confirmation', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const first = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Bulk visible A' }) })
  const second = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Bulk visible B' }) })
  const untouched = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Unrelated session' }) })
  await page.goto(`${vista.origin}/sessions?session=${first.id}`)

  const manager = page.locator('.session-manager-page')
  const all = manager.getByRole('checkbox', { name: 'Select all shown', exact: true })
  const firstCheckbox = manager.getByRole('checkbox', { name: 'Select Bulk visible A', exact: true })
  const secondCheckbox = manager.getByRole('checkbox', { name: 'Select Bulk visible B', exact: true })
  const selectedCount = manager.locator('.session-bulk-actions [role="status"]')
  const rows = manager.locator('.managed-session')
  await expect(rows).toHaveCount(3)
  await manager.getByRole('textbox', { name: 'Search sessions', exact: true }).fill('Bulk visible')
  await expect(rows).toHaveCount(2)
  await all.check()
  await expect(selectedCount).toHaveText('2 selected')
  await expect(firstCheckbox).toBeChecked()
  await expect(secondCheckbox).toBeChecked()
  await manager.getByRole('button', { name: 'Clear selection', exact: true }).click()
  await expect(selectedCount).toHaveText('0 selected')
  await firstCheckbox.check()
  await expect(selectedCount).toHaveText('1 selected')
  await expect.poll(() => all.evaluate(input => (input as HTMLInputElement).indeterminate)).toBe(true)

  // Changing the search removes hidden selections instead of carrying them
  // into a later bulk action. "Select all" remains scoped to the shown rows.
  await manager.getByRole('textbox', { name: 'Search sessions', exact: true }).fill('Unrelated')
  await expect(selectedCount).toHaveText('0 selected')
  await manager.getByRole('textbox', { name: 'Search sessions', exact: true }).fill('Bulk visible')
  await all.check()
	await page.screenshot({ path: testInfo.outputPath('session-bulk-management.png'), animations: 'disabled' })

  let failSecondClose = true
  await page.route(`**/api/sessions/${second.id}/close`, async route => {
    if (failSecondClose) return route.fulfill({ status: 503, body: 'Controlled close failure' })
    return route.continue()
  })
  await manager.getByRole('button', { name: 'Close selected (2)', exact: true }).click()
  await expect(manager.locator('.session-batch-error')).toContainText('Controlled close failure')
  await expect(selectedCount).toHaveText('1 selected')
  await expect(firstCheckbox).not.toBeChecked()
  await expect(secondCheckbox).toBeChecked()
  await expect(page).toHaveURL(url => url.pathname === '/sessions' && !url.searchParams.has('session'))
  await expect.poll(async () => (await vista.api('/sessions')).filter((session: { id: string; open: boolean }) =>
    [first.id, second.id].includes(session.id) && session.open).length).toBe(1)
  failSecondClose = false
  await manager.getByRole('button', { name: 'Close selected (1)', exact: true }).click()
  await expect(selectedCount).toHaveText('0 selected')
  await expect(manager.locator('.session-batch-error')).toHaveCount(0)
  await expect.poll(async () => (await vista.api('/sessions')).filter((session: { id: string; open: boolean }) =>
    [first.id, second.id].includes(session.id) && session.open).length).toBe(0)
  await expect(manager).toBeVisible()

  await all.check()
  await manager.getByRole('button', { name: 'Open selected (2)', exact: true }).click()
  await expect(selectedCount).toHaveText('0 selected')
  await expect.poll(async () => (await vista.api('/sessions')).filter((session: { id: string; open: boolean }) =>
    [first.id, second.id].includes(session.id) && session.open).length).toBe(2)
  await expect(page).toHaveURL(url => url.pathname === '/sessions')
  await expect(manager).toBeVisible()

  await firstCheckbox.check()
  const deleteSelected = manager.getByRole('button', { name: 'Delete selected', exact: true })
  await deleteSelected.scrollIntoViewIfNeeded()
  const before = await manager.locator('.session-manager-content').evaluate(content => ({
    height: content.clientHeight, scrollHeight: content.scrollHeight, scrollTop: content.scrollTop,
  }))
  await deleteSelected.click()
  const confirmation = page.getByRole('dialog').filter({ hasText: 'Delete 1 selected sessions and their history?' })
  await expect(confirmation).toBeVisible()
  await expect(confirmation).toContainText('Delete 1 selected sessions and their history?')
  const geometry = await confirmation.evaluate(dialog => {
    const box = dialog.getBoundingClientRect()
    const content = document.querySelector('.session-manager-content')!
    return {
      left: box.left, right: box.right, top: box.top, bottom: box.bottom,
      width: innerWidth, height: innerHeight,
      inManager: !!dialog.closest('.session-manager-page'),
      contentHeight: content.clientHeight, contentScrollHeight: content.scrollHeight, contentScrollTop: content.scrollTop,
    }
  })
  expect(geometry.inManager).toBe(false)
  expect(geometry.left).toBeGreaterThanOrEqual(-1)
  expect(geometry.right).toBeLessThanOrEqual(geometry.width + 1)
  expect(geometry.top).toBeGreaterThanOrEqual(-1)
  expect(geometry.bottom).toBeLessThanOrEqual(geometry.height + 1)
  expect(geometry.contentHeight).toBe(before.height)
  expect(geometry.contentScrollHeight).toBe(before.scrollHeight)
  expect(geometry.contentScrollTop).toBe(before.scrollTop)
	await page.screenshot({ path: testInfo.outputPath('session-bulk-confirmation.png'), animations: 'disabled' })
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(confirmation).toBeHidden()
  expect(await vista.api('/sessions')).toHaveLength(3)
  await expect(firstCheckbox).toBeChecked()

  await deleteSelected.click()
  await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(confirmation).toBeHidden()
  await expect.poll(async () => (await vista.api('/sessions')).map((session: { id: string }) => session.id).sort()).toEqual([second.id, untouched.id].sort())
  await expect(selectedCount).toHaveText('0 selected')
  await expect(rows).toHaveCount(1)
  await expect(page).toHaveURL(url => url.pathname === '/sessions')
  await expect(manager).toBeVisible()
})
