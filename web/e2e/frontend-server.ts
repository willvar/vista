import { createServer, preview } from 'vite'
import { rmSync } from 'node:fs'
import { resolve } from 'node:path'
import type { IncomingMessage, ServerResponse } from 'node:http'

export const frontendAssets = resolve(import.meta.dirname, '../../internal/server/assets')
export const controlAssets = resolve(import.meta.dirname, '../.vista/test-state/controls')
type RequestHandler = (request: IncomingMessage, response: ServerResponse) => boolean

// Only immutable build output is shared. Each worker owns its listener and any
// native SSE handlers; browser contexts and mutable API fixtures stay isolated.
export async function startFrontend(options: {
  directory?: string
  handle?: RequestHandler
} = {}) {
  const server = await preview({
    configFile: false,
    root: resolve(import.meta.dirname, '..'),
    build: { outDir: options.directory ?? frontendAssets },
    preview: { host: '127.0.0.1', port: 0, strictPort: false, open: false },
    plugins: [{
      name: 'isolated-browser-fixture',
      configurePreviewServer(server) {
        server.middlewares.use((request, response, next) => {
          if (!options.handle?.(request, response)) next()
        })
      },
    }],
  })
  const address = server.httpServer.address()
  if (!address || typeof address === 'string') {
    await server.close()
    throw new Error('Mock UI server did not start')
  }
  return { origin: `http://127.0.0.1:${address.port}`, close: () => server.close() }
}

// These two module-failure regressions replace source imports before mounting.
// Keep that capability separate from ordinary tests of the built application.
export async function startSourceFrontend(handle: RequestHandler) {
  const server = await createServer({
    root: resolve(import.meta.dirname, '..'),
    configFile: resolve(import.meta.dirname, '../vite.config.ts'),
    cacheDir: resolve(import.meta.dirname, `../.vista/test-state/render-failure-vite-${process.pid}`),
    server: { host: '127.0.0.1', port: 0, watch: { ignored: ['**/.vista/**'] } },
    plugins: [{
      name: 'source-failure-events', enforce: 'pre',
      configureServer(server) {
        server.middlewares.use((request, response, next) => { if (!handle(request, response)) next() })
      },
    }],
  })
  await server.listen()
  const address = server.httpServer!.address()
  if (!address || typeof address === 'string') {
    await server.close()
    throw new Error('Source UI server did not start')
  }
  return {
    origin: `http://127.0.0.1:${address.port}`,
    async close() { try { await server.close() } finally { rmSync(server.config.cacheDir, { recursive: true, force: true }) } },
  }
}
