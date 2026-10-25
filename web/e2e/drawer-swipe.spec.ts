import type { Locator, Page } from '@playwright/test'
import { test, expect } from './fixtures'

type Session = { id: string; title: string; open: boolean }
type Vista = { origin: string; api: (path: string, options?: RequestInit) => Promise<any> }

test.beforeEach(async ({ isMobile }) => {
  test.skip(!isMobile, 'The session drawer is a mobile interaction')
})

async function prepareSessions(page: Page, vista: Vista, extraRows = 0) {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const active: Session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Current conversation' }) })
  const other: Session = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Other conversation' }) })
  const rows = [active, other, ...Array.from({ length: extraRows }, (_, index) => ({
    ...other, id: `scroll-fixture-${index}`, title: `Scrollable conversation ${index + 1}`,
  }))]
  // Keep both real conversations in a stable order; the extra rows only supply scrollable content.
  await page.route('**/api/sessions?open=true', route => route.request().method() === 'GET'
    ? route.fulfill({ json: rows }) : route.continue())
  await page.goto(`${vista.origin}/sessions/${active.id}`)
  await expect(page.locator('.conversation-header h2')).toHaveText(active.title)
  await expect(page.locator('.sender-card textarea')).toBeEnabled()
  return { active, other }
}

async function openDrawer(page: Page) {
  const panel = page.locator('.sessions-panel')
  await page.locator('.mobile-sessions-trigger:visible').tap()
  await expect(panel).toHaveClass(/mobile-open/)
  await expect.poll(() => panel.evaluate(element => Math.round(element.getBoundingClientRect().x))).toBe(0)
  return panel
}

async function touchInput(page: Page) {
  const cdp = await page.context().newCDPSession(page)
  // Browser touch input exercises touch-action, native scrolling, pointer cancellation and clicks.
  const send = (type: 'touchStart' | 'touchMove' | 'touchEnd', x = 0, y = 0) => cdp.send('Input.dispatchTouchEvent', {
    type, touchPoints: type === 'touchEnd' ? [] : [{ x, y, radiusX: 1, radiusY: 1, force: 1, id: 1 }],
  })
  return {
    start: (x: number, y: number) => send('touchStart', x, y),
    move: (x: number, y: number) => send('touchMove', x, y),
    end: () => send('touchEnd'),
  }
}

async function visibleCardPoint(page: Page, card: Locator) {
  await card.scrollIntoViewIfNeeded()
  const box = (await card.boundingBox())!
  const list = (await page.locator('.sessions-panel ul').boundingBox())!
  const top = Math.max(box.y, list.y)
  const bottom = Math.min(box.y + box.height, list.y + list.height)
  expect(bottom - top, 'The gesture must start on visible card content').toBeGreaterThan(10)
  return { x: box.x + box.width - 55, y: (top + bottom) / 2 }
}

async function backdropStrength(page: Page) {
  return page.locator('.mobile-sessions-backdrop').evaluate(element => {
    const style = getComputedStyle(element)
    const color = style.backgroundColor.match(/[\d.]+/g)!.map(Number)
    return Number(style.opacity) * (color[3] ?? 1)
  })
}

