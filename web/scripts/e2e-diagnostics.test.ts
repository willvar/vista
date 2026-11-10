import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'
import { pathToFileURL } from 'node:url'
import test from 'node:test'

const execute = promisify(execFile)
const runtime = resolve(import.meta.dirname, '../.vista/test-state')

test('a passing diagnostic retry retains its trace and cannot turn a failed gate green', { timeout: 30_000 }, async () => {
  await mkdir(runtime, { recursive: true })
  const root = await mkdtemp(join(runtime, 'diagnostics-unit-'))
  const report = join(root, 'report.json')
  try {
    await writeFile(join(root, 'playwright.config.ts'), `
      import original from ${JSON.stringify(pathToFileURL(resolve(import.meta.dirname, '../playwright.config.ts')).href)};
      export default { ...original, testDir: ${JSON.stringify(root)}, globalSetup: undefined,
        workers: 1, projects: [{ name: 'diagnostics' }],
        outputDir: ${JSON.stringify(join(root, 'results'))},
        reporter: [['json', { outputFile: ${JSON.stringify(report)} }]] };
    `)
    await writeFile(join(root, 'probe.spec.ts'), `
      import { test, expect } from '@playwright/test';
      test('stable case', () => expect(true).toBe(true));
      test('transient failure', ({}, info) => expect(info.retry).toBeGreaterThan(0));
    `)
    let code = 0
    try {
      await execute(process.execPath, [resolve(import.meta.dirname, '../node_modules/@playwright/test/cli.js'), 'test', '--config=' + join(root, 'playwright.config.ts')], {
        env: { ...process.env, VISTA_CHROME: process.env.VISTA_CHROME || 'unused-by-browserless-probe', VISTA_E2E_SELECTION: '', PLAYWRIGHT_JSON_OUTPUT_FILE: report },
        timeout: 25_000,
      })
    } catch (error) {
      if ((error as { code?: number }).code !== 1) throw error
      code = 1
    }
    assert.equal(code, 1, 'The first failed assertion must still fail the command after its retry passes')
    const result = JSON.parse(await readFile(report, 'utf8'))
    const cases = result.suites.flatMap((suite: any) => suite.specs).flatMap((spec: any) => spec.tests)
    const stable = cases.find((item: any) => item.status === 'expected')
    const flaky = cases.find((item: any) => item.status === 'flaky')
    assert.deepEqual(stable.results.map((item: any) => item.status), ['passed'])
    assert.deepEqual(flaky.results.map((item: any) => item.status), ['failed', 'passed'])
    assert.equal(flaky.results[0].attachments.some((item: any) => item.name === 'trace'), false)
    const trace = flaky.results[1].attachments.find((item: any) => item.name === 'trace')
    assert.ok(trace, 'A passing retry must retain its diagnostic trace')
    assert.ok((await stat(trace.path)).size > 0)
  } finally { await rm(root, { recursive: true, force: true }) }
})
