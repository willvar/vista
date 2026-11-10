import { build } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'
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
  return () => rm(preparations, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
}
