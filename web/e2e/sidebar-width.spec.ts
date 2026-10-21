import { closeSettings } from './settings-navigation'
import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

const panelWidth = (page: Page) => page.locator('.sessions-panel').evaluate(element => element.getBoundingClientRect().width)
const dragger = (page: Page) => page.locator('.ant-splitter-bar-dragger:visible')

async function dragSidebarTo(page: Page, width: number) {
  // SplitBar reserves 300 ms after mousedown to recognize a double click.
  await page.waitForTimeout(350)
  const handle = dragger(page)
  await expect(handle).toBeVisible()
  const bounds = (await handle.boundingBox())!
  const current = await panelWidth(page)
  const x = bounds.x + bounds.width / 2
  const y = bounds.y + bounds.height / 2
  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.move(x + width - current, y, { steps: 12 })
  await page.mouse.up()
}

async function noPageOverflow(page: Page) {
  const bounds = await page.evaluate(() => ({ width: innerWidth, height: innerHeight, pageWidth: document.documentElement.scrollWidth, pageHeight: document.documentElement.scrollHeight }))
  expect(bounds.pageWidth).toBeLessThanOrEqual(bounds.width + 2)
  expect(bounds.pageHeight).toBeLessThanOrEqual(bounds.height + 2)
}

test('desktop sidebar drag respects limits, persists its width, resets, and leaves settings centered', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect.poll(() => panelWidth(page)).toBe(270)
  await dragSidebarTo(page, 390)
  await expect.poll(() => panelWidth(page)).toBe(390)
  await expect.poll(() => page.evaluate(() => Number(localStorage.getItem('vista-sidebar-width')))).toBe(390)
  await page.reload()
  await expect.poll(() => panelWidth(page)).toBe(390)
  await page.screenshot({ path: testInfo.outputPath('sidebar-custom-width.png'), animations: 'disabled' })

  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  const settings = page.locator('.settings-page:visible')
  await expect(settings).toBeVisible()
  await settings.evaluate(async element => {
    const modal = element.closest('.ant-modal-wrap') || element
    await Promise.allSettled(modal.getAnimations({ subtree: true }).map(animation => animation.finished))
  })
  const modal = await settings.evaluate(element => {
    const bounds = element.closest('.ant-modal-container')!.getBoundingClientRect()
    return { x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height, viewportWidth: innerWidth, viewportHeight: innerHeight }
  })
  expect(Math.abs(modal.x + modal.width / 2 - modal.viewportWidth / 2)).toBeLessThanOrEqual(2)
  expect(Math.abs(modal.y + modal.height / 2 - modal.viewportHeight / 2)).toBeLessThanOrEqual(2)
  await noPageOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('sidebar-settings-centered.png'), animations: 'disabled' })
  await closeSettings(page)

  await dragSidebarTo(page, 750)
  await expect.poll(() => panelWidth(page)).toBe(480)
  await dragSidebarTo(page, 100)
  await expect.poll(() => panelWidth(page)).toBe(220)
  await dragger(page).dblclick()
  await expect.poll(() => panelWidth(page)).toBe(270)
  await page.reload()
  await expect.poll(() => panelWidth(page)).toBe(270)
  await page.setViewportSize({ width: 900, height: 700 })
  await expect.poll(() => panelWidth(page)).toBe(240)
  await dragSidebarTo(page, 700)
  await expect.poll(() => panelWidth(page)).toBe(420)
  await expect.poll(() => page.locator('.workspace:not(.modal-page)').evaluate(element => element.getBoundingClientRect().width)).toBeGreaterThanOrEqual(480)
  await dragger(page).dblclick()
  await expect.poll(() => panelWidth(page)).toBe(240)
  await noPageOverflow(page)
})

test('desktop width and the mounted conversation survive portrait and landscape drawers', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const textarea = page.locator('.sender-card textarea')
  await expect(textarea).toBeEnabled()
  const draft = 'Preserve the draft while resizing\n横竖屏切换不会丢失内容。'
  await textarea.fill(draft)
  const mountedTextarea = (await textarea.elementHandle())!
  await dragSidebarTo(page, 410)
  await expect.poll(() => panelWidth(page)).toBe(410)
  for (const [name, viewport] of [['portrait', { width: 390, height: 844 }], ['landscape', { width: 568, height: 320 }]] as const) {
    await page.setViewportSize(viewport)
    await expect(dragger(page)).toHaveCount(0)
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${session.id}`)
    await expect(textarea).toHaveValue(draft)
    expect(await mountedTextarea.evaluate(element => element.isConnected && element === document.querySelector('.sender-card textarea')), 'Responsive layout must retain the mounted composer').toBe(true)
    await noPageOverflow(page)
    await page.locator('.mobile-sessions-trigger:visible').click()
    const panel = page.locator('.sessions-panel')
    await expect(panel).toHaveClass(/mobile-open/)
    await expect(panel.locator('.session-card.selected')).toBeVisible()
    await expect.poll(() => panel.evaluate(element => Math.round(element.getBoundingClientRect().x))).toBe(0)
    const box = (await panel.boundingBox())!
    expect(box.width).toBeLessThanOrEqual(viewport.width)
    expect(box.height).toBeLessThanOrEqual(viewport.height + 1)
    await noPageOverflow(page)
    await page.screenshot({ path: testInfo.outputPath(`sidebar-${name}-drawer.png`), animations: 'disabled' })
    await panel.getByRole('button', { name: 'Close session list', exact: true }).click()
    await expect(panel).not.toHaveClass(/mobile-open/)
    await expect(textarea).toHaveValue(draft)
    await noPageOverflow(page)
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(dragger(page)).toBeVisible()
  await expect.poll(() => panelWidth(page)).toBe(410)
  expect(await mountedTextarea.evaluate(element => element.isConnected && element === document.querySelector('.sender-card textarea'))).toBe(true)
  await expect(textarea).toHaveValue(draft)
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${session.id}`)
  await noPageOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('sidebar-restored-desktop.png'), animations: 'disabled' })
})
