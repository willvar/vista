import assert from 'node:assert/strict'
import test from 'node:test'
import { initialModelKey, visibleModels } from './modelVisibility.ts'
import type { ModelCatalog } from '@vista/client'

const catalog: ModelCatalog = { default: 'unknown', models: [
  { key: 'unknown', id: 'private-id', provider: 'openai-chat', recognized: false },
  { key: 'text', id: 'text-only', provider: 'openai-chat', recognized: true, inputModalities: ['text'] },
  { key: 'vision', id: 'private-vision', provider: 'openai-chat', recognized: true, inputModalities: ['text', 'image'] },
] }

test('default filtering keeps declared text-only models and metadata-only aliases', () => {
  assert.deepEqual(visibleModels(catalog).map(model => model.key), ['text', 'vision'])
  assert.equal(initialModelKey(catalog), '')
  assert.equal(catalog.models.length, 3) // History and hidden counts retain the full inventory.
  assert.equal(visibleModels({ ...catalog, showUnrecognizedModels: true }).length, 3)
})

test('previous choices follow filtering while recognized choices retain their priority', () => {
  assert.equal(initialModelKey(catalog, ['removed', 'unknown']), '')
  assert.equal(initialModelKey(catalog, ['unknown', 'vision']), 'vision')
  assert.equal(initialModelKey(catalog, ['removed']), '')
  assert.equal(initialModelKey({ ...catalog, showUnrecognizedModels: true }, ['unknown']), 'unknown')
})

test('all-hidden inventories have no automatic default and can be explicitly revealed', () => {
  const unknown: ModelCatalog = { default: 'unknown', models: [catalog.models[0]!] }
  assert.deepEqual(visibleModels(unknown), [])
  assert.equal(initialModelKey(unknown), '')
  assert.equal(initialModelKey({ ...unknown, showUnrecognizedModels: true }), 'unknown')
  assert.equal(initialModelKey(unknown, ['unknown']), '')
})


test('an unavailable saved model is not selectable even when unrecognized models are shown', () => {
  const retired: ModelCatalog = { models: [{ ...catalog.models[1]!, status: 'retired' }], default: 'text', showUnrecognizedModels: true }
  assert.equal(initialModelKey(retired, ['text']), '')
})

test('disabled connections stay out of the model picker even when every model is recognized or revealed', () => {
  const disabled = { ...catalog, showUnrecognizedModels: true, models: catalog.models.map(model => ({ ...model, disabled: model.key !== 'vision' })) }
  assert.deepEqual(visibleModels(disabled).map(model => model.key), ['vision'])
  assert.equal(initialModelKey(disabled, ['text', 'unknown']), 'vision')
  assert.equal(initialModelKey({ ...disabled, models: disabled.models.slice(0, 2) }, ['text']), '')
})
