import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope } from 'vue'
import type { VistaClient } from '../client/vista'
import { useInteractionPreferences } from './useInteractionPreferences.ts'

test('remembering the current model does not rewrite its key but changing models persists it', async t => {
  const patches: unknown[] = []
  const client = {
    async request(path: string, options?: RequestInit) {
      assert.equal(path, '/preferences')
      if (options?.method === 'PATCH') { patches.push(JSON.parse(String(options.body))); return {} }
      return { lastMode: 'manual', lastTier: 'speed', manualModelKey: 'first', reasoningByModelId: {} }
    },
  } as VistaClient
  const scope = effectScope()
  t.after(() => scope.stop())
  const preferences = scope.run(() => useInteractionPreferences(client))!
  await preferences.load()
  preferences.rememberedModel.value = { key: 'first', id: 'model-a', reasoningEffort: 'low' }
  preferences.rememberedModel.value = { key: 'first', id: 'model-a', reasoningEffort: 'high' }
  await preferences.flush()
  assert.deepEqual(patches, [])
  preferences.rememberedModel.value = { key: 'second', id: 'model-b', reasoningEffort: 'high' }
  await preferences.flush()
  assert.deepEqual(patches, [{ manualModelKey: 'second' }])
  assert.equal(preferences.rememberedModel.value.reasoningEffort, 'high')
})
