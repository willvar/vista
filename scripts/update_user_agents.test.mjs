import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test } from 'node:test'
import { sources, updateVersions } from './update_user_agents.mjs'

async function fixture(t) {
  const root = new URL('../.vista/test-state/', import.meta.url)
  await mkdir(root, { recursive: true })
  const directory = await mkdtemp(new URL('ua-versions-', root))
  t.after(() => rm(directory, { recursive: true, force: true }))
  return { directory, output: join(directory, 'versions_generated.go') }
}

function registry(version = '1.2.3') {
  return async (url, options) => {
    assert.equal(options.headers.Accept, 'application/json')
    assert.ok(options.signal instanceof AbortSignal)
    assert.equal(new URL(url).hostname, 'registry.npmjs.org')
    const name = decodeURIComponent(new URL(url).pathname.slice(1, -'/latest'.length))
    return Response.json({ name, version })
  }
}

test('UA versions are generated deterministically, checked without writing, and do not rewrite unchanged files', async t => {
  const { directory, output } = await fixture(t)
  assert.equal((await updateVersions({ output, fetchImpl: registry() })).changed, true)
  const content = await readFile(output, 'utf8')
  assert.match(content, /^\/\/ Code generated .*DO NOT EDIT\./)
  assert.match(content, /const CodexVersion = "1\.2\.3"/)
  assert.match(content, /const CopilotVersion = "1\.2\.3"/)
  assert.equal(content.match(/^const /gm).length, 8)
  assert.equal(sources.find(source => source.constant === 'ClineVersion').package, 'cline')
  const before = (await stat(output)).mtimeMs
  assert.equal((await updateVersions({ output, check: true, fetchImpl: registry() })).changed, false)
  assert.equal((await updateVersions({ output, fetchImpl: registry() })).changed, false)
  assert.equal((await stat(output)).mtimeMs, before)
  await assert.rejects(updateVersions({ output, check: true, fetchImpl: registry('1.2.4') }), /stale/)
  assert.equal(await readFile(output, 'utf8'), content)
  assert.deepEqual(await readdir(directory), ['versions_generated.go'])
})

test('any unavailable or invalid upstream prevents partial updates', async t => {
  const { directory, output } = await fixture(t)
  await writeFile(output, 'existing pinned versions\n')
  for (const broken of [
    async () => { throw new Error('request timed out') },
    async () => new Response('unavailable', { status: 503 }),
    async () => new Response('not JSON'),
    async () => Response.json({ name: 'wrong-package', version: '1.2.3' }),
    async () => Response.json({ name: '@openai/codex', version: '1.2.3-beta.1' }),
  ]) {
    let requested = 0
    const fetchImpl = (url, options) => {
      requested++
      return decodeURIComponent(url).includes('@openai/codex') ? broken() : registry()(url, options)
    }
    await assert.rejects(updateVersions({ output, fetchImpl }), /UA versions were not updated:[\s\S]*@openai\/codex:/)
    assert.equal(requested, sources.length)
    assert.equal(await readFile(output, 'utf8'), 'existing pinned versions\n')
    assert.deepEqual(await readdir(directory), ['versions_generated.go'])
  }
})

test('an older registry version cannot replace the pinned snapshot', async t => {
  const { output } = await fixture(t)
  await updateVersions({ output, fetchImpl: registry('2.0.0') })
  const content = await readFile(output, 'utf8')
  await assert.rejects(updateVersions({ output, fetchImpl: registry('1.99.99') }), /refusing downgrade/)
  assert.equal(await readFile(output, 'utf8'), content)
  assert.equal((await updateVersions({ output, fetchImpl: registry('2.0.1') })).changed, true)
})

test('release builds refresh UA versions first while dev and pinned rebuilds skip the network step', () => {
  const environment = { ...process.env }
  for (const name of ['UPDATE_UA', 'MAKEFLAGS', 'MAKEOVERRIDES']) delete environment[name]
  const plan = (...arguments_) => execFileSync('make', ['--dry-run', 'build', ...arguments_], {
    cwd: new URL('../', import.meta.url), encoding: 'utf8', env: environment,
  })
  const updater = 'node scripts/update_user_agents.mjs'
  const release = plan('VERSION=v0.1.0')
  assert.ok(release.indexOf(updater) >= 0)
  assert.ok(release.indexOf(updater) < release.indexOf('npm --prefix web run build'))
  assert.ok(release.indexOf(updater) < release.indexOf('go build'))
  assert.ok(!plan('VERSION=dev').includes(updater))
  assert.ok(!plan('VERSION=v0.1.0', 'UPDATE_UA=0').includes(updater))
})
