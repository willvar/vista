import type { Locator, Page } from '@playwright/test'
import { test, expect } from './fixtures'

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

async function showSidebar(page: Page) {
  const trigger = page.locator('.mobile-sessions-trigger:visible')
  if (await trigger.count() && !await page.locator('.sessions-panel').evaluate(element => element.classList.contains('mobile-open'))) await trigger.click()
}
function sidebarRow(page: Page, id: string) { return page.locator(`.session-card[data-session-id="${id}"]`) }
async function menu(page: Page, row: Locator, name: string) {
  await row.getByRole('button', { name: 'Session menu', exact: true }).click()
  await page.locator('.ui-menu[data-state="open"]').getByRole('menuitem', { name, exact: true }).click()
}
async function rowIDs(rows: Locator) { return rows.evaluateAll(elements => elements.map(element => element.getAttribute('data-session-id'))) }

test('sidebar titles reclaim hidden action space while pinned and touch controls keep their room', { tag: '@responsive' }, async ({ page, vista, isMobile }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'A long conversation title that can use all available sidebar space' }) })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  await showSidebar(page)
  const row = sidebarRow(page, session.id)
  const title = row.locator('.inline-session-title')
  const textWidth = () => title.evaluate(element => element.clientWidth - parseFloat(getComputedStyle(element).paddingInlineEnd))
  await page.locator('.sidebar-section-label').hover()
  const idleWidth = await textWidth()
  await row.hover()
  const hoveredWidth = await textWidth()
  if (isMobile) expect(idleWidth).toBe(hoveredWidth)
  else expect(idleWidth - hoveredWidth).toBeGreaterThanOrEqual(64)
  const pin = row.locator('.session-pin')
  await pin.click()
  await expect(pin).toHaveAttribute('aria-pressed', 'true')
  await page.locator('.sidebar-section-label').hover()
  const pinnedWidth = await textWidth()
  if (isMobile) expect(pinnedWidth).toBe(idleWidth)
  else {
    expect(idleWidth - pinnedWidth).toBeGreaterThanOrEqual(32)
    expect(pinnedWidth - hoveredWidth).toBeGreaterThanOrEqual(32)
  }
  const textBox = await title.locator('.session-name').boundingBox()
  const pinBox = await pin.boundingBox()
  expect(textBox!.x + textBox!.width).toBeLessThanOrEqual(pinBox!.x)
  await row.locator('.session-menu').focus()
  await page.keyboard.press('Tab')
  await expect(pin).toBeFocused()
  expect(await textWidth()).toBe(hoveredWidth)
  await expect(row.locator('.session-menu')).toHaveCSS('opacity', '1')
})

