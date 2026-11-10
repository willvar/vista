import { defineConfig } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { availableParallelism } from 'node:os'
import { responsiveGrep } from './scripts/e2e-coverage.ts'

if (!process.env.VISTA_CHROME) throw new Error('Set VISTA_CHROME to the isolated test browser executable')
const runtime = resolve(import.meta.dirname, '.vista/test-state')
const selection = process.env.VISTA_E2E_SELECTION
const mobileGrep = selection ? new RegExp(`(?=.*(?:${responsiveGrep.source}))(?=.*(?:${selection}))`) : responsiveGrep
mkdirSync(runtime, { recursive: true })
// Playwright's temporary browser profiles must also stay on disk.
process.env.TMPDIR = runtime

export default defineConfig({
  testDir: './e2e',
  outputDir: resolve(runtime, 'e2e-results'),
  globalSetup: './scripts/e2e-setup.ts',
  timeout: 30_000,
  expect: { timeout: 8_000 },
  fullyParallel: true,
  workers: process.env.CI ? 2 : Math.min(4, availableParallelism()),
  // Repeat a failed case once for diagnostics, never to turn a failure green.
  retries: 1,
  failOnFlakyTests: true,
  reporter: [['list'], ['html', { outputFolder: resolve(runtime, 'e2e-report'), open: 'never' }]],
  use: {
    browserName: 'chromium',
    headless: true,
    locale: 'en-US',
    actionTimeout: 8_000,
    // Successful first attempts need no continuous DOM/network recording.
    // The diagnostic retry keeps the complete trace, even if it passes.
    trace: { mode: 'on-first-retry', screenshots: false },
    screenshot: 'only-on-failure',
    launchOptions: {
      executablePath: process.env.VISTA_CHROME,
      args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking'],
    },
  },
  projects: [
    { name: 'desktop', grep: selection ? new RegExp(selection) : undefined, use: { viewport: { width: 1440, height: 900 } } },
    { name: 'mobile-portrait', grep: mobileGrep, use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true } },
    { name: 'mobile-landscape', grep: mobileGrep, use: { viewport: { width: 568, height: 320 }, isMobile: true, hasTouch: true } },
  ],
})
