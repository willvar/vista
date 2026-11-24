import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { test, expect } from './fixtures'
import { installMockStateStream, notifyState } from './state-stream-fixture'
import type { Locator, Page } from '@playwright/test'

test.use({ imageModel: true })
const original = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=', 'base64')

async function storageFixture() {
 const binary = process.env.VISTA_E2E_STORAGE_FIXTURE
 if (!binary) throw new Error('Storage fixture was not prepared by the shared E2E setup')
 const child = spawn(binary, [], { stdio: ['pipe', 'pipe', 'pipe'] })
 const line = createInterface({ input: child.stdout })
 const value = await Promise.race([
  once(line, 'line').then(([text]) => JSON.parse(text) as { endpoint: string; control: string }),
  once(child, 'exit').then(() => { throw new Error('Storage fixture exited before readiness') }),
 ])
 line.close()
 return {
  ...value,
  async controlRequest(path: string) { const result = await fetch(value.control + path, { method: 'POST' }); if (!result.ok) throw new Error('Storage fixture control failed'); return path === '/status' ? result.json() : undefined },
  async stop() {
   if (child.exitCode !== null || child.signalCode) return
   const exited = once(child, 'exit')
   child.stdin.end()
   const timer = setTimeout(() => child.kill('SIGKILL'), 1500)
   try { await exited } finally { clearTimeout(timer) }
  },
 }
}
const addLabel = 'Add DOFS configuration'
const editLabel = 'Edit DOFS configuration'
const switchLabel = 'Switch storage location…'
const saved = 'Save changes'
const check = 'Test connection'
const checked = 'Storage read, write and deletion checks passed.'
const primary = 'Media archive'
const spare = 'Spare storage'
const credentials = { accessKey: 'fixture-access', secretKey: 'fixture-secret' }

