import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Session } from '@vista/client'
import type { VistaFixture } from './fixtures'
import { test, expect } from './fixtures'

async function run(vista: VistaFixture, id: string, content: string) {
  await vista.api(`/sessions/${id}/messages`, { method: 'POST', body: JSON.stringify({ content, modelKey: 'test' }) })
  await expect.poll(async () => (await vista.api('/sessions') as Session[]).find(item => item.id === id)?.latestRunStatus).toBe('succeeded')
}

test.describe('model-maintained long-term instructions', () => {
  test.use({ instructionTool: true })

  test('a real Worker appends and deletes global requirements in the same document used by Settings and new executions', async ({ vista }) => {
    const first: Session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    await run(vista, first.id, 'Remember concise answers')
    const saved = await vista.api('/agent-settings')
    expect(saved.instructions).toBe('Keep answers concise.\n')
    expect(saved.instructionsSha256).toMatch(/^[a-f0-9]{64}$/)
    const second: Session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    await run(vista, second.id, 'An independent question')
    expect(String(vista.modelRequests.at(-1)!.messages?.[0].content)).toContain('Keep answers concise.')
    expect(String(vista.modelRequests.at(-1)!.messages?.[0].content)).not.toContain('saved preferences at run start')
    await run(vista, first.id, 'Forget my preference')
    expect((await vista.api('/agent-settings')).instructions).toBe('')
    await run(vista, second.id, 'Continue with the latest instructions')
    const current = vista.modelRequests.at(-1)!.messages!.filter(message => ['system', 'developer'].includes(message.role)).at(-1)
    expect(String(current?.content)).not.toContain('Keep answers concise.')
    expect(String(current?.content)).toContain('edit_instructions')
  })

  test('project appends and corrections stay in the specified AGENTS.md without changing global instructions', async ({ vista }) => {
    const initial = await vista.api('/agent-settings')
    await vista.api('/agent-settings', { method: 'PUT', body: JSON.stringify({ instructions: 'A global requirement.', expectedSha256: initial.instructionsSha256 }) })
    const session: Session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    await run(vista, session.id, 'Remember project pnpm')
    expect(readFileSync(join(vista.workspacePath, 'AGENTS.md'), 'utf8')).toBe('Use pnpm.\n')
    await run(vista, session.id, 'Change project npm')
    expect(readFileSync(join(vista.workspacePath, 'AGENTS.md'), 'utf8')).toBe('Use npm.\n')
    expect((await vista.api('/agent-settings')).instructions).toBe('A global requirement.')
  })
})

test('the unified editor preserves a conflicting draft and reloads only after confirmed discard @responsive', async ({ page, vista }) => {
  const initial = await vista.api('/agent-settings')
  await vista.api('/agent-settings', { method: 'PUT', body: JSON.stringify({ instructions: 'Original requirement.', expectedSha256: initial.instructionsSha256 }) })
  const retiredReads: string[] = []
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/memories') retiredReads.push(request.url()) })
  await page.goto(vista.origin + '/settings/personalization')
  await expect(page.getByRole('button', { name: 'Manage memories', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Full-screen editor', exact: true }).click()
  const dialog = page.locator('.instructions-editor-modal')
  const editor = dialog.getByRole('textbox', { name: 'Global instructions', exact: true })
  await editor.fill('My unsaved draft.')
  const current = await vista.api('/agent-settings')
  await vista.api('/agent-settings', { method: 'PUT', body: JSON.stringify({ instructions: 'Changed by another writer.', expectedSha256: current.instructionsSha256 }) })
  const save = dialog.getByRole('button', { name: 'Save changes', exact: true })
  await save.click()
  await expect(dialog.getByRole('alert')).toContainText('Your draft is kept')
  await expect(editor).toHaveValue('My unsaved draft.')
  await expect(save).toBeDisabled()
  expect((await vista.api('/agent-settings')).instructions).toBe('Changed by another writer.')
  await dialog.getByRole('button', { name: 'Reload current values', exact: true }).click()
  const confirmation = page.locator('.instructions-reload-popover')
  await expect(confirmation).toContainText('Discard this draft')
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(editor).toHaveValue('My unsaved draft.')
  await dialog.getByRole('button', { name: 'Reload current values', exact: true }).click()
  await confirmation.getByRole('button', { name: 'Reload current values', exact: true }).click()
  await expect(editor).toHaveValue('Changed by another writer.')
  await expect(save).toBeDisabled()
  await editor.fill('An updated requirement.')
  await save.click()
  await expect.poll(async () => (await vista.api('/agent-settings')).instructions).toBe('An updated requirement.')
  expect(retiredReads).toEqual([])
})
