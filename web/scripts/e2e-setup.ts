import { build } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { controlAssets } from '../e2e/frontend-server'

// The application is built by make before Playwright starts. Build the separate
// control-contract page once too, rather than transforming it in every worker.
export default async function setup() {
  await build({
    configFile: false,
    root: resolve(import.meta.dirname, '../e2e/controls'),
    plugins: [vue()],
    define: { 'process.env.NODE_ENV': JSON.stringify('production') },
    logLevel: 'warn',
    build: { outDir: controlAssets, emptyOutDir: true, reportCompressedSize: false },
  })
  const runtime = resolve(import.meta.dirname, '../.vista/test-state')
  await mkdir(runtime, { recursive: true })
  const preparations = await mkdtemp(resolve(runtime, 'preparations-'))
  process.env.VISTA_E2E_PREPARATIONS = preparations
  const storageFixture = resolve(preparations, process.platform === 'win32' ? 'resource-storage-fixture.exe' : 'resource-storage-fixture')
  await promisify(execFile)('go', ['build', '-o', storageFixture, './scripts/resource_storage_fixture'], {
    cwd: resolve(import.meta.dirname, '../..'), env: { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' }, timeout: 120_000,
  })
  process.env.VISTA_E2E_STORAGE_FIXTURE = storageFixture
  return () => rm(preparations, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}
