// Loopback-only optional service; no model material is fetched or sent online.
import { createServer } from 'node:http'
import { realpath } from 'node:fs/promises'
import { ModelCache } from './classifier.mjs'
import { findModel } from './models.mjs'

const [dir, id, rawPort = '8189'] = process.argv.slice(2)
const model = findModel(id)
const port = Number(rawPort)
if (!dir || !model || !Number.isInteger(port) || port < 0 || port > 65535) {
  console.error('Usage: node server.mjs <model-directory> <known-model-id> [port]')
  process.exit(2)
}
const cache = new ModelCache({ modelDir: await realpath(dir), maxLoadedModels: 1, threads: 2 })
const classifier = await cache.get(model.id)
const host = '127.0.0.1'
let busy = false
function json(res, status, payload) {
  if (!res.destroyed) res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }).end(JSON.stringify(payload))
}

function prepare(body) {
  if (body.model !== model.id || typeof body.state !== 'string' || !body.state.trim() || !body.questions || Array.isArray(body.questions) || typeof body.questions !== 'object') throw Error('invalid batch')
  const rows = Object.entries(body.questions)
  if (rows.length < 1 || rows.length > 16) throw Error('invalid question count')
  return rows.map(([name, q]) => {
    if (!name || name.length > 64 || !q || typeof q.instructions !== 'string' || !q.instructions.trim()) throw Error('invalid question')
    let labels, descriptions
    if (q.type === 'noul') {
      labels = ['true', 'false']
      descriptions = { true: `The condition is met: ${q.instructions}`, false: `The condition is not met: ${q.instructions}` }
    } else if (q.type === 'score' && Array.isArray(q.criteria)) {
      labels = q.criteria.map((_, i) => String(i))
      descriptions = Object.fromEntries(labels.map((label, i) => [label, q.criteria[i]]))
    } else if (q.type === 'choice' && q.criteria && typeof q.criteria === 'object' && !Array.isArray(q.criteria)) {
      labels = Object.keys(q.criteria)
      descriptions = q.criteria
    } else throw Error('invalid question type')
    if (labels.length < 2 || labels.length > 16 || labels.some(k => !k.trim() || typeof descriptions[k] !== 'string' || !descriptions[k].trim())) throw Error('invalid rubric')
    return { name, type: q.type, text: body.state, labels, descriptions, instructions: q.instructions, task: q.type }
  })
}

const server = createServer({ requestTimeout: 45000 }, async (req, res) => {
  if (req.headers.host !== `${host}:${server.address().port}` || req.headers.origin) return json(res, 403, { error: 'loopback access only' })
  if (req.method === 'GET' && req.url === '/v1/models') {
    return json(res, 200, { data: [{ id: model.id, name: model.name, type: 'decision', input_modalities: ['text'] }] })
  }
  if (req.method !== 'POST' || req.url !== '/v1/systemone') return json(res, 404, { error: 'unknown route' })
  if (req.headers['content-type'] !== 'application/json') return json(res, 415, { error: 'JSON required' })
  if (busy) return json(res, 503, { error: 'local model is busy' })
  busy = true
  try {
    let size = 0
    const chunks = []
    for await (const chunk of req) {
      size += chunk.length
      if (size > 1_048_576) return json(res, 413, { error: 'request too large' })
      chunks.push(chunk)
    }
    let rows
    try { rows = prepare(JSON.parse(Buffer.concat(chunks))) }
    catch { return json(res, 400, { error: 'invalid decision input' }) }
    const answers = Object.create(null)
    let inputTokens = 0
    for (const item of rows) {
      if (res.destroyed) return
      const result = await classifier.classify(item)
      inputTokens += result.inputTokens
      const max = Math.max(...result.logits)
      const weights = result.logits.map(x => Math.exp(x - max))
      const sum = weights.reduce((acc, p) => acc + p, 0)
      const p = weights.map(x => x / sum)
      const probabilities = Object.fromEntries(item.labels.map((name, i) => [name, p[i]]))
      const best = p.indexOf(Math.max(...p))
      if (item.type === 'noul') answers[item.name] = { type: 'noul', noul: p[0] }
      else if (item.type === 'choice') answers[item.name] = { type: 'choice', choice: item.labels[best], confidence: p[best], probabilities }
      else answers[item.name] = { type: 'score', score: p.reduce((acc, w, i) => acc + w * i, 0), confidence: p[best], legend: item.descriptions, probabilities }
    }
    json(res, 200, { model: model.id, answers, usage: { input_tokens: inputTokens, output_tokens: 0 } })
  } catch { json(res, 422, { error: 'local model could not evaluate the decision' }) }
  finally { busy = false }
})
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => server.close(() => void cache.close().catch(() => { process.exitCode = 1 })))
server.listen(port, host, () => console.log(`ONNX decisions: http://${host}:${server.address().port}/v1`))
