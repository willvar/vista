import { test as base } from '@playwright/test'
import { startFrontend } from './frontend-server'

export const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await startFrontend()
    try { await use(server.origin) } finally { await server.close() }
  }, { scope: 'worker' }],
})

export { expect } from '@playwright/test'