test('executing sessions show progress in the pin slot or before touch actions and preserve controls', async ({ page, vista, isMobile }, info) => {
  const task = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Running task' }) })
  const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Current conversation' }) })
  const release = vista.pauseModelReplies()
  try {
    await page.goto(`${vista.origin}/sessions/${active.id}`)
    await showSidebar(page)
    await page.locator('.sidebar-section-label').click()
    const row = sidebarRow(page, task.id)
    const loading = row.locator('.session-execution-loading')
    const trigger = row.locator('.session-menu')
    const pin = row.locator('.session-pin')
    await expect(loading).toHaveCount(0)
    const reads: string[] = []
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'GET' && ['/api/sessions', '/api/status', '/api/models'].includes(path)) reads.push(path)
    })
    await vista.api(`/sessions/${task.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Keep working until released' }) })
    await expect(row.locator('.session-indicator.running')).toHaveText('Executing')
    await expect(loading).toBeVisible()
    await expect(loading).toHaveAttribute('aria-hidden', 'true')
    await expect(loading.locator('.ui-icon-spin')).toHaveCSS('animation-name', 'ui-spin')
    const loadingBox = (await loading.boundingBox())!
    const pinBox = (await pin.boundingBox())!
    const menuBox = (await trigger.boundingBox())!
    if (isMobile) {
      expect(loadingBox.x + loadingBox.width).toBeLessThanOrEqual(menuBox.x + 1)
      expect(menuBox.x + menuBox.width).toBeLessThanOrEqual(pinBox.x + 1)
      await expect(trigger).toHaveCSS('opacity', '1')
      await expect(pin).toHaveCSS('opacity', '1')
    } else {
      expect(loadingBox).toEqual(pinBox)
      await expect(trigger).toHaveCSS('opacity', '0')
      await expect(pin).toHaveCSS('opacity', '0')
    }
    const beforeHover = await row.boundingBox()
    await page.screenshot({ path: info.outputPath('session-executing.png'), animations: 'disabled' })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await expect(loading.locator('.ui-icon-spin')).toHaveCSS('animation-name', 'none')
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await row.hover()
    if (!isMobile) await expect(loading).toBeHidden()
    await expect(trigger).toHaveCSS('opacity', '1')
    await expect(pin).toHaveCSS('opacity', '1')
    expect(await row.boundingBox()).toEqual(beforeHover)
    await pin.click()
    await expect(pin).toHaveAttribute('aria-pressed', 'true')
    await trigger.click()
    const popup = page.getByRole('menu')
    await expect(popup).toBeVisible()
    await popup.getByRole('menuitem', { name: 'Rename', exact: true }).hover()
    if (!isMobile) await expect(loading).toBeHidden()
    await expect(pin).toHaveCSS('opacity', '1')
    await page.screenshot({ path: info.outputPath('session-executing-menu.png'), animations: 'disabled' })
    await page.locator('.sidebar-section-label').click()
    await expect(popup).toHaveCount(0)
    await expect(loading).toBeVisible()
    if (!isMobile) {
      await expect(pin).toHaveCSS('opacity', '0')
      await trigger.focus()
      await page.keyboard.press('Tab')
      await expect(pin).toBeFocused()
      await expect(pin).toHaveCSS('opacity', '1')
      await expect(loading).toBeHidden()
      await page.locator('.sidebar-section-label').click()
      await expect(loading).toBeVisible()
    }
    release()
    await expect(row.locator('.session-indicator.running')).toHaveCount(0)
    await expect(loading).toHaveCount(0)
    await expect(pin).toHaveAttribute('aria-pressed', 'true')
    await expect(pin).toHaveCSS('opacity', '1')
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
    expect(reads).toEqual([])
  } finally { release() }
})

test('pointer menu dismissal keeps focus quiet and keyboard navigation restores its indicator', async ({ page, vista, isMobile }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Menu focus' }) })
  await page.goto(vista.origin)
  for (const theme of ['light', 'dark']) {
    await page.evaluate(value => localStorage.setItem('vista-appearance', value), theme)
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await showSidebar(page)
    const trigger = sidebarRow(page, session.id).locator('.session-menu')
    if (isMobile) await trigger.tap()
    else await trigger.click()
    const popup = page.getByRole('menu')
    await expect(popup).toBeFocused()
    await expect(popup).toHaveCSS('outline-style', 'none')
    // Ark restores focus after dismissing on a non-focusable outside element.
    const outside = page.locator('.sidebar-section-label')
    if (isMobile) await outside.tap()
    else await outside.click()
    await expect(popup).toHaveCount(0)
    if (!isMobile) await expect(trigger).toBeFocused()
    await expect(trigger).toHaveCSS('outline-style', 'none')
    // Touch dismissal need not retain DOM focus; start keyboard traversal here.
    await trigger.focus()
    await expect(trigger).toHaveCSS('outline-style', 'none')
    await page.keyboard.press('Tab')
    const pin = sidebarRow(page, session.id).locator('.session-pin')
    await expect(pin).toBeFocused()
    await expect(pin).toHaveCSS('opacity', '1')
    await expect(pin).toHaveCSS('outline-style', 'solid')
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toBeFocused()
    await expect(trigger).toHaveCSS('outline-style', 'solid')
    await page.keyboard.press('ArrowDown')
    await expect(popup).toBeFocused()
    await expect(popup.getByRole('menuitem', { name: 'Rename', exact: true })).toHaveAttribute('data-highlighted', '')
    await page.keyboard.press('Escape')
    await expect(trigger).toBeFocused()
    await expect(trigger).toHaveCSS('opacity', '1')
    await expect(trigger).toHaveCSS('outline-style', 'solid')
    await trigger.click()
    await page.locator('.sidebar-section-label').click()
    await expect(trigger).toHaveCSS('outline-style', 'none')
  }
})

test('an open session menu keeps its row highlighted and resets pointer actions on dismissal', async ({ page, vista, isMobile }) => {
  const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Active conversation' }) })
  const target = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Menu target' }) })
  await page.goto(vista.origin)
  for (const theme of ['light', 'dark']) {
    await page.evaluate(value => localStorage.setItem('vista-appearance', value), theme)
    await page.goto(`${vista.origin}/sessions/${active.id}`)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await showSidebar(page)
    const row = sidebarRow(page, target.id)
    await row.hover()
    const hovered = await row.evaluate(element => getComputedStyle(element).backgroundColor)
    const trigger = row.locator('.session-menu')
    await trigger.click()
    const popup = page.getByRole('menu')
    await expect(popup).toBeVisible()
    await popup.getByRole('menuitem', { name: 'Rename', exact: true }).hover()
    await expect.poll(() => row.evaluate(element => element.matches(':hover'))).toBe(false)
    await expect(row).toHaveCSS('background-color', hovered)
    await expect(trigger).toHaveCSS('opacity', '1')
    await expect(row.locator('.session-pin')).toHaveCSS('opacity', '1')
    await expect(row).not.toHaveClass(/selected/)
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
    await page.locator('.sidebar-section-label').click()
    await expect(popup).toHaveCount(0)
    await expect(row).not.toHaveCSS('background-color', hovered)
    if (!isMobile) {
      await expect(trigger).toHaveCSS('opacity', '0')
      await expect(row.locator('.session-pin')).toHaveCSS('opacity', '0')
    }

    // Closing on the trigger retains genuine hover until the pointer leaves.
    await trigger.click()
    await expect(popup).toBeVisible()
    await trigger.click()
    await expect(popup).toHaveCount(0)
    await expect(row).toHaveCSS('background-color', hovered)
    await page.locator('.sidebar-section-label').hover()
    await expect(row).not.toHaveCSS('background-color', hovered)
    if (!isMobile) {
      await expect(trigger).toHaveCSS('opacity', '0')
      await expect(row.locator('.session-pin')).toHaveCSS('opacity', '0')
    }

    await menu(page, row, 'Delete')
    const confirmation = page.getByRole('dialog', { name: 'Delete session?', exact: true })
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(confirmation).toBeHidden()
    await page.locator('.sidebar-section-label').hover()
    await expect(row).not.toHaveCSS('background-color', hovered)
    if (!isMobile) {
      await expect(trigger).toHaveCSS('opacity', '0')
      await expect(row.locator('.session-pin')).toHaveCSS('opacity', '0')
    }
  }
})

test('pins keep the existing relative order in both lists and synchronize without refetching workspace state', async ({ page, vista, context }, info) => {
  for (const title of ['Plans', 'Research', 'Code review']) await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title }) })
  const original = await vista.api('/sessions') as Array<{ id: string; title: string }>
  const active = original[1]!
  const oldest = original.at(-1)!
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  await showSidebar(page)
  await expect(page.locator('.session-card')).toHaveCount(3)
  const other = await context.newPage()
  try {
    await other.goto(`${vista.origin}/sessions?session=${active.id}`)
    const managerRows = other.locator('.managed-session')
    await expect(managerRows).toHaveCount(3)
    await page.bringToFront()
    const reads: string[] = []
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'GET' && ['/api/sessions', '/api/status', '/api/models'].includes(path)) reads.push(path)
    })
    await sidebarRow(page, oldest.id).hover({ position: { x: 8, y: 8 } })
    const pin = sidebarRow(page, oldest.id).getByRole('button', { name: 'Pin session', exact: true })
    const menuBounds = await sidebarRow(page, oldest.id).locator('.session-menu').boundingBox()
    const pinBounds = await pin.boundingBox()
    expect(pinBounds!.x).toBeGreaterThan(menuBounds!.x)
    await pin.click()
    await expect.poll(() => rowIDs(page.locator('.session-card'))).toEqual([oldest.id, ...original.filter(item => item.id !== oldest.id).map(item => item.id)])
    await expect(managerRows.first()).toHaveAttribute('data-session-id', oldest.id)
    await expect(sidebarRow(page, oldest.id).locator('.session-pin')).toHaveAttribute('aria-pressed', 'true')
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
    // Pinning the newest after the oldest still uses creation order inside the group.
    // Move into the row before targeting its hover controls, dismissing the
    // preceding pin's tooltip even when sorting moved it under the pointer.
    await sidebarRow(page, original[0]!.id).hover({ position: { x: 8, y: 8 } })
    await sidebarRow(page, original[0]!.id).getByRole('button', { name: 'Pin session', exact: true }).click()
    const pinned = new Set([oldest.id, original[0]!.id])
    const expected = [...original.filter(item => pinned.has(item.id)), ...original.filter(item => !pinned.has(item.id))].map(item => item.id)
    await expect.poll(() => rowIDs(page.locator('.session-card'))).toEqual(expected)
    await expect.poll(() => rowIDs(managerRows)).toEqual(expected)
    expect(reads).toEqual([])
    await other.bringToFront()
    await other.locator(`.managed-session[data-session-id="${oldest.id}"]`).getByRole('button', { name: 'Unpin session', exact: true }).click()
    await expect.poll(() => rowIDs(page.locator('.session-card'))).toEqual(original.map(item => item.id))
    await expect(sidebarRow(page, oldest.id).locator('.session-pin')).toHaveAttribute('aria-pressed', 'false')
    expect(reads).toEqual([])
    await page.bringToFront()
    await page.reload()
    await showSidebar(page)
    await expect(sidebarRow(page, original[0]!.id).locator('.session-pin')).toHaveAttribute('aria-pressed', 'true')
    await page.screenshot({ path: info.outputPath('session-pins.png'), animations: 'disabled' })
  } finally { await other.close() }
})

test('closed pinned sessions reach the first manager page and keep their pin when reopened', async ({ page, vista }) => {
  test.setTimeout(60_000)
  for (let i = 0; i < 12; i++) {
    const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Archive ${i}` }) })
    await vista.api(`/sessions/${session.id}/close`, { method: 'POST' })
  }
  const original = (await vista.api('/sessions?page=1&pageSize=20')).items
  const oldest = original.at(-1)
  await vista.api(`/sessions/${oldest.id}`, { method: 'PATCH', body: JSON.stringify({ pinned: true }) })
  await page.goto(`${vista.origin}/sessions`)
  const row = page.locator('.managed-session').first()
  await expect(row).toHaveAttribute('data-session-id', oldest.id)
  await expect(row.getByRole('button', { name: 'Unpin session', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.list-pagination [role="status"]')).toHaveText('1–10 of 12')
  await row.locator('.managed-session-entry').click()
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${oldest.id}`)
  await showSidebar(page)
  await expect(sidebarRow(page, oldest.id).locator('.session-pin')).toHaveAttribute('aria-pressed', 'true')
})

test('header titles save on blur or Enter, preserve drafts across SSE and cancel without confirmation', async ({ page, vista }, info) => {
  const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Original title' }) })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const header = page.locator('.workspace:not(.modal-page) > .conversation-header')
  const edits: unknown[] = []
  page.on('request', request => { if (request.method() === 'PATCH' && new URL(request.url()).pathname === `/api/sessions/${session.id}`) edits.push(request.postDataJSON()) })
  const begin = async () => { await header.getByRole('button', { name: 'Rename', exact: true }).click(); return header.getByRole('textbox', { name: 'Session title', exact: true }) }
  const input = await begin()
  await expect(input).toBeFocused()
  await input.fill('My title draft')
  await vista.api(`/sessions/${session.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Remote update' }) })
  await expect(sidebarRow(page, session.id)).toContainText('Remote update')
  await expect(input).toHaveValue('My title draft')
  await page.locator('.sender-card textarea').click()
  await expect(header.locator('h2')).toHaveText('My title draft')
  expect(edits).toEqual([{ title: 'My title draft' }])
  const enter = await begin()
  await enter.fill('Confirmed with Enter')
  await enter.press('Enter')
  await expect(header.locator('h2')).toHaveText('Confirmed with Enter')
  expect(edits).toHaveLength(2)
  const escape = await begin()
  await escape.fill('Discard this draft')
  await escape.press('Escape')
  await expect(header.locator('h2')).toHaveText('Confirmed with Enter')
  const empty = await begin()
  await empty.fill('  ')
  await page.locator('.sender-card textarea').click()
  await expect(header.locator('h2')).toHaveText('Confirmed with Enter')
  const unchanged = await begin()
  await unchanged.press('Tab')
  await expect(header.locator('h2')).toHaveText('Confirmed with Enter')
  expect(edits).toHaveLength(2)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(header.getByRole('button', { name: 'Save changes', exact: true })).toHaveCount(0)
  await page.screenshot({ path: info.outputPath('session-title.png'), animations: 'disabled' })
})

test('sidebar rename edits the original title, preserves row activation and retries failed saves', async ({ page, vista }) => {
  const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Active session' }) })
  const target = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Other session' }) })
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  await showSidebar(page)
  const row = sidebarRow(page, target.id)
  await menu(page, row, 'Rename')
  const input = row.getByRole('textbox', { name: 'Session title', exact: true })
  await expect(input).toBeFocused()
  await input.pressSequentially('Renamed with spaces')
  await expect(input).toHaveValue('Renamed with spaces')
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
  // The next menu click both blurs the editor and retains its own action.
  await sidebarRow(page, active.id).locator('.session-menu').click()
  await expect(row.locator('.session-name')).toHaveText('Renamed with spaces')
  await expect(page.getByRole('menu')).toBeVisible()
  await page.keyboard.press('Escape')
  await menu(page, row, 'Rename')
  await input.fill('Retry this title')
  await page.route(`**/api/sessions/${target.id}`, route => route.request().method() === 'PATCH'
    ? route.fulfill({ status: 503, body: 'Synthetic save failure' }) : route.continue(), { times: 1 })
  await input.press('Enter')
  await expect(page.locator('main > .error')).toContainText('Synthetic save failure')
  await expect(input).toHaveValue('Retry this title')
  await expect(input).toBeEditable()
  await input.focus()
  await input.press('Enter')
  await expect(row.locator('.session-name')).toHaveText('Retry this title')
  await expect(page.locator('main > .error')).toHaveCount(0)
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('a delayed title save does not dismiss the next sidebar editor', async ({ page, vista }) => {
  const active = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Active session' }) })
  const first = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'First title' }) })
  const second = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Second title' }) })
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  await showSidebar(page)
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  let saving = false
  await page.route(`**/api/sessions/${first.id}`, async route => {
    saving = true
    await gate
    await route.continue()
  }, { times: 1 })
  try {
    await menu(page, sidebarRow(page, first.id), 'Rename')
    await sidebarRow(page, first.id).getByRole('textbox', { name: 'Session title', exact: true }).fill('First saved title')
    await menu(page, sidebarRow(page, second.id), 'Rename')
    const editor = sidebarRow(page, second.id).getByRole('textbox', { name: 'Session title', exact: true })
    await editor.fill('Keep this second draft')
    await expect.poll(() => saving).toBe(true)
    release()
    await expect(sidebarRow(page, first.id).locator('.session-name')).toHaveText('First saved title')
    await expect(editor).toHaveValue('Keep this second draft')
    await expect(editor).toBeFocused()
    await editor.press('Enter')
    await expect(sidebarRow(page, second.id).locator('.session-name')).toHaveText('Keep this second draft')
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
  } finally { release() }
})

