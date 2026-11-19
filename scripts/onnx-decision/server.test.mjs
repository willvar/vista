import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { once } from 'node:events'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

// A valid ONNX graph with symbolic text inputs and two fixed logits. It checks
// the native engine/shape boundary without distributing model weights.
const graph = Buffer.from('CAg6kgESG3RpbnkgY2xhc3NpZmljYXRpb24gZml4dHVyZSoYCAEIAhABQgZsb2dpdHNKCAAAAEAAAIC/WhwKCWlucHV0X2lkcxIPCg0IBxIJCgIIAQoDEgFOWiEKDmF0dGVudGlvbl9tYXNrEg8KDQgHEgkKAggBCgMSAU5iGAoGbG9naXRzEg4KDAgBEggKAggBCgIIAkICEA0=', 'base64')
const model = 'gliclass-instruct-edge-v1.0'

test('native local decision inventory and all three result types', { timeout: 10000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'vista-onnx-test-'))
  let child, done
  try {
    const dir = join(root, model)
    await mkdir(dir)
    const tokens = ['[UNK]', '<<LABEL>>', '<<SEP>>', '<<EXAMPLE>>', 'red', 'blue', 'Sample']
    const files = {
      'model.onnx': graph,
      'tokenizer.json': Buffer.from(JSON.stringify({
        model: { type: 'WordLevel', unk_token: '[UNK]', vocab: Object.fromEntries(tokens.map((v, i) => [v, i])) },
        added_tokens: tokens.slice(1, 4).map((content, i) => ({ id: i+1, content, single_word: false, normalized: false, special: true })),
        version: '1.0', normalizer: null, post_processor: null, decoder: null, padding: null, truncation: null,
        pre_tokenizer: { type: 'Whitespace' },
      })),
      'tokenizer_config.json': Buffer.from('{"unk_token":"[UNK]"}'),
    }
    const manifests = []
    for (const [name, data] of Object.entries(files)) {
      await writeFile(join(dir, name), data)
      manifests.push({ name, size: data.length, sha256: createHash('sha256').update(data).digest('hex') })
    }
    await writeFile(join(dir, 'model.json'), JSON.stringify({ modelId: model, sourceRevision: '727be8a417f6a7718e591b025e07054c146d8139', files: manifests }))
    child = spawn(process.execPath, [fileURLToPath(new URL('./server.mjs', import.meta.url)), root, model, '0'], { stdio: ['ignore','pipe','inherit'] })
    done = once(child, 'exit')
    const lines = createInterface({ input: child.stdout })
    const [line] = await Promise.race([once(lines, 'line', { signal: AbortSignal.timeout(5000) }), done.then(([code]) => { throw Error(`ONNX process exited: ${code}`) })])
    lines.close()
    const base = line.match(/http:\/\/127\.0\.0\.1:\d+\/v1$/u)?.[0]
    assert.ok(base)
    const models = await (await fetch(base + '/models', { signal: AbortSignal.timeout(3000) })).json()
    assert.deepEqual(models.data.map(({ id, type }) => [id, type]), [[model, 'decision']])
    const response = await fetch(base + '/systemone', { method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: AbortSignal.timeout(3500), body: JSON.stringify({
      model, state: 'Sample evidence', questions: { yes: { type: 'noul', instructions: 'Is it ready?' }, choice: { type: 'choice', instructions: 'Pick a color', criteria: { red: 'red', blue: 'blue' } }, score: { type: 'score', instructions: 'Quality', criteria: ['poor', 'good'] } },
    }) })
    assert.equal(response.status, 200)
    const result = await response.json()
    assert.ok(result.answers.yes.noul > 0.9)
    assert.equal(result.answers.choice.choice, 'red')
    assert.ok(result.answers.score.score > 0 && result.answers.score.score < 0.1 && result.usage.input_tokens > 0)
    assert.equal((await fetch(base + '/models', { headers: { Origin: 'https://outside.test' }, signal: AbortSignal.timeout(2500) })).status, 403)
  } finally {
    if (child) { child.kill('SIGTERM'); await done }
    await rm(root, { recursive: true, force: true })
  }
})
