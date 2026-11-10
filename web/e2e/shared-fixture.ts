import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { setTimeout } from 'node:timers/promises'

// A fresh directory belongs to this Playwright invocation. Atomic mkdir picks
// one producer across workers; readers only see a complete result or its error.
export async function sharedFixture<T>(name: string, prepare: (directory: string) => Promise<T>): Promise<T> {
  const root = process.env.VISTA_E2E_PREPARATIONS
  if (!root) throw new Error('Shared fixtures require the Playwright global setup')
  const directory = join(root, name)
  let producer = false
  try { await mkdir(directory); producer = true }
  catch (error) { if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error }
  if (producer) {
    try {
      const value = await prepare(directory)
      await writeFile(join(directory, 'result.tmp'), JSON.stringify({ value }))
      await rename(join(directory, 'result.tmp'), join(directory, 'result.json'))
      return value
    } catch (error) {
      await writeFile(join(directory, 'result.tmp'), JSON.stringify({ error: String(error) }))
      await rename(join(directory, 'result.tmp'), join(directory, 'result.json'))
      throw error
    }
  }
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    let result: { value: T; error?: string }
    try { result = JSON.parse(await readFile(join(directory, 'result.json'), 'utf8')) }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
      await setTimeout(50)
      continue
    }
    if (result.error) throw new Error(`Shared ${name} preparation failed: ${result.error}`)
    return result.value
  }
  throw new Error(`Shared ${name} preparation did not finish within 90 seconds`)
}
