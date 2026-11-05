import type { Locator } from '@playwright/test'
import { test, expect } from './fixtures'

async function controlAppearance(control: Locator) {
  await expect(control).toBeVisible()
  await control.evaluate(async element => { await Promise.allSettled(element.closest('[role="dialog"]')!.getAnimations().map(animation => animation.finished)) })
  await control.page().mouse.move(0, 0)
  await control.evaluate(async element => { await Promise.allSettled(element.getAnimations().map(animation => animation.finished)) })
  const appearance = await control.evaluate(element => {
    const style = getComputedStyle(element)
    const bounds = element.getBoundingClientRect()
    const header = element.closest('.ui-modal-header')!.getBoundingClientRect()
    return {
      width: bounds.width, height: bounds.height,
      padding: style.padding, radius: style.borderRadius, fontSize: style.fontSize,
      color: style.color,
      alignment: Math.abs(bounds.y + bounds.height / 2 - header.y - header.height / 2),
    }
  })
  expect(appearance.alignment, 'Header controls are vertically centered').toBeLessThanOrEqual(1)
  await control.hover()
  // Button colors transition between idle and hover.
  await control.evaluate(async element => { await Promise.allSettled(element.getAnimations().map(animation => animation.finished)) })
  const hover = await control.evaluate(element => {
    const style = getComputedStyle(element)
    return { color: style.color, background: style.backgroundColor, border: style.borderColor }
  })
  return { ...appearance, alignment: undefined, hover }
}

test('workspace and secondary dialogs share header controls and keep actions out of their accessible titles', async ({ page, vista }, info) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.goto(vista.origin + '/settings/models')
  const mobile = page.viewportSize()!.width <= 830
  const settings = page.getByRole('dialog', { name: 'Model connections', exact: true })
  const workspaceControl = settings.locator(mobile ? '.ui-modal-back' : '.ui-modal-close')
  const expectedAppearance = await controlAppearance(workspaceControl)
  expect(expectedAppearance.width).toBe(expectedAppearance.height)
  expect(expectedAppearance.width).toBeGreaterThanOrEqual(mobile ? 44 : 32)
  const actions = settings.locator('.ui-modal-header-actions')
  await expect(actions.getByRole('button').filter({ visible: true })).toHaveCount(mobile ? 2 : 3)
  await settings.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const provider = page.getByRole('dialog', { name: 'Connect provider', exact: true })
  const closeProvider = provider.getByRole('button', { name: 'Close', exact: true })
  await expect(provider.getByRole('heading')).toHaveText('Connect provider')
  expect(await controlAppearance(closeProvider)).toEqual(expectedAppearance)
  const closeIcon = await closeProvider.locator('svg').innerHTML()
  await closeProvider.click()
  await expect(provider).toHaveCount(0)
  await expect(settings).toBeVisible()

  await page.goto(vista.origin + '/settings/personalization')
  await page.getByRole('button', { name: 'Manage memories', exact: true }).click()
  const memories = page.getByRole('dialog', { name: /^Manage memories/ })
  const closeMemories = memories.getByRole('button', { name: 'Close', exact: true })
  await expect(memories).toHaveAccessibleName(/^Manage memories\s*0$/)
  expect(await controlAppearance(closeMemories)).toEqual(expectedAppearance)
  expect(await closeMemories.locator('svg').innerHTML()).toBe(closeIcon)
  const order = await memories.locator('.ui-modal-header-actions button').allTextContents()
  expect(order).toHaveLength(2)
  await expect(memories.locator('.ui-modal-header-actions button').first()).toHaveAccessibleName('Add memory')
  await page.screenshot({ path: info.outputPath('shared-memory-header.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(memories).toBeHidden()
  await expect(page.getByRole('dialog', { name: 'Personalization', exact: true })).toBeVisible()

  await page.goto(vista.origin + '/sessions')
  const sessions = page.getByRole('dialog', { name: 'Manage sessions', exact: true })
  expect(await controlAppearance(sessions.locator(mobile ? '.ui-modal-back' : '.ui-modal-close'))).toEqual(expectedAppearance)
  await page.screenshot({ path: info.outputPath('shared-session-header.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(sessions).toHaveCount(0)
})

test('memory save disables its shared close control until the mutation completes', async ({ page, vista }) => {
  await page.goto(vista.origin + '/settings/personalization')
  await page.getByRole('button', { name: 'Manage memories', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: /^Manage memories/ })
  await dialog.getByRole('button', { name: 'Add memory', exact: true }).click()
  await dialog.getByRole('textbox', { name: 'Preference', exact: true }).fill('Keep explanations concise.')
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/memories?*', async route => {
    if (route.request().method() === 'POST') await gate
    await route.continue()
  })
  try {
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
    const close = dialog.getByRole('button', { name: 'Close', exact: true })
    await expect(close).toBeVisible()
    await expect(close).toBeDisabled()
    await page.keyboard.press('Escape')
    await expect(dialog).toBeVisible()
    release()
    await expect(close).toBeEnabled()
    await expect(dialog.locator('.memory-text')).toHaveText('Keep explanations concise.')
    await close.click()
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('dialog', { name: 'Personalization', exact: true })).toBeVisible()
  } finally { release() }
})
