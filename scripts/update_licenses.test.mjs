import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test } from 'node:test'
import { licenseFiles, packageForModule, renderNotices } from './update_licenses.mjs'

async function fixture(t) {
  const state = new URL('../.vista/test-state/', import.meta.url)
  await mkdir(state, { recursive: true })
  const directory = await mkdtemp(new URL('licenses-', state))
  t.after(() => rm(directory, { recursive: true, force: true }))
  return directory
}

test('license discovery preserves separate copyright and NOTICE documents', async t => {
  const directory = await fixture(t)
  await mkdir(join(directory, 'third_party'), { recursive: true })
  await mkdir(join(directory, 'node_modules/other'), { recursive: true })
  await writeFile(join(directory, 'LICENSE'), 'MIT \r\nCopyright Original Author \t\r\n')
  await writeFile(join(directory, 'NOTICE.txt'), 'Original attribution\n')
  await writeFile(join(directory, 'third_party/COPYING'), 'Third-party copyright\n')
  await writeFile(join(directory, 'node_modules/other/LICENSE'), 'Unrelated dependency\n')
  const files = await licenseFiles(directory)
  assert.deepEqual(files.map(file => file.name), ['LICENSE', 'NOTICE.txt', 'third_party/COPYING'])
  assert.equal(files[0].text, 'MIT\nCopyright Original Author\n')
  assert.equal(await readFile(join(directory, 'LICENSE'), 'utf8'), 'MIT \r\nCopyright Original Author \t\r\n')
  await writeFile(join(directory, 'NOTICE.txt'), '')
  await assert.rejects(licenseFiles(directory), /Empty license or notice/)
})

test('bundle modules resolve to the nearest package, preserving nested dependency versions', async t => {
  const directory = await fixture(t)
  const outer = join(directory, 'web/node_modules/example')
  const inner = join(outer, 'node_modules/example')
  await mkdir(join(inner, 'dist'), { recursive: true })
  await writeFile(join(outer, 'package.json'), JSON.stringify({ name: 'example', version: '2.0.0' }))
  await writeFile(join(inner, 'package.json'), JSON.stringify({ name: 'example', version: '1.0.0' }))
  assert.deepEqual(await packageForModule(join(inner, 'dist/index.js') + '?commonjs-proxy', directory), {
    location: 'web/node_modules/example/node_modules/example', name: 'example', version: '1.0.0',
  })
  assert.equal(await packageForModule('\0virtual-module', directory), undefined)
  assert.equal(await packageForModule(join(directory, 'web/src/main.ts'), directory), undefined)
})

test('notices deduplicate identical texts without losing versions, attribution, or combined licenses', () => {
  const license = { name: 'LICENSE', text: 'MIT\nCopyright Dependency Author\n' }
  const entries = [
    { name: 'one', version: '1.0.0', scope: 'Frontend dependency', license: 'MIT', files: [license] },
    { name: 'two', version: '2.0.0', scope: 'Generated regional catalogs', license: 'MIT AND Apache-2.0', files: [license, { name: 'NOTICE', text: 'Additional attribution\n' }] },
  ]
  const text = renderNotices(entries, 'MIT\nCopyright Vista Author\n')
  assert.equal(text, renderNotices([...entries].reverse(), 'MIT\nCopyright Vista Author\n'))
  assert.equal(text.match(/Copyright Dependency Author/g).length, 1)
  for (const value of ['one@1.0.0', 'two@2.0.0', 'MIT AND Apache-2.0', 'Additional attribution', 'Copyright Vista Author']) assert.ok(text.includes(value))
  assert.throws(() => renderNotices([{ name: 'missing', version: '1', scope: 'Runtime', files: [] }], ''), /No license documents/)
})
