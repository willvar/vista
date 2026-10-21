import { closeSettings, openSettingsSection } from './settings-navigation'
import { test, expect } from './fixtures'

for (const modal of [
  { route: '/settings/general', page: '.settings-page', close: 'Close settings' },
  { route: '/sessions', page: '.session-manager-page', close: 'Close session manager' },
]) {
  test(`${modal.route} keeps the modal mounted for its normal closing animation`, async ({ page, vista }) => {
    await page.goto(vista.origin + modal.route)
    const modalPage = page.locator(modal.page)
    await expect(modalPage).toBeVisible()
    // Wait until the library's opening motion has completed before closing.
    await expect(page.locator('.workspace-modal.ant-zoom-enter, .workspace-modal.ant-zoom-appear')).toHaveCount(0)
    await page.evaluate(() => {
      const modal = document.querySelector('.workspace-modal')!
      const observed = { leave: false, removedDuringLeave: false }
      ;(window as any).__modalMotion = observed
      const observer = new MutationObserver(() => {
        if (modal.className.includes('ant-zoom-leave')) {
          observed.leave = true
          observed.removedDuringLeave ||= !modal.isConnected
        }
      })
      observer.observe(modal, { attributes: true, attributeFilter: ['class'] })
    })
    if (modal.page === '.settings-page') await closeSettings(page)
    else await modalPage.getByRole('button', { name: new RegExp(`^(?:${modal.close}|Back to conversation)$`) }).click()
    await expect(page).toHaveURL(vista.origin + '/')
    await expect(page.locator('.workspace-modal')).toHaveCount(0)
    expect(await page.evaluate(() => (window as any).__modalMotion)).toEqual({ leave: true, removedDuringLeave: false })
  })
}

test('settings navigation reuses the open modal and browser Back closes with normal motion', async ({ page, vista }) => {
  await page.goto(vista.origin + '/')
  await page.locator('.settings-trigger').evaluate((button: HTMLButtonElement) => button.click())
  await expect(page.locator('.settings-page')).toBeVisible()
  await expect(page.locator('.workspace-modal.ant-zoom-enter, .workspace-modal.ant-zoom-appear')).toHaveCount(0)
  await page.evaluate(() => {
    const modal = document.querySelector('.workspace-modal')!
    ;(window as any).__workspaceModal = modal
    ;(window as any).__modalLeaveCount = 0
    new MutationObserver(() => {
      if (modal.className.includes('ant-zoom-leave')) (window as any).__modalLeaveCount++
    }).observe(modal, { attributes: true, attributeFilter: ['class'] })
  })
  await openSettingsSection(page, 'models')
  await expect(page).toHaveURL(vista.origin + '/settings/models')
  expect(await page.evaluate(() => document.querySelector('.workspace-modal') === (window as any).__workspaceModal)).toBe(true)
  expect(await page.evaluate(() => (window as any).__modalLeaveCount)).toBe(0)
  await page.goBack()
  await expect(page).toHaveURL(vista.origin + (page.viewportSize()!.width <= 830 ? '/settings' : '/settings/general'))
  expect(await page.evaluate(() => (window as any).__modalLeaveCount)).toBe(0)
  await page.goBack()
  await expect(page).toHaveURL(vista.origin + '/')
  await expect(page.locator('.workspace-modal')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).__modalLeaveCount)).toBeGreaterThan(0)
})
