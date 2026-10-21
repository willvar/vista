import assert from 'node:assert/strict'
import test from 'node:test'
import type { Message, ModelInfo, ModelResultView } from '../client/types'
import { catalogModelName, displayModelName, historicalModelInfo, messageModelIdentity, messageReasoningEffort, modelPickerOptions, modelSearchText, reasoningEffortSuffix } from './usePresentation.ts'

const reply: Message = { id: 'reply', runId: 'run', sequence: 10, role: 'assistant', content: 'Hello', createdAt: '', provider: 'openai-chat', modelId: 'model', outputTurn: 2 }
const result = (sequence: number, turn: number, effort?: string, depth = 0): ModelResultView => ({ sequence, turn, depth, runId: 'run', provider: 'openai-chat', id: 'model', finishReason: 'stop', reasoningEffort: effort })
const fromRun: Record<string, ModelInfo> = { run: { key: 'connection-model', provider: 'openai-chat', id: 'model', reasoningEffort: 'low' } }

test('model display uses the catalog name independently of the provider name', () => {
  const model: ModelInfo = { key: 'herdex-model', provider: 'openai-chat', name: 'Herdex', id: 'gpt-6-luna', modelName: 'GPT 6 Luna' }
  assert.equal(displayModelName(model), 'GPT 6 Luna')
  assert.equal(displayModelName({ ...model, modelName: undefined }), 'gpt-6-luna')
  assert.equal(displayModelName({ ...model, modelName: '  ' }), 'gpt-6-luna')
  assert.equal(model.name, 'Herdex')
})

test('historical model labels resolve exact catalog IDs and retain unknown raw IDs', () => {
  const catalog: ModelInfo[] = [
    { key: 'first', provider: 'openai-chat', id: 'gpt-6-luna' },
    { key: 'second', provider: 'openai-chat', id: 'gpt-6-luna', modelName: 'GPT 6 Luna' },
  ]
  assert.equal(catalogModelName('gpt-6-luna', catalog), 'GPT 6 Luna')
  assert.equal(catalogModelName('vendor/gpt-6-luna', catalog), 'vendor/gpt-6-luna')
  assert.equal(catalogModelName('gpt-6-luna', []), 'gpt-6-luna')
})

test('historical identity accepts provider IDs while preserving connection boundaries', () => {
  const catalog: ModelInfo[] = [{ key: 'remote', name: 'OCZG', provider: 'openai-chat', providerId: 'opencode-go', id: 'provider-model' }]
  const message = { ...reply, provider: 'opencode-go', modelId: 'provider-model', modelKey: 'remote' }
  const runModels = { run: { ...catalog[0] } }
  assert.equal(historicalModelInfo(messageModelIdentity(message, runModels), catalog)?.name, 'OCZG')
  assert.equal(historicalModelInfo({ key: 'removed', provider: 'opencode-go', id: 'provider-model' }, catalog)?.name, undefined)
  assert.equal(historicalModelInfo({ provider: 'opencode-go', id: 'provider-model' }, [catalog[0], { ...catalog[0], key: 'other' }])?.name, undefined)
})

test('usage model names accept provider IDs without using another provider name', () => {
  const catalog = [{ key: 'remote', provider: 'openai-chat', providerId: 'opencode-go', id: 'provider-model', modelName: 'Provider Model' }]
  assert.equal(catalogModelName('provider-model', catalog, 'opencode-go'), 'Provider Model')
  assert.equal(catalogModelName('provider-model', catalog, 'openai-chat'), 'Provider Model')
  assert.equal(catalogModelName('provider-model', catalog, 'another-provider'), 'provider-model')
})

test('historical labels retain their recorded provider and ID when the catalog is unavailable', () => {
  const identity = { key: 'remote', provider: 'opencode-go', id: 'provider-model' }
  assert.deepEqual(historicalModelInfo(identity, []), identity)
  const otherProvider = { key: 'other', provider: 'other-provider', id: 'provider-model', modelName: 'Unrelated model name', name: 'Other provider' }
  assert.deepEqual(historicalModelInfo(identity, [otherProvider]), identity)
  assert.deepEqual(historicalModelInfo({ provider: 'opencode-go', id: 'provider-model' }, [otherProvider]), { key: '', provider: 'opencode-go', id: 'provider-model' })
  assert.deepEqual(historicalModelInfo({ id: 'provider-model' }, [otherProvider]), { key: '', provider: '', id: 'provider-model' })
  assert.equal(historicalModelInfo({ provider: 'opencode-go' }, []), undefined)
})

