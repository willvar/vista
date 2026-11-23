import { expect, type Page } from '@playwright/test'

export async function openSettingsSection(page: Page, section: string) {
  const advanced = page.locator('.about-advanced-modal:visible')
  if (await advanced.count()) await advanced.locator('.ui-modal-close').click()
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
  const advanced = page.locator('.about-advanced-modal:visible')
  if (await advanced.count()) await advanced.locator('.ui-modal-close').click()
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

export async function openAboutAdvanced(page: Page, tab?: string) {
  const dialog = page.getByRole('dialog', { name: 'Advanced settings', exact: true })
  if (!new URL(page.url()).searchParams.getAll('layer').includes('advanced:')) {
    await page.locator('.settings-page').getByRole('button', { name: 'Advanced settings', exact: true }).click()
  }
  await expect(dialog).toBeVisible()
  if (tab) {
    await dialog.getByRole('tab', { name: tab, exact: true }).click()
    await expect(dialog.getByRole('tab', { name: tab, exact: true })).toHaveAttribute('aria-selected', 'true')
  }
  return dialog
}

export async function saveAboutAdvanced(page: Page) {
  const save = page.locator('.about-advanced-modal .ui-modal-footer').getByRole('button', { name: 'Save changes', exact: true })
  await save.click()
  await expect(save).not.toHaveAttribute('aria-busy', 'true')
  await expect(save).toBeDisabled()
}
