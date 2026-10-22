import { openSettingsSection } from './settings-navigation'
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect } from './fixtures'

const review = '---\nname: Code review\ndescription: Check changes and tests\n---\n\nRead references/checklist.md before reviewing.\n'

test('multiple system skills use their own metadata and can only be viewed', async ({ page, vista }) => {
  const catalog = await vista.api('/skills')
  const extra = { id: 'system:another-guide', name: 'Another guide', description: 'A separate built-in capability', source: 'system', path: 'skill://system/another-guide/SKILL.md' }
  await page.route('**/api/skills', route => route.fulfill({ json: { ...catalog, skills: [...catalog.skills, extra] } }))
  await page.route(/\/api\/skills\/system(?::|%3A)another-guide$/, route => route.fulfill({ json: { content: 'Independent embedded instructions', sha256: 'test-hash', files: ['references/extra.md'] } }))
  await page.goto(`${vista.origin}/settings/skills`)
  for (const skill of [...catalog.skills.filter((skill: { source: string }) => skill.source === 'system'), extra]) {
    const card = page.locator(`[data-skill="${skill.id}"]`)
    await expect(card).toContainText(skill.description)
    await expect(card).toContainText('System')
    await expect(card.getByRole('switch')).toHaveCount(0)
    await expect(card.getByRole('button', { name: /^Delete / })).toHaveCount(0)
    await card.getByRole('button', { name: `View ${skill.name}`, exact: true }).click()
    const viewer = page.getByRole('dialog', { name: 'View skill', exact: true })
    await expect(viewer.getByLabel('SKILL.md', { exact: true })).toHaveAttribute('readonly', '')
    await expect(viewer.getByRole('button', { name: 'Save changes', exact: true })).toHaveCount(0)
    if (skill.id === extra.id) await expect(viewer.getByLabel('SKILL.md', { exact: true })).toHaveValue('Independent embedded instructions')
    await viewer.locator('.skill-form-footer').getByRole('button', { name: 'Close', exact: true }).click()
    await expect(viewer).toBeHidden()
  }
  expect(readdirSync(catalog.directory)).toEqual([])
})

