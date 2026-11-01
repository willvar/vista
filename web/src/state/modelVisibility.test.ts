import assert from 'node:assert/strict'
import test from 'node:test'
import { initialModelKey, visibleModels } from './modelVisibility.ts'
import type { ModelCatalog } from '../client/types'

const catalog: ModelCatalog = { default: 'unknown', models: [
  { key: 'unknown', id: 'private-id', provider: 'openai-chat', recognized: false },
  { key: 'text', id: 'text-only', provider: 'openai-chat', recognized: true, inputModalities: ['text'] },
  { key: 'vision', id: 'private-vision', provider: 'openai-chat', recognized: true, inputModalities: ['text', 'image'] },
] }

test('default filtering keeps declared text-only models and metadata-only aliases', () => {
  assert.deepEqual(visibleModels(catalog).map(model => model.key), ['text', 'vision'])
  assert.equal(initialModelKey(catalog), 'text')
  assert.equal(catalog.models.length, 3) // History and hidden counts retain the full inventory.
  assert.equal(visibleModels({ ...catalog, showUnrecognizedModels: true }).length, 3)
})

test('previous choices follow filtering while recognized choices retain their priority', () => {
  assert.equal(initialModelKey(catalog, ['removed', 'unknown']), 'text')
  assert.equal(initialModelKey(catalog, ['unknown', 'vision']), 'vision')
  assert.equal(initialModelKey(catalog, ['removed']), 'text')
  assert.equal(initialModelKey({ ...catalog, showUnrecognizedModels: true }, ['unknown']), 'unknown')
})

test('all-hidden inventories have no automatic default and can be explicitly revealed', () => {
  const unknown: ModelCatalog = { default: 'unknown', models: [catalog.models[0]!] }
  assert.deepEqual(visibleModels(unknown), [])
  assert.equal(initialModelKey(unknown), '')
  assert.equal(initialModelKey({ ...unknown, showUnrecognizedModels: true }), 'unknown')
  assert.equal(initialModelKey(unknown, ['unknown']), '')
})
