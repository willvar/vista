import { createHash } from 'node:crypto'
import { lstat, open, realpath } from 'node:fs/promises'
import { join, resolve } from 'node:path'

const files = new Set(['model.onnx', 'tokenizer.json', 'tokenizer_config.json', 'config.json', 'special_tokens_map.json'])

async function readBounded(root, model, name, limit) {
  const folder = join(root, model.id)
  if (await realpath(folder) !== folder) throw Error('model directory must not be a link')
  const path = join(folder, name)
  const info = await lstat(path)
  if (!info.isFile() || info.size < 1 || info.size > limit) throw Error('invalid model file size or type')
  const file = await open(path, 'r')
  try {
    const stat = await file.stat()
    if (!stat.isFile() || stat.size !== info.size || stat.ino !== info.ino || stat.dev !== info.dev) throw Error('model file changed')
    const data = Buffer.allocUnsafe(stat.size)
    for (let offset = 0; offset < data.length;) {
      const { bytesRead } = await file.read(data, offset, data.length - offset, offset)
      if (bytesRead === 0) throw Error('incomplete model file')
      offset += bytesRead
    }
    return data
  } finally { await file.close() }
}

export async function resolveModelFiles(root, model) {
  if (model.source.kind === 'hub') return model.source.files
  const data = JSON.parse(await readBounded(resolve(root), model, 'model.json', 32768))
  if (data.modelId !== model.id || data.sourceRevision !== model.source.revision || !Array.isArray(data.files) || data.files.length > files.size) throw Error('invalid local export manifest')
  const seen = new Set()
  const result = data.files.map(({ name, size, sha256 }) => {
    const max = name === 'model.onnx' ? 1_500_000_000 : 16_777_216
    if (!files.has(name) || seen.has(name) || !Number.isSafeInteger(size) || size < 1 || size > max || !/^[a-f0-9]{64}$/u.test(sha256)) throw Error('invalid local export file')
    seen.add(name)
    return { name, bytes: size, sha256 }
  })
  if (!seen.has('model.onnx') || !seen.has('tokenizer.json')) throw Error('local export is incomplete')
  return result
}

export async function readModelArtifact(root, model, file) {
  const data = await readBounded(resolve(root), model, file.name, file.bytes)
  if (data.length !== file.bytes || createHash('sha256').update(data).digest('hex') !== file.sha256) throw Error('model artifact does not match the manifest')
  return data
}
