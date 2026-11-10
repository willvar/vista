import { expect, type Locator } from '@playwright/test'

// Long documents are fixture data. Native Chromium insertion is slow for large
// multiline values; control tests retain native editing operations.
export async function fillTextFixture(input: Locator, value: string) {
  await expect(input).toBeVisible()
  await expect(input).toBeEditable()
  await input.evaluate((element: HTMLTextAreaElement, content) => {
    element.value = content
    element.dispatchEvent(new Event('input', { bubbles: true }))
  }, value)
  await expect(input).toHaveValue(value)
}