test('remote pin changes preserve the active title editor until the user leaves it', async ({ page, vista }) => {
  for (const title of ['One', 'Two', 'Three']) await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title }) })
  const ordered = await vista.api('/sessions') as Array<{ id: string; title: string }>
  const target = ordered.at(-1)!
  await page.goto(`${vista.origin}/sessions/${ordered[0]!.id}`)
  await showSidebar(page)
  const row = sidebarRow(page, target.id)
  await menu(page, row, 'Rename')
  const editor = row.getByRole('textbox', { name: 'Session title', exact: true })
  await editor.fill('Keep typing this draft')
  let localSaves = 0
  page.on('request', request => { if (request.method() === 'PATCH' && request.postDataJSON()?.title) localSaves++ })
  await vista.api(`/sessions/${target.id}`, { method: 'PATCH', body: JSON.stringify({ pinned: true }) })
  await expect(row.locator('.session-pin')).toHaveAttribute('aria-pressed', 'true')
  await expect(editor).toBeFocused()
  await expect(editor).toHaveValue('Keep typing this draft')
  expect(localSaves).toBe(0)
  await editor.press('Escape')
  await expect(row.locator('.session-name')).toHaveText(target.title)
  await expect(page.locator('.session-card').first()).toHaveAttribute('data-session-id', target.id)
  expect(localSaves).toBe(0)
})