async function fillPreset(page: Page, name: string, endpoint: string, prefix = '') {
 await page.getByRole('button', { name: addLabel, exact: true }).click()
 return fillPresetFields(page.getByRole('dialog', { name: addLabel }), name, endpoint, prefix)
}
async function fillPresetFields(dialog: Locator, name: string, endpoint: string, prefix = '') {
 await dialog.getByLabel('Profile name', { exact: true }).fill(name)
 await dialog.getByLabel('Service endpoint', { exact: true }).fill(endpoint)
 await dialog.getByLabel('Bucket', { exact: true }).fill('resources')
 await dialog.getByLabel('Region (optional)', { exact: true }).fill('us-east-1')
 await dialog.getByLabel('Storage prefix (optional)', { exact: true }).fill(prefix)
 await dialog.getByLabel('Access key', { exact: true }).fill(credentials.accessKey)
 await dialog.getByLabel('Secret key', { exact: true }).fill(credentials.secretKey)
 return dialog
}
async function savePreset(dialog: Awaited<ReturnType<typeof fillPreset>>) {
 await dialog.getByRole('button', { name: saved, exact: true }).click()
 await expect(dialog).toBeHidden()
}
async function switchStorage(page: Page, label: string, byKeyboard = false) {
 await page.getByRole('button', { name: switchLabel, exact: true }).click()
 const dialog = page.getByRole('dialog', { name: switchLabel })
 const confirm = dialog.getByRole('button', { name: 'Confirm and migrate', exact: true })
 await expect(confirm).toBeDisabled()
 const combo = dialog.getByRole('combobox', { name: 'Destination storage', exact: true })
 await combo.click()
 const option = page.getByRole('option', { name: label, exact: true })
 if (byKeyboard) {
  const current = page.getByRole('option', { selected: true })
  await expect(current).toBeVisible()
  await expect(combo).toHaveAttribute('aria-activedescendant', (await current.getAttribute('id'))!)
  await combo.press('ArrowUp')
  await expect(combo).toHaveAttribute('aria-activedescendant', (await option.getAttribute('id'))!)
  await combo.press('Enter')
 } else await option.click()
 await expect(dialog).toContainText('Conversations keep reading and writing')
 await expect(dialog.locator('strong')).toContainText(label)
 await expect(dialog.getByLabel('Bucket', { exact: true })).toHaveCount(0)
 const sent = page.waitForRequest(request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/resource-storage' && request.postDataJSON().action === 'migrate')
 await confirm.click()
 if (label === 'Local storage') expect((await sent).postDataJSON().presetId).toBe('')
 else await sent
 await expect(dialog).toBeHidden()
}

test('an intermediate connection check snapshot does not invalidate a DOFS draft', async ({ page, vista }) => {
 test.setTimeout(45_000)
 const service = await storageFixture()
 try {
  await installMockStateStream(page)
  await page.goto(vista.origin + '/settings/storage')
  const progress = page.locator('.storage-progress')
  await expect(progress).toHaveCount(0)
  const editor = await fillPreset(page, primary, service.endpoint)
  await service.controlRequest('/hold-check')
  await editor.getByRole('button', { name: check, exact: true }).click()
  await expect.poll(async () => (await service.controlRequest('/status')).blocked).toBeGreaterThan(0)
  const during = await vista.api('/resource-storage')
  // Deliver a background update with the actual configuration read during the probe.
  await notifyState(page, ['storage'], { storage: { ...during, action: 'scan', phase: 'running' } })
  await expect(progress).toBeVisible()
  await service.controlRequest('/release')
  await expect(editor.getByText(checked, { exact: true })).toBeVisible()
  await expect(editor.getByRole('button', { name: saved, exact: true })).toBeEnabled()
  await savePreset(editor)
  await expect(page.locator('.storage-preset')).toContainText(primary)
 } finally {
  await service.controlRequest('/release').catch(() => undefined)
  await service.stop()
 }
})

test('storage migration keeps conversations active, follows new originals and reclaims copies @responsive', async ({ page, vista }, info) => {
 test.setTimeout(60_000)
 const service = await storageFixture()
 try {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const upload = async (name: string, body: Buffer) => vista.api(`/sessions/${session.id}/resources?name=${name}`, { method: 'POST', headers: { 'Content-Type': 'image/png' }, body: new Uint8Array(body) })
  const first = await upload('first.png', original)
  let browserReads = 0
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/resource-storage' && request.method() === 'GET') browserReads++ })
  await page.goto(vista.origin + '/settings/storage')
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  await expect(page.getByRole('button', { name: switchLabel, exact: true })).toBeDisabled()
  const editor = await fillPreset(page, primary, service.endpoint)
  await editor.getByRole('button', { name: check, exact: true }).click()
  await expect(editor.getByText(checked, { exact: true })).toBeVisible()
  expect((await vista.api('/resource-storage')).config.presets).toEqual([])
  await savePreset(editor)
  await savePreset(await fillPreset(page, spare, service.endpoint, 'spare'))
  const row = (name: string) => page.locator('.storage-preset').filter({ hasText: name })
  await expect(row(primary)).toContainText('DOFS')
  await expect(row(primary)).not.toContainText('resources')
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  const preconfigured = await vista.api('/resource-storage')
  expect(preconfigured.config.presets.map((preset: { name: string }) => preset.name)).toEqual([primary, spare])
  expect(preconfigured.config.migration).toBeUndefined()
  expect(preconfigured.config.activePresetId).toBe('')
  await row(primary).getByRole('button', { name: 'Edit', exact: true }).click()
  const savedEditor = page.getByRole('dialog', { name: editLabel })
  await expect(savedEditor.getByLabel('Secret key', { exact: true })).toHaveValue('')
  await savedEditor.getByRole('button', { name: check, exact: true }).click()
  await expect(savedEditor.getByText(checked, { exact: true })).toBeVisible()
  await expect(savedEditor.getByRole('button', { name: saved, exact: true })).toBeDisabled()
  await page.goBack()
  await expect(savedEditor).toBeHidden()
  await service.controlRequest('/hold')
  await switchStorage(page, primary)
  await expect.poll(async () => (await service.controlRequest('/status')).blocked).toBeGreaterThan(0)
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  await expect(page.locator('.storage-migration')).toContainText('Synchronizing resources')
  const secondBytes = Buffer.concat([original, Buffer.from('additional immutable bytes')])
  const second = await upload('second.png', secondBytes)
  const sourceRead = await page.request.get(vista.origin + `/api/sessions/${session.id}/resources/${first.hash}`)
  expect(await sourceRead.body()).toEqual(original)
  await service.controlRequest('/release')
  await expect(page.locator('.storage-current')).toHaveText(`Current storage: ${primary}`)
  await expect(row(primary).getByRole('button', { name: 'Delete', exact: true })).toBeDisabled()
  await expect(page.locator('.storage-migration')).toHaveCount(0)
  const status = await vista.api('/resource-storage')
  expect(status.config.migration.phase).toBe('complete')
  expect(JSON.stringify(status)).not.toContain('fixture-secret')
  expect(JSON.stringify(status)).not.toContain('masterKey')
  for (const [ref, bytes] of [[first, original], [second, secondBytes]] as const) {
   const response = await page.request.get(vista.origin + `/api/sessions/${session.id}/resources/${ref.hash}`)
   expect(response.status()).toBe(200)
   expect(await response.body()).toEqual(bytes)
  }
  await expect(page.locator('.storage-card').nth(2)).toContainText('2 files')
  await page.getByRole('button', { name: 'Clean unreferenced resources', exact: true }).click()
  await page.getByRole('dialog', { name: 'Clean unreferenced resources', exact: true }).getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.locator('.storage-card').nth(2)).toContainText('0 files')
  // A new output after cutover uses object storage and must be included when
  // moving back to local; the retained original hashes/ownership never change.
  const produced = await upload('produced.png', Buffer.concat([original, Buffer.from('produced after cutover')]))
  await switchStorage(page, spare)
  await expect(page.locator('.storage-current')).toHaveText(`Current storage: ${spare}`)
  await row(primary).getByRole('button', { name: 'Delete', exact: true }).click()
  const removal = page.getByRole('dialog', { name: 'Delete', exact: true })
  await expect(removal).toContainText('This does not delete stored resources.')
  await removal.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(row(primary)).toHaveCount(0)
  await switchStorage(page, 'Local storage', info.project.name === 'desktop')
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  expect((await vista.api('/resource-storage')).config.activePresetId).toBe('')
  const restored = await page.request.get(vista.origin + `/api/sessions/${session.id}/resources/${produced.hash}`)
  expect(restored.status()).toBe(200)
  expect(await restored.body()).toEqual(Buffer.concat([original, Buffer.from('produced after cutover')]))
  expect(browserReads).toBeLessThanOrEqual(1)
  const layout = await page.locator('.resource-storage-settings').evaluate(element => ({ overflow: element.scrollWidth - element.clientWidth }))
  expect(layout.overflow).toBeLessThanOrEqual(2)
 } finally { await service.stop() }
})

