import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

// Keep local model fixtures independent of live catalog updates. Catalog update
// behavior has its own Go and browser tests with controlled upstream responses.
export function prepareVistaTestConfig(state) {
  const directory = join(state, 'vista')
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  writeFileSync(join(directory, 'model-catalog-status.json'), JSON.stringify({
    nextCheckAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
  }), { mode: 0o600 })
}
