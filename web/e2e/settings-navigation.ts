import { expect, type Page } from '@playwright/test'

export async function openSettingsSection(page: Page, section: string) {
  const settings = page.locator('.settings-page')
  await expect(settings).toBeVisible()
  const back = settings.locator('.ui-modal-back:visible')
  if (await back.count() && !await settings.evaluate(element => element.classList.contains('settings-index'))) {
    await back.click()
    await expect(settings).toHaveClass(/settings-index/)
  }
  const link = settings.locator(`.settings-nav a[href^="/settings/${section}"]`)
  await link.click()
  await expect(link).toHaveAttribute('aria-current', 'page')
}

export async function closeSettings(page: Page) {
  const settings = page.locator('.settings-page')
  await expect(settings).toBeVisible()
  const back = settings.locator('.ui-modal-back:visible')
  if (await back.count() && !await settings.evaluate(element => element.classList.contains('settings-index'))) {
    await back.click()
    await expect(settings).toHaveClass(/settings-index/)
  }
  await settings.locator('.ui-modal-close:visible, .ui-modal-back:visible').click()
  await expect(settings).toHaveCount(0)
}