test('storage migration failure can retry or cancel without changing the source @responsive', async ({ page, vista }) => {
 test.setTimeout(60_000)
 const service = await storageFixture()
 try {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const ref = await vista.api(`/sessions/${session.id}/resources?name=source.png`, { method: 'POST', headers: { 'Content-Type': 'image/png' }, body: original })
  await page.goto(vista.origin + '/settings/storage')
  await savePreset(await fillPreset(page, primary, service.endpoint))
  await service.controlRequest('/deny')
  await switchStorage(page, primary)
  await expect(page.locator('.storage-migration')).toContainText('Synchronization stopped')
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  const response = await page.request.get(vista.origin + `/api/sessions/${session.id}/resources/${ref.hash}`)
  expect(await response.body()).toEqual(original)
  await service.controlRequest('/allow')
  await service.controlRequest('/hold')
  await page.locator('.storage-destination').getByRole('button', { name: 'Retry', exact: true }).click()
  await expect.poll(async () => (await service.controlRequest('/status')).blocked).toBeGreaterThan(0)
  await page.locator('.storage-destination').getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.locator('.storage-migration')).toHaveCount(0)
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  await service.controlRequest('/release')
  await expect(page.locator('.storage-card').nth(2)).not.toContainText('0 files')
  await page.getByRole('button', { name: 'Clean unreferenced resources', exact: true }).click()
  await page.getByRole('dialog', { name: 'Clean unreferenced resources', exact: true }).getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.locator('.storage-card').nth(2)).toContainText('0 files')
  expect((await vista.api('/resource-storage')).config.migration.phase).toBe('cancelled')
 } finally { await service.stop() }
})

test('storage form refreshes pristine editors but keeps drafts after another client changes a configuration @responsive', async ({ page, vista }) => {
 const service = await storageFixture()
 try {
  await page.goto(vista.origin + '/settings/storage')
  await page.getByRole('button', { name: addLabel, exact: true }).click()
  const pristine = page.getByRole('dialog', { name: addLabel })
  const profileName = pristine.getByLabel('Profile name', { exact: true })
  await expect(profileName).toBeEditable()
  await profileName.focus()
  const initial = await vista.api('/resource-storage')
  await vista.api('/resource-storage', { method: 'POST', body: JSON.stringify({ action: 'save', revision: initial.config.revision, config: { name: spare, endpoint: service.endpoint, bucket: 'resources', region: 'us-east-1', ...credentials } }) })
  await expect(page.locator('.storage-preset').filter({ hasText: spare })).toHaveCount(1)
  await expect(profileName).toBeEditable()
  await expect(profileName).toBeFocused()
  const editor = await fillPresetFields(pristine, primary, service.endpoint)
  await editor.getByLabel('Bucket', { exact: true }).fill('unsaved-bucket')
  await expect(editor.getByRole('button', { name: saved, exact: true })).toBeEnabled()
  const status = await vista.api('/resource-storage')
  await vista.api('/resource-storage', { method: 'POST', body: JSON.stringify({ action: 'save', revision: status.config.revision, config: { name: 'Another configuration', endpoint: service.endpoint, bucket: 'resources', prefix: 'other', region: 'us-east-1', ...credentials } }) })
  await expect(editor.getByLabel('Bucket', { exact: true })).toHaveValue('unsaved-bucket')
  await expect(editor.getByRole('button', { name: saved, exact: true })).toBeDisabled()
  await editor.getByRole('button', { name: 'Reload current values', exact: true }).click()
  await page.getByRole('dialog', { name: 'Discard unsaved changes', exact: true }).getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(editor.getByLabel('Bucket', { exact: true })).toHaveValue('unsaved-bucket')
 } finally { await service.stop() }
})
