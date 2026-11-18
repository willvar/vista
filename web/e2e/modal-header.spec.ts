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
  const settings = page.getByRole('dialog', { name: 'Service connections', exact: true })
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

  await page.goto(vista.origin + '/settings/network')
  const network = page.getByRole('dialog', { name: 'Network configuration', exact: true })
  const addProxy = network.locator('.workspace-modal-header .ui-modal-header-actions').getByRole('button', { name: 'Add proxy profile', exact: true })
  await expect(addProxy).toHaveText('')
  await expect(addProxy.locator('svg')).toHaveClass(/tabler-icon-plus/)
  expect(await controlAppearance(addProxy)).toEqual(expectedAppearance)
  await expect(network.locator('.network-settings').getByRole('button', { name: 'Add proxy profile', exact: true })).toHaveCount(0)
  await expect(network).toContainText('No proxy profiles yet. Use + in the upper right to add one.')
  await addProxy.click()
  const profile = page.getByRole('dialog', { name: 'Add proxy profile', exact: true })
  await expect(profile).toBeVisible()
  await profile.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(profile).toHaveCount(0)
  await expect(network).toBeVisible()

  await page.goto(vista.origin + '/settings/personalization')
  await page.locator('.delegation-depth-setting').getByRole('button', { name: 'Edit', exact: true }).click()
  const delegation = page.getByRole('dialog', { name: 'Delegation depth', exact: true })
  const closeDelegation = delegation.getByRole('button', { name: 'Close', exact: true })
  expect(await controlAppearance(closeDelegation)).toEqual(expectedAppearance)
  expect(await closeDelegation.locator('svg').innerHTML()).toBe(closeIcon)
  await page.screenshot({ path: info.outputPath('shared-delegation-header.png'), animations: 'disabled' })
  await closeDelegation.click()
  await expect(delegation).toBeHidden()

  await page.goto(vista.origin + '/sessions')
  const sessions = page.getByRole('dialog', { name: 'Manage sessions', exact: true })
  expect(await controlAppearance(sessions.locator(mobile ? '.ui-modal-back' : '.ui-modal-close'))).toEqual(expectedAppearance)
  await page.screenshot({ path: info.outputPath('shared-session-header.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(sessions).toHaveCount(0)
})
