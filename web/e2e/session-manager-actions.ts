import type { Locator, Page } from '@playwright/test'

export async function selectSessions(manager: Locator) {
  await manager.getByRole('button', { name: 'Select', exact: true }).click()
}

export async function sessionManagerAction(page: Page, row: Locator, name: string) {
  await row.getByRole('button', { name: 'Session menu', exact: true }).click()
  await page.getByRole('menuitem', { name, exact: true }).click()
}