test('sidebar deletion uses a secondary modal and cancellation preserves the conversation', async ({ page, vista }, info) => {
  const session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Delete only after confirmation' }) })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  await showSidebar(page)
  const deletes: string[] = []
  page.on('request', request => { if (request.method() === 'DELETE') deletes.push(new URL(request.url()).pathname) })
  const row = sidebarRow(page, session.id)
  await menu(page, row, 'Delete')
  const confirmation = page.getByRole('dialog', { name: 'Delete session?', exact: true })
  await expect(confirmation).toContainText('Delete only after confirmation')
  await expect(confirmation).toContainText('history')
  expect(deletes).toEqual([])
  await confirmation.evaluate(element => {
    const copy = element.querySelector('.ui-modal-body')!
    ;(window as any).deleteCopies = []
    new MutationObserver(() => { if (element.getClientRects().length) (window as any).deleteCopies.push(copy.textContent) })
      .observe(element, { childList: true, subtree: true, characterData: true, attributes: true })
  })
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(confirmation).toBeHidden()
  await expect(row).toBeVisible()
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${session.id}`)
  await menu(page, row, 'Delete')
  await page.keyboard.press('Escape')
  await expect(confirmation).toBeHidden()
  expect(deletes).toEqual([])
  await menu(page, row, 'Delete')
  await expect(confirmation.getByRole('button', { name: 'Delete', exact: true })).toBeInViewport()
  await page.screenshot({ path: info.outputPath('session-delete.png'), animations: 'disabled' })
  await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(confirmation).toBeHidden()
  await expect(row).toHaveCount(0)
  await expect(page).toHaveURL(vista.origin + '/')
  expect(deletes).toEqual([`/api/sessions/${session.id}`])
  expect(await vista.api('/sessions')).toEqual([])
  const copies = await page.evaluate(() => (window as any).deleteCopies as string[])
  expect(copies.length).toBeGreaterThan(0)
  expect(copies.every(copy => copy.includes('Delete only after confirmation'))).toBe(true)
})
