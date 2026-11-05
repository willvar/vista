import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { test, expect } from './fixtures'

test.use({ configuredProvider: false })

for (const kind of ['providers', 'mcp', 'skills'] as const) {
  test(`${kind} import reuses the secondary batch dialog without inventing item progress`, async ({ page, vista }, info) => {
    const source = join(dirname(vista.workspacePath), 'opencode')
    mkdirSync(source, { recursive: true })
    const config = kind === 'providers' ? { providers: {
      gateway: { name: 'Batch gateway', package: '@opencode/ai/providers/openai-compatible', settings: { baseURL: vista.modelEndpoint, apiKey: 'batch-test-secret' } },
      deepseek: { settings: { apiKey: 'batch-test-secret' } },
    } } : kind === 'mcp' ? { mcp: { servers: {
      first: { type: 'local', command: [process.execPath], disabled: true },
      second: { type: 'local', command: [process.execPath], disabled: true },
    } } } : {}
    writeFileSync(join(source, 'opencode.jsonc'), JSON.stringify(config))
    if (kind === 'skills') {
      for (const name of ['first', 'second']) {
        const folder = join(source, 'skills', name)
        mkdirSync(folder, { recursive: true })
        writeFileSync(join(folder, 'SKILL.md'), `---\nname: ${name}\ndescription: Batch import fixture\n---\nRead the task carefully.\n`)
      }
    }
    const title = kind === 'providers' ? 'Import connections' : kind === 'mcp' ? 'Import MCP services' : 'Import skills'
    const endpoint = kind === 'providers' ? '/api/provider-import' : '/api/extension-import'
    await page.goto(`${vista.origin}/settings/${kind === 'providers' ? 'models' : kind}`)
    await page.getByRole('button', { name: title, exact: true }).click()
    const modal = page.locator('.provider-import-modal')
    const dialog = modal.getByRole('dialog', { name: title, exact: true })
    await dialog.getByRole('button', { name: 'Read configuration', exact: true }).click()
    const confirm = dialog.getByRole('button', { name: 'Import (2)', exact: true })
    await expect(confirm).toBeEnabled()
    await dialog.evaluate(element => { element.setAttribute('data-preview-dialog', 'true') })
    let imports = 0
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    await page.route(`**${endpoint}`, async route => {
      const input = route.request().postDataJSON()
      if (input.action !== 'import') return route.continue()
      imports++
      expect(input.ids).toHaveLength(2)
      expect(input.fingerprint).toBeTruthy()
      const response = await route.fetch()
      await gate
      await route.fulfill({ response })
    })
    let streams = 0
    page.on('request', request => { if (new URL(request.url()).pathname === '/api/events') streams++ })
    try {
      await confirm.click()
      const progress = dialog.locator('.batch-operation-progress')
      await expect(progress).toBeVisible()
      await expect(dialog).toHaveAttribute('data-preview-dialog', 'true')
      await expect(dialog.locator('.provider-import-content')).toHaveCount(0)
      const bar = dialog.getByRole('progressbar')
      expect(await bar.evaluate(element => element.matches(':indeterminate'))).toBe(true)
      await expect(bar).not.toHaveAttribute('value')
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await expect(dialog.locator('.batch-operation-progress-fill')).toHaveCSS('animation-name', 'none')
      await page.emulateMedia({ reducedMotion: 'no-preference' })
      await expect(dialog.locator('.batch-operation-progress-fill')).not.toHaveCSS('animation-name', 'none')
      await page.keyboard.press('Escape')
      await page.mouse.click(2, 2)
      await expect(dialog).toBeVisible()
      await expect(dialog.getByRole('button')).toHaveCount(0)
      await expect(progress).not.toContainText('Processed')
      expect(imports).toBe(1)
      await page.screenshot({ path: info.outputPath(`${kind}-batch-progress.png`), animations: 'disabled' })
      release()
      await expect(modal).toBeHidden()
      await expect(page.locator('.settings-page')).toBeVisible()
      expect(imports).toBe(1)
      expect(streams, 'Import retains the shared state stream').toBe(0)
      if (kind === 'providers') expect((await vista.api('/model-config')).models.map((model: { name: string }) => model.name)).toContain('Batch gateway')
      else if (kind === 'mcp') expect((await vista.api('/mcp-config')).servers).toHaveLength(2)
      else expect((await vista.api('/skills')).skills.filter((skill: { source: string }) => skill.source === 'user')).toHaveLength(2)
    } finally { release() }
  })
}
