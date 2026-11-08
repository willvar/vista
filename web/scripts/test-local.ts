import { execFileSync, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, lstatSync, mkdirSync, readFileSync, readlinkSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { browserScope, browserSelection, browserSpecs, localBrowserArgumentsValid } from './e2e-coverage.ts'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const git = (...args: string[]) => execFileSync('git', args, { cwd: root, encoding: 'utf8' }).trim()
const base = process.env.TEST_BASE || 'HEAD'
git('rev-parse', '--verify', '--end-of-options', base + '^{commit}')
const untracked = git('ls-files', '--others', '--exclude-standard', '-z').split('\0').filter(Boolean)
const paths = [...new Set([
  ...git('diff', '--name-only', '-z', base, '--').split('\0'),
  ...untracked,
].filter(Boolean))]
function sourceHash() {
  const hash = createHash('sha256')
  const changed = [...new Set([
    ...git('diff', '--name-only', '-z', 'HEAD', '--').split('\0'),
    ...git('ls-files', '--others', '--exclude-standard', '-z').split('\0'),
  ].filter(Boolean))].sort()
  for (const path of changed) {
    const file = resolve(root, path)
    hash.update(path).update('\0')
    if (!existsSync(file)) hash.update('deleted')
    else if (lstatSync(file).isSymbolicLink()) hash.update('symlink').update(readlinkSync(file))
    else hash.update(readFileSync(file))
    hash.update('\0')
  }
  return hash.digest('hex')
}
const scope = browserScope(paths, browserSpecs(resolve(root, 'web/e2e')))
const grep = browserSelection(scope.files, scope.full)
const args = process.argv.slice(2)
if (!localBrowserArgumentsValid(args)) {
  throw new Error('Local checks accept --list, --headed, --max-failures=N, --workers=N, --timeout=N and --reporter=NAME. Use make test e2e for custom filtering.')
}
const output = resolve(root, '.vista/acceptance/local-checks', new Date().toISOString().replace(/[:.]/g, '-'))
mkdirSync(output, { recursive: true })
const command = ['web/node_modules/@playwright/test/cli.js', 'test', '--config=web/playwright.config.ts', '--workers=2', '--forbid-only', ...args]
const binary = process.env.VISTA_BIN
const identity = {
  base, sourceCommit: git('rev-parse', 'HEAD'), sourceModified: !!git('status', '--porcelain'), sourceDiffHash: sourceHash(), paths, ...scope,
  binaryHash: binary ? createHash('sha256').update(readFileSync(binary)).digest('hex') : null,
  selection: grep, command,
}
writeFileSync(resolve(output, 'selection.json'), JSON.stringify(identity, null, 2) + '\n')
console.log(scope.full ? 'Browser scope: complete coverage (shared or unclassified changes).' : `Browser scope: core smoke + ${scope.files.length} affected spec files.`)
console.log(`Verification record: ${output}`)
const started = performance.now()
const result = spawnSync(process.execPath, command, { cwd: root, stdio: 'inherit', env: { ...process.env, VISTA_E2E_SELECTION: grep || '' } })
const sourceUnchanged = identity.sourceDiffHash === sourceHash() && identity.sourceCommit === git('rev-parse', 'HEAD')
writeFileSync(resolve(output, 'result.json'), JSON.stringify({ ...identity, sourceUnchanged, durationMs: performance.now() - started, exitCode: result.status, signal: result.signal, error: result.error?.message }, null, 2) + '\n')
if (!sourceUnchanged) console.error('Source changed during verification; rerun checks affected by those changes.')
process.exitCode = sourceUnchanged ? result.status ?? 1 : 1
