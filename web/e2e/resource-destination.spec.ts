import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { test, expect } from './fixtures'
import type { Page } from '@playwright/test'

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
async function selectStorage(page: Page, label: string) {
 await page.getByRole('combobox', { name: 'Storage method', exact: true }).click()
 await page.getByRole('option', { name: label, exact: true }).click()
}
async function configure(page: Page, endpoint: string) {
 await selectStorage(page, 'Object storage (S3 compatible)')
 await page.getByLabel('Service endpoint', { exact: true }).fill(endpoint)
 await page.getByLabel('Bucket', { exact: true }).fill('resources')
 await page.getByLabel('Region (optional)', { exact: true }).fill('us-east-1')
 await page.getByLabel('Access key', { exact: true }).fill('fixture-access')
 await page.getByLabel('Secret key', { exact: true }).fill('fixture-secret')
}
async function applyStorage(page: Page) {
 await page.locator('.storage-destination').getByRole('button', { name: 'Change storage', exact: true }).click()
 const dialog = page.getByRole('dialog', { name: 'Change storage', exact: true })
 await expect(dialog).toContainText('Conversations keep reading and writing')
 await dialog.getByRole('button', { name: 'Change storage', exact: true }).click()
 await expect(dialog).toBeHidden()
}

test('storage migration keeps conversations active, follows new originals and reclaims copies @responsive', async ({ page, vista }) => {
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
  await configure(page, service.endpoint)
  await page.getByRole('button', { name: 'Check storage', exact: true }).click()
  await expect(page.getByText('Storage read, write and deletion checks passed.', { exact: true })).toBeVisible()
  await expect(page.getByLabel('Access key', { exact: true })).toHaveValue('')
  await expect(page.getByLabel('Secret key', { exact: true })).toHaveValue('')
  await service.controlRequest('/hold')
  await applyStorage(page)
  await expect.poll(async () => (await service.controlRequest('/status')).blocked).toBeGreaterThan(0)
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
  await expect(page.locator('.storage-migration')).toContainText('Synchronizing resources')
  const secondBytes = Buffer.concat([original, Buffer.from('additional immutable bytes')])
  const second = await upload('second.png', secondBytes)
  const sourceRead = await page.request.get(vista.origin + `/api/sessions/${session.id}/resources/${first.hash}`)
  expect(await sourceRead.body()).toEqual(original)
  await service.controlRequest('/release')
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Object storage (S3 compatible)')
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
  await selectStorage(page, 'Local storage')
  await applyStorage(page)
  await expect(page.locator('.storage-current')).toHaveText('Current storage: Local storage')
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
  await configure(page, service.endpoint)
  await service.controlRequest('/deny')
  await applyStorage(page)
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

test('storage form keeps active drafts and rejects another client configuration change @responsive', async ({ page, vista }) => {
 const service = await storageFixture()
 try {
  await page.goto(vista.origin + '/settings/storage')
  await configure(page, service.endpoint)
  await page.getByLabel('Bucket', { exact: true }).fill('unsaved-bucket')
  const status = await vista.api('/resource-storage')
  await vista.api('/resource-storage', { method: 'POST', body: JSON.stringify({ action: 'check', revision: status.config.revision, config: { kind: 's3', endpoint: service.endpoint, bucket: 'resources', region: 'us-east-1', accessKey: 'fixture-access', secretKey: 'fixture-secret' } }) })
  await expect(page.getByLabel('Bucket', { exact: true })).toHaveValue('unsaved-bucket')
  await expect(page.locator('.storage-destination').getByRole('button', { name: 'Change storage', exact: true })).toBeDisabled()
  await page.locator('.storage-destination-actions').getByRole('button', { name: 'Reload current values', exact: true }).click()
  await page.getByRole('button', { name: 'Cancel', exact: true }).last().click()
  await expect(page.getByLabel('Bucket', { exact: true })).toHaveValue('unsaved-bucket')
 } finally { await service.stop() }
})