test('historical identity cannot borrow a catalog label after the model or provider changes', () => {
  const configured = { key: 'remote', provider: 'openai-chat', providerId: 'opencode-go', id: 'provider-model', name: 'OCZG' }
  for (const identity of [
    { key: 'remote', provider: 'different-provider', id: 'provider-model' },
    { key: 'remote', provider: 'opencode-go', id: 'different-model' },
  ]) assert.deepEqual(historicalModelInfo(identity, [configured]), identity)
  assert.equal(historicalModelInfo({ key: 'remote', provider: 'openai-chat', id: 'provider-model' }, [configured])?.name, 'OCZG')
})

test('message identity fills missing fields only from a compatible request in its own run', () => {
  const message = { ...reply, modelKey: 'connection-model' }
  assert.deepEqual(messageModelIdentity(message, fromRun), { key: 'connection-model', provider: 'openai-chat', id: 'model' })
  assert.deepEqual(messageModelIdentity({ ...message, modelKey: undefined }, fromRun), { key: 'connection-model', provider: 'openai-chat', id: 'model' })
  assert.deepEqual(messageModelIdentity({ ...message, modelId: undefined }, fromRun), { key: 'connection-model', provider: 'openai-chat', id: 'model' })
  assert.deepEqual(messageModelIdentity({ ...message, provider: undefined }, { run: { ...fromRun.run, key: 'other-connection' } }), {
    key: 'connection-model', provider: undefined, id: 'model',
  })
  assert.deepEqual(messageModelIdentity({ ...message, provider: undefined }, { run: { ...fromRun.run, id: 'other-model' } }), {
    key: 'connection-model', provider: undefined, id: 'model',
  })
  assert.deepEqual(messageModelIdentity({ ...message, modelKey: undefined }, { run: { ...fromRun.run, provider: 'other-provider' } }), {
    key: undefined, provider: 'openai-chat', id: 'model',
  })
  assert.deepEqual(messageModelIdentity(message, { 'another-run': fromRun.run }), { key: 'connection-model', provider: 'openai-chat', id: 'model' })
})

test('model search retains catalog name, original ID and provider without changing the displayed name', () => {
  const model: ModelInfo = { key: 'herdex-model', provider: 'openai-chat', name: 'Herdex', id: 'gpt-6-luna', modelName: 'GPT 6 Luna' }
  const search = modelSearchText(model, 'Herdex · GPT 6 Luna').toLocaleLowerCase()
  for (const query of ['gpt 6 luna', 'gpt-6-luna', 'herdex']) assert.ok(search.includes(query))
  assert.equal(displayModelName(model), 'GPT 6 Luna')
})

test('shared model choices naturally sort provider groups and catalog names without mutating the catalog', () => {
  const models: ModelInfo[] = [
    { key: 'provider10-model', connectionId: 'connection10', name: 'Provider 10', provider: 'openai-chat', id: 'last-id', modelName: 'Model 1' },
    { key: 'provider2-ten', connectionId: 'connection2', name: 'Provider 2', provider: 'openai-chat', id: 'a-first-id', modelName: 'Model 10' },
    { key: 'provider2-two', connectionId: 'connection2', name: 'Provider 2', provider: 'openai-responses', id: 'z-last-id', modelName: 'model 2' },
    { key: 'provider2-raw', connectionId: 'connection2', name: 'Provider 2', provider: 'openai-chat', id: 'model 3' },
    { key: 'demo', name: 'Demo', provider: '', id: 'demo' },
  ]
  const keys = models.map(model => model.key)
  const providerLabel = (model: ModelInfo) => model.name!
  const fullLabel = (model: ModelInfo) => `${model.name} · ${displayModelName(model)}`
  for (const locale of ['en', 'zh-CN']) {
    const choices = modelPickerOptions(models, locale, providerLabel, fullLabel)
    assert.deepEqual(choices.map(group => group.label), ['Provider 2', 'Provider 10'])
    assert.deepEqual(choices[0].options.map(option => option.label), ['model 2', 'model 3', 'Model 10'])
    assert.deepEqual(choices[0].options.map(option => option.value), ['provider2-two', 'provider2-raw', 'provider2-ten'])
    assert.equal(choices[0].options[0].fullLabel, 'Provider 2 · model 2')
    assert.match(choices[0].options[0].searchText, /z-last-id/)
    assert.match(choices[0].options[0].searchText, /Provider 2/)
    assert.match(choices[0].options[0].searchText, /openai-responses/)
  }
  assert.deepEqual(models.map(model => model.key), keys)
})

