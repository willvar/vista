import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { promisify } from 'node:util'
import { DatabaseSync } from 'node:sqlite'
import test from 'node:test'
import { sharedFixture } from '../e2e/shared-fixture.ts'
import { saveHistoryState, restoreHistoryState } from '../e2e/history-state.ts'

const execute = promisify(execFile)
const runtime = resolve(import.meta.dirname, '../.vista/test-state')

test('concurrent worker processes prepare one complete shared fixture per invocation', async () => {
  await mkdir(runtime, { recursive: true })
  const root = await mkdtemp(join(runtime, 'preparation-unit-'))
  const module = pathToFileURL(resolve(import.meta.dirname, '../e2e/shared-fixture.ts')).href
  const code = `import { sharedFixture } from ${JSON.stringify(module)};
    import { appendFile } from 'node:fs/promises';
    import { join } from 'node:path';
    const value = await sharedFixture('history', async directory => {
      await appendFile(join(directory, 'productions'), 'once\\n');
      await new Promise(resolve => setTimeout(resolve, 60));
      return { complete: true, producer: process.pid };
    });
    process.stdout.write(JSON.stringify(value));`
  try {
    const consumers = await Promise.all(Array.from({ length: 4 }, () => execute(process.execPath, ['--input-type=module', '-e', code], {
      env: { ...process.env, VISTA_E2E_PREPARATIONS: root },
    })))
    const results = consumers.map(item => JSON.parse(item.stdout))
    assert.equal(results[0].complete, true)
    results.forEach(result => assert.deepEqual(result, results[0]))
    assert.equal(await readFile(join(root, 'history', 'productions'), 'utf8'), 'once\n')
  } finally { await rm(root, { recursive: true, force: true }) }
})

test('preparation failures reach waiters and a fresh invocation does not reuse old data', async () => {
  await mkdir(runtime, { recursive: true })
  const root = await mkdtemp(join(runtime, 'preparation-unit-'))
  const previous = process.env.VISTA_E2E_PREPARATIONS
  try {
    process.env.VISTA_E2E_PREPARATIONS = root
    let startProducer!: () => void, releaseProducer!: () => void
    const started = new Promise<void>(resolve => { startProducer = resolve })
    const released = new Promise<void>(resolve => { releaseProducer = resolve })
    const producer = sharedFixture('history', async () => {
      startProducer()
      await released
      throw new Error('Producer failed')
    })
    await started
    const pending = Promise.allSettled([
      producer,
      sharedFixture('history', async () => assert.fail('A waiter must not generate a replacement')),
    ])
    releaseProducer()
    const results = await pending
    for (const result of results) {
      assert.equal(result.status, 'rejected')
      if (result.status === 'rejected') assert.match(String(result.reason), /Producer failed/)
    }
    const fresh = join(root, 'next-invocation')
    await mkdir(fresh)
    process.env.VISTA_E2E_PREPARATIONS = fresh
    assert.equal(await sharedFixture('history', async () => 'new data'), 'new data')
  } finally {
    if (previous === undefined) delete process.env.VISTA_E2E_PREPARATIONS
    else process.env.VISTA_E2E_PREPARATIONS = previous
    await rm(root, { recursive: true, force: true })
  }
})

test('history copies survive source removal with independent databases and rebased tool outputs', async () => {
  await mkdir(runtime, { recursive: true })
  const root = await mkdtemp(join(runtime, 'history-unit-'))
  const source = join(root, 'original-中文"'), seed = join(root, 'seed')
  const content = 'Complete tool output café 🙂'
  try {
    await mkdir(join(source, 'outputs'), { recursive: true })
    await writeFile(join(source, 'outputs', 'tool.txt'), content)
    await writeFile(join(source, 'instance.json'), 'Never copy process ownership')
    const db = new DatabaseSync(join(source, 'vista.db'))
    db.exec(`CREATE TABLE runs (status TEXT); INSERT INTO runs VALUES ('succeeded');
      CREATE TABLE jobs (id TEXT); CREATE TABLE worker_owners (pid INTEGER);
      INSERT INTO worker_owners VALUES (123);
      CREATE TABLE artifacts (output_path TEXT);
      CREATE TABLE messages (content TEXT); CREATE TABLE model_output_chunks (data BLOB);
      CREATE TABLE events (data BLOB); CREATE TABLE agent_tasks (data BLOB); CREATE TABLE runtime_events (data BLOB);`)
    db.prepare('INSERT INTO artifacts VALUES (?)').run(join(source, 'outputs', 'tool.txt'))
    db.prepare('INSERT INTO messages VALUES (?)').run(content)
    db.prepare('INSERT INTO model_output_chunks VALUES (?)').run(Buffer.from(content))
    for (const table of ['events', 'agent_tasks', 'runtime_events']) {
      db.prepare(`INSERT INTO ${table} VALUES (?)`).run(Buffer.from(JSON.stringify({ outputPath: join(source, 'outputs', 'tool.txt') })))
    }
    db.close()
    saveHistoryState(source, seed)
    await rm(source, { recursive: true, force: true })
    for (const name of ['first', 'second']) {
      const target = join(root, name)
      restoreHistoryState(seed, target)
      if (process.platform !== 'win32') assert.equal((await stat(target)).mode & 0o777, 0o700)
      const restored = new DatabaseSync(join(target, 'vista.db'))
      try {
        assert.equal(restored.prepare('SELECT COUNT(*) AS count FROM worker_owners').get()!.count, 0)
        assert.equal(restored.prepare('SELECT content FROM messages').get()!.content, content)
        assert.deepEqual(restored.prepare('SELECT data FROM model_output_chunks').get()!.data, new Uint8Array(Buffer.from(content)))
        assert.equal(restored.prepare('SELECT output_path FROM artifacts').get()!.output_path, join(target, 'outputs', 'tool.txt'))
        for (const table of ['events', 'agent_tasks', 'runtime_events']) {
          const raw = restored.prepare(`SELECT data FROM ${table}`).get()!.data as Uint8Array
          assert.equal(JSON.parse(Buffer.from(raw).toString()).outputPath, join(target, 'outputs', 'tool.txt'))
        }
        assert.equal(await readFile(join(target, 'outputs', 'tool.txt'), 'utf8'), content)
        await assert.rejects(readFile(join(target, 'instance.json')), { code: 'ENOENT' })
        restored.exec("UPDATE messages SET content='changed only in this copy'")
      } finally { restored.close() }
    }
  } finally { await rm(root, { recursive: true, force: true }) }
})