async function expectCurrentSession(page: Page, vista: Vista, active: Session, other: Session) {
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${active.id}`)
  await expect(page.locator('.session-card.selected .session-name')).toHaveText(active.title)
  const sessions: Session[] = await vista.api('/sessions')
  expect(sessions.map(({ id, open }) => ({ id, open })).sort((a, b) => a.id.localeCompare(b.id))).toEqual(
    [active, other].map(({ id }) => ({ id, open: true })).sort((a, b) => a.id.localeCompare(b.id)),
  )
}

test('a slow short left drag follows the finger, fades the backdrop, and springs back', async ({ page, vista }, testInfo) => {
  const { active, other } = await prepareSessions(page, vista)
  const panel = await openDrawer(page)
  const card = panel.locator('.session-card').filter({ hasText: other.title })
  const start = await visibleCardPoint(page, card)
  const touch = await touchInput(page)
  const initialBackdrop = await backdropStrength(page)
  await touch.start(start.x, start.y)
  await page.waitForTimeout(100)
  await touch.move(start.x - 30, start.y + 1)
  await expect.poll(() => panel.evaluate(element => element.getBoundingClientRect().x)).toBeLessThan(-15)
  const firstX = await panel.evaluate(element => element.getBoundingClientRect().x)
  await page.waitForTimeout(100)
  await touch.move(start.x - 60, start.y + 2)
  await expect.poll(() => panel.evaluate(element => element.getBoundingClientRect().x)).toBeLessThan(firstX - 15)
  const draggedX = await panel.evaluate(element => element.getBoundingClientRect().x)
  expect(draggedX).toBeGreaterThan(-80)
  expect(await backdropStrength(page)).toBeLessThan(initialBackdrop)
  expect(await backdropStrength(page)).toBeGreaterThan(0)
  await page.screenshot({ path: testInfo.outputPath('drawer-following-finger.png') })
  // Stop moving before release so distance, rather than a fling, decides the result.
  await page.waitForTimeout(250)
  await touch.end()
  await expect(panel).toHaveClass(/mobile-open/)
  await expect.poll(() => panel.evaluate(element => Math.round(element.getBoundingClientRect().x))).toBe(0)
  await expect.poll(() => backdropStrength(page)).toBeCloseTo(initialBackdrop, 2)
  await expectCurrentSession(page, vista, active, other)
})

test('a long left drag and a short fast fling both close without activating a card', async ({ page, vista }) => {
  const { active, other } = await prepareSessions(page, vista)
  const touch = await touchInput(page)
  for (const fast of [false, true]) {
    const panel = await openDrawer(page)
    const start = await visibleCardPoint(page, panel.locator('.session-card').filter({ hasText: other.title }))
    await touch.start(start.x, start.y)
    const distance = fast ? 70 : 200
    for (let step = 1; step <= (fast ? 2 : 4); step++) {
      await page.waitForTimeout(fast ? 12 : 85)
      await touch.move(start.x - distance * step / (fast ? 2 : 4), start.y + 1)
    }
    if (!fast) await page.waitForTimeout(200)
    await touch.end()
    await expect(panel, fast ? 'A short fast fling closes the drawer' : 'A long slow drag closes the drawer').not.toHaveClass(/mobile-open/)
    await expect(page.locator('.mobile-sessions-backdrop')).toHaveCount(0)
    await expect.poll(() => panel.evaluate(element => element.getBoundingClientRect().right)).toBeLessThanOrEqual(0)
    await expectCurrentSession(page, vista, active, other)
  }
})

test('dragging from a close button preserves the session and the next tap still activates it', async ({ page, vista }) => {
  const { active, other } = await prepareSessions(page, vista)
  const panel = await openDrawer(page)
  const card = panel.locator('.session-card').filter({ hasText: other.title })
  const close = card.locator('.session-close')
  await close.scrollIntoViewIfNeeded()
  const button = (await close.boundingBox())!
  const start = { x: button.x + button.width / 2, y: button.y + button.height / 2 }
  const touch = await touchInput(page)
  await touch.start(start.x, start.y)
  await page.waitForTimeout(100)
  await touch.move(start.x - 30, start.y)
  await page.waitForTimeout(100)
  await touch.move(start.x - 60, start.y)
  await page.waitForTimeout(250)
  await touch.end()
  await expect(panel).toHaveClass(/mobile-open/)
  await expect.poll(() => panel.evaluate(element => Math.round(element.getBoundingClientRect().x))).toBe(0)
  await expectCurrentSession(page, vista, active, other)
  await card.locator('.session-name').tap()
  await expect(page).toHaveURL(url => url.pathname === `/sessions/${other.id}`)
  await expect(page.locator('.conversation-header h2')).toHaveText(other.title)
  await expect(panel).not.toHaveClass(/mobile-open/)
  await expect(page.locator('.sender-card textarea')).toBeFocused()
})

test('vertical touch scrolling keeps the drawer open without switching or closing sessions', async ({ page, vista }) => {
  const { active, other } = await prepareSessions(page, vista, 24)
  const panel = await openDrawer(page)
  const list = panel.locator('ul')
  // Short windows scroll the whole panel; taller ones scroll only its list.
  const panelScrolls = await panel.evaluate(element => ['auto', 'scroll'].includes(getComputedStyle(element).overflowY))
  const scroller = panelScrolls ? panel : list
  await scroller.evaluate(element => { element.scrollTop = 0 })
  const box = (await scroller.boundingBox())!
  const listBox = (await list.boundingBox())!
  expect(await scroller.evaluate(element => element.scrollHeight)).toBeGreaterThan(box.height)
  const bottom = Math.min(box.y + box.height, listBox.y + listBox.height) - 8
  const top = Math.max(box.y, listBox.y) + 8
  const start = { x: box.x + box.width / 2, y: bottom }
  const distance = Math.min(220, bottom - top)
  expect(distance).toBeGreaterThan(25)
  const touch = await touchInput(page)
  await touch.start(start.x, start.y)
  for (let step = 1; step <= 5; step++) {
    await page.waitForTimeout(30)
    await touch.move(start.x - 2, start.y - distance * step / 5)
  }
  await touch.end()
  await expect.poll(() => scroller.evaluate(element => element.scrollTop)).toBeGreaterThan(15)
  await expect(panel).toHaveClass(/mobile-open/)
  await expect.poll(() => panel.evaluate(element => Math.round(element.getBoundingClientRect().x))).toBe(0)
  await expectCurrentSession(page, vista, active, other)
})
