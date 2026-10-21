import { defineConfig } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

if (!process.env.VISTA_CHROME) throw new Error('Set VISTA_CHROME to the isolated test browser executable')
const runtime = resolve(import.meta.dirname, '.vista/test-state')
mkdirSync(runtime, { recursive: true })
// Playwright's temporary browser profiles must also stay on disk.
process.env.TMPDIR = runtime

export default defineConfig({
  testDir: './e2e',
  outputDir: resolve(runtime, 'e2e-results'),
  timeout: 30_000,
  expect: { timeout: 8_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: resolve(runtime, 'e2e-report'), open: 'never' }]],
  use: {
    browserName: 'chromium',
    headless: true,
    locale: 'en-US',
    actionTimeout: 8_000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: {
      executablePath: process.env.VISTA_CHROME,
      args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking'],
    },
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'mobile-portrait', use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true } },
    { name: 'mobile-landscape', use: { viewport: { width: 568, height: 320 }, isMobile: true, hasTouch: true } },
  ],
})
