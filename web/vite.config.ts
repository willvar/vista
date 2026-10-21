import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'
import type { IncomingMessage } from 'node:http'

function devOrigin(request: IncomingMessage) {
  return `http://127.0.0.1:${request.socket.localPort}`
}

function backendAddress(): URL | undefined {
  const address = process.env.VISTA_DEV_BACKEND?.trim()
  if (!address) return undefined
  const backend = new URL(address)
  if (backend.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(backend.hostname) || !backend.port || backend.username || backend.password || backend.pathname !== '/' || backend.search || backend.hash) {
    throw new Error('VISTA_DEV_BACKEND must be a loopback HTTP address with an explicit port, such as http://127.0.0.1:41777')
  }
  return backend
}

function devApiGuard(backend: URL | undefined): Plugin {
  return {
    name: 'vista-dev-api',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use('/api', (request, response, next) => {
        if (request.headers.origin && request.headers.origin !== devOrigin(request)) {
          response.statusCode = 403
          response.end('invalid development origin')
          return
        }
        if (!backend) {
          response.statusCode = 503
          response.end('Set VISTA_DEV_BACKEND to the running Vista backend address')
          return
        }
        next()
      })
    },
  }
}

export default defineConfig(({ command }) => {
  const backend = command === 'serve' ? backendAddress() : undefined
  return {
    plugins: [vue(), ...(command === 'serve' ? [devApiGuard(backend)] : [])],
    ...(command === 'build' ? { define: { 'process.env.NODE_ENV': JSON.stringify('production') } } : {}),
    build: {
      emptyOutDir: false,
      outDir: '../internal/server/assets',
      lib: {
        entry: resolve(import.meta.dirname, 'src/main.ts'),
        formats: ['iife'],
        name: 'Vista',
        fileName: () => 'app.js',
      },
    },
    server: {
      host: '127.0.0.1',
      open: false,
      proxy: backend ? {
        '^/api(?:/|$)': {
          target: backend.origin,
          changeOrigin: true,
          timeout: 0,
          proxyTimeout: 0,
          configure(proxy) {
            proxy.on('proxyReq', (proxyRequest, request) => {
              if (request.headers.origin === devOrigin(request)) proxyRequest.setHeader('Origin', backend.origin)
            })
          },
        },
      } : undefined,
    },
  }
})
