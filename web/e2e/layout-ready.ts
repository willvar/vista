import { expect, type Locator, type Page } from '@playwright/test'

// Wait for observable layout work, rather than assuming it always takes 350 ms.
// Infinite progress animations must not hold up geometry checks.
export async function settleLayout(page: Page) {
  await page.evaluate(async () => {
    const frame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    await document.fonts.ready
    await frame(); await frame()
    await Promise.all(document.getAnimations().filter(animation => animation.playState === 'running'
      && animation.effect?.getTiming().iterations !== Infinity).map(animation => animation.finished.catch(() => {})))
    await frame(); await frame()
  })
}

export async function expectFieldPair(page: Page, first: Locator, second: Locator) {
  await expect(first).toBeVisible()
  await expect(second).toBeVisible()
  await settleLayout(page)
  const bounds = (field: Locator) => field.evaluate(element => {
    // Password and number inputs sit inside their visible control border.
    const box = (element.closest('.ui-input-affix-wrapper, .ui-input-number, .ui-select') || element).getBoundingClientRect()
    return { x: box.x, y: box.y, width: box.width, height: box.height }
  })
  const [left, right] = await Promise.all([bounds(first), bounds(second)])
  if (page.viewportSize()!.width > 600) {
    expect(Math.abs(left.y - right.y)).toBeLessThan(2)
    expect(right.x).toBeGreaterThanOrEqual(left.x + left.width)
  } else {
    expect(right.y).toBeGreaterThanOrEqual(left.y + left.height)
  }
  for (const box of [left, right]) {
    expect(box.width).toBeGreaterThan(100)
    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  }
}

export async function expectHeaderRow(page: Page, row: Locator) {
  const name = row.getByLabel('Header name', { exact: true }), value = row.getByLabel('Header value', { exact: true })
  await expectFieldPair(page, name, value)
  await expect(name).toHaveAttribute('placeholder', 'Header name *')
  await expect(name).toHaveAttribute('aria-required', 'true')
  await expect(value).toHaveAttribute('placeholder', 'Header value')
  await expect(value).toHaveAttribute('type', 'password')
  await expect(row.locator('.schema-control > label:not(.visually-hidden)')).toHaveCount(0)
  const actions = row.locator('.schema-array-actions')
  await expect(actions).toBeVisible()
  const [field, buttons] = await Promise.all([name.boundingBox(), actions.boundingBox()])
  if (page.viewportSize()!.width > 480) {
    expect(Math.abs(field!.y - buttons!.y)).toBeLessThan(2)
    expect(buttons!.x).toBeGreaterThan(field!.x + field!.width)
  }
  expect(buttons!.x + buttons!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
}