test('local skills can be refreshed, edited, searched, disabled and deleted in settings', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.goto(`${vista.origin}/settings/general`)
  await openSettingsSection(page, 'skills')
  await expect(page.locator('.skills-empty')).toContainText('No user skills yet')
  await expect(page.locator('.skills-directory')).toContainText('Place skill folders containing SKILL.md in this directory, then click Refresh.')
  await expect(page.getByRole('button', { name: 'Add skill', exact: true })).toHaveCount(0)
  await expect(page.locator('.skills-settings input[type="file"]')).toHaveCount(0)
  const { directory } = await vista.api('/skills')
  mkdirSync(join(directory, 'review'))
  writeFileSync(join(directory, 'review', 'SKILL.md'), review)
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  const card = page.locator('[data-skill="review"]')
  await expect(card).toContainText('Code review')
  await expect(card).toContainText('Check changes and tests')

  await card.getByRole('button', { name: 'Edit Code review', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit skill', exact: true })
  await expect(editor.getByLabel('SKILL.md', { exact: true })).toHaveValue(review)
  const updated = review.replace('Code review', 'Review carefully') + 'Verify changes before answering.\n'
  await editor.getByLabel('SKILL.md', { exact: true }).fill(updated)
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(editor).toBeHidden()
  await expect(card).toContainText('Review carefully')
  await page.getByRole('textbox', { name: 'Search skills', exact: true }).fill('not present')
  await expect(card).toHaveCount(0)
  await expect(page.locator('.skills-empty')).toHaveText('No matching skills.')
  await page.getByRole('textbox', { name: 'Search skills', exact: true }).fill('tests')
  await expect(card).toBeVisible()
  await card.getByRole('switch', { name: 'Enable Review carefully', exact: true }).click()
  await expect(card.getByRole('switch')).toHaveAttribute('aria-checked', 'false')
  await page.reload()
  await expect(card).toContainText('Disabled')
  expect((await vista.api('/skills')).skills.find((skill: { id: string }) => skill.id === 'review').disabled).toBe(true)
  await card.getByRole('switch').click()
  await expect(card.getByRole('switch')).toHaveAttribute('aria-checked', 'true')

  for (const theme of ['light', 'dark']) {
    await page.evaluate(value => localStorage.setItem('vista-appearance', value), theme)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(card).toBeVisible()
    const layout = await page.locator('.skills-settings').evaluate(element => {
      const box = element.getBoundingClientRect()
      return { left: box.left, right: box.right, width: innerWidth, overflow: element.scrollWidth - element.clientWidth }
    })
    expect(layout.left).toBeGreaterThanOrEqual(0)
    expect(layout.right).toBeLessThanOrEqual(layout.width)
    expect(layout.overflow).toBeLessThanOrEqual(2)
    await page.screenshot({ path: testInfo.outputPath(`skills-${theme}.png`), animations: 'disabled' })
  }
  await card.getByRole('button', { name: 'Delete Review carefully', exact: true }).click()
  const confirmation = page.getByRole('alertdialog')
  await expect(confirmation).toContainText('scripts and supporting files')
  await page.locator('.skill-delete-popover').getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(card).toHaveCount(0)
  await expect(page.locator('.skills-empty')).toContainText('No user skills yet')
  await page.reload()
  expect((await vista.api('/skills')).skills.filter((skill: { source: string }) => skill.source === 'user')).toEqual([])
})

test('editing a local skill preserves its binary assets and references', async ({ page, vista }, testInfo) => {
  const { directory } = await vista.api('/skills')
  const folder = join(directory, 'review-pack')
  const asset = Buffer.from([0, 255, 128, 13, 10])
  mkdirSync(join(folder, 'references'), { recursive: true })
  mkdirSync(join(folder, 'assets'))
  mkdirSync(join(folder, 'scripts'))
  writeFileSync(join(folder, 'SKILL.md'), review)
  writeFileSync(join(folder, 'references', 'checklist.md'), 'Check the relevant tests.\n')
  writeFileSync(join(folder, 'scripts', 'check.py'), 'print("checked")\n')
  writeFileSync(join(folder, 'assets', 'sample.bin'), asset)
  await page.goto(`${vista.origin}/settings/skills`)
  const card = page.locator('[data-skill="review-pack"]')
  await expect(card).toBeVisible()
  expect(readFileSync(join(directory, 'review-pack/assets/sample.bin'))).toEqual(asset)
  expect(readFileSync(join(directory, 'review-pack/scripts/check.py'), 'utf8')).toBe('print("checked")\n')
  await card.getByRole('button', { name: 'Edit Code review', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit skill', exact: true })
  await expect(editor.locator('summary')).toHaveText('3 supporting files')
  await editor.locator('summary').click()
  await expect(editor.locator('.skill-supporting-files')).toContainText('assets/sample.bin')
  const longContent = review.replace('Check changes and tests', '中'.repeat(1024)) + 'Keep the full supporting evidence. 中文资料。\n'.repeat(2000)
  await editor.getByLabel('SKILL.md', { exact: true }).fill(longContent)
  await editor.getByRole('button', { name: 'Save changes', exact: true }).scrollIntoViewIfNeeded()
  const layout = await editor.evaluate(element => ({ width: element.scrollWidth - element.clientWidth, form: element.querySelector('form')!.scrollWidth - element.querySelector('form')!.clientWidth }))
  expect(layout.width).toBeLessThanOrEqual(2)
  expect(layout.form).toBeLessThanOrEqual(2)
  await page.screenshot({ path: testInfo.outputPath('skill-editor.png'), animations: 'disabled' })
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(editor).toBeHidden()
  expect((await vista.api('/skills/review-pack')).content).toBe(longContent)
  expect(readFileSync(join(directory, 'review-pack/assets/sample.bin'))).toEqual(asset)

})

test('failed requests and late document responses keep the current editor and saved content intact', async ({ page, vista }) => {
  const { directory } = await vista.api('/skills')
  for (const id of ['first', 'second']) {
    mkdirSync(join(directory, id))
    writeFileSync(join(directory, id, 'SKILL.md'), `---\nname: ${id}\ndescription: Example\n---\nOriginal instructions`)
  }
  await page.route('**/api/skills', async route => {
    await route.fulfill({ status: 503, body: 'Temporary skill read failure' })
    await page.unroute('**/api/skills')
  })
  await page.goto(`${vista.origin}/settings/skills`)
  await expect(page.getByRole('alert')).toContainText('Temporary skill read failure')
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  const first = page.locator('[data-skill="first"]')
  const second = page.locator('[data-skill="second"]')
  await expect(second).toBeVisible()
  let received = false
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/skills/first', async route => {
    received = true
    await held
    await route.fulfill({ json: { content: 'Late first content', sha256: 'late', files: [] } }).catch(() => {})
  })
  await first.getByRole('button', { name: 'Edit first', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit skill', exact: true })
  await expect.poll(() => received).toBe(true)
  await page.keyboard.press('Escape')
  await expect(editor).toBeHidden()
  await expect(page).toHaveURL(/\/settings\/skills$/)
  await second.getByRole('button', { name: 'Edit second', exact: true }).click()
  const original = '---\nname: second\ndescription: Example\n---\nOriginal instructions'
  const updated = '---\nname: second\ndescription: Example\n---\nUpdated second instructions'
  await expect(editor.getByLabel('SKILL.md', { exact: true })).toHaveValue(original)
  release()
  await editor.getByLabel('SKILL.md', { exact: true }).fill(updated)
  await page.route('**/api/skills/second', route => route.request().method() === 'PUT' ? route.fulfill({ status: 500, body: 'Could not save skill' }) : route.continue())
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(editor.getByRole('alert')).toContainText('Could not save skill')
  await expect(editor.getByLabel('SKILL.md', { exact: true })).toHaveValue(updated)
  expect((await vista.api('/skills/second')).content).toBe(original)
  await page.unroute('**/api/skills/second')
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(editor).toBeHidden()
  expect((await vista.api('/skills/second')).content).toBe(updated)
  await first.getByRole('button', { name: 'Edit first', exact: true }).click()
  release()
  await expect(editor).toBeVisible()
  await page.locator('.ant-modal-wrap').last().click({ position: { x: 5, y: 5 } })
  await expect(editor).toBeHidden()
  await expect(page).toHaveURL(/\/settings\/skills$/)
})