test('model choices keep separate connections and use stable keys to order equal labels', () => {
  const common = { name: 'Same provider', provider: 'openai-chat', id: 'same-id', modelName: 'Same model' }
  const models: ModelInfo[] = [
    { ...common, key: 'model10', connectionId: 'connection2' },
    { ...common, key: 'other-model', connectionId: 'connection10' },
    { ...common, key: 'model2', connectionId: 'connection2' },
    { ...common, key: 'fallback-b', baseUrl: 'https://b.example/v1' },
    { ...common, key: 'fallback-a', baseUrl: 'https://a.example/v1' },
  ]
  const choices = modelPickerOptions(models, 'en', model => model.name!, model => `${model.name} · ${model.id}`)
  assert.equal(choices.length, 4)
  assert.deepEqual(choices.filter(group => group.key.startsWith('connection')).map(group => group.key), ['connection2', 'connection10'])
  assert.deepEqual(choices.find(group => group.key === 'connection2')?.options.map(option => option.value), ['model2', 'model10'])
  assert.deepEqual(choices, modelPickerOptions([...models].reverse(), 'en', model => model.name!, model => `${model.name} · ${model.id}`))
})

test('reasoning label suppresses missing or none efforts and translates known values', () => {
  const translations: Record<string, string> = { reasoningHigh: '高' }
  const t = (key: string, params?: Record<string, unknown>) => (translations[key] || key).replace('{value}', String(params?.value || ''))
  const te = (key: string) => key in translations
  for (const effort of [undefined, '', '  ', 'none', ' NONE ']) assert.equal(reasoningEffortSuffix(effort, t, te), '')
  assert.equal(reasoningEffortSuffix('high', t, te), ' · 高')
  assert.equal(reasoningEffortSuffix('adaptive', t, te), ' · adaptive')
})

test('reply header uses its final root result instead of older turns, child calls or the run fallback', () => {
  const results = [result(1, 1, 'medium'), result(4, 2, 'high'), result(5, 2, 'max', 1)]
  assert.equal(messageReasoningEffort(reply, fromRun, results), 'high')
  const persisted = { ...reply, modelResults: results }
  assert.equal(messageReasoningEffort(persisted, fromRun), 'high')
  assert.equal(fromRun.run.reasoningEffort, 'low')
})

test('an actual none or empty final effort is not replaced by another historical setting', () => {
  assert.equal(messageReasoningEffort(reply, fromRun, [result(3, 2, 'none')]), 'none')
  assert.equal(messageReasoningEffort(reply, fromRun, [result(3, 2, '')]), '')
})

test('missing result effort can use the actual request while other runs and model identities cannot', () => {
  assert.equal(messageReasoningEffort(reply, fromRun, [result(3, 2)]), 'low')
  assert.equal(messageReasoningEffort(reply, {}, [{ ...result(3, 2, 'max'), runId: 'other-run' }]), undefined)
  assert.equal(messageReasoningEffort(reply, {}, [{ ...result(3, 2, 'max'), id: 'other-model' }]), undefined)
  assert.equal(messageReasoningEffort(reply, { run: { ...fromRun.run, id: 'other-model' } }), undefined)
  assert.equal(messageReasoningEffort({ ...reply, modelKey: 'different-connection' }, fromRun), undefined)
  const aliasRun = { run: { ...fromRun.run, providerId: 'opencode-go' } }
  assert.equal(messageReasoningEffort({ ...reply, provider: 'opencode-go' }, aliasRun), 'low')
  assert.equal(messageReasoningEffort({ ...reply, provider: 'another-provider' }, aliasRun), undefined)
})

test('historical reasoning remains unchanged when the next run uses a different effort', () => {
  const original = { ...reply, modelResults: [result(4, 2, 'high')] }
  const updated = { ...fromRun, run: { ...fromRun.run, reasoningEffort: 'max' }, 'next-run': { ...fromRun.run, reasoningEffort: 'minimal' } }
  assert.equal(messageReasoningEffort(original, updated), 'high')
  assert.equal(messageReasoningEffort({ ...reply, runId: 'next-run' }, updated), 'minimal')
})
