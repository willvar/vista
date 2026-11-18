import test from 'node:test'
import assert from 'node:assert/strict'
import { generationCards } from './generationCards.ts'

const running = { sequence: 6, kind: 'tool', data: { runId: 'run', status: 'started', tool: { name: 'generate_video', callId: 'tool-1', sourceSequence: 5 } } }

test('generation projection keeps the original message and ignores a late duplicate start', () => {
  const hash = '0'.repeat(64)
  const done = { sequence: 22, kind: 'tool', data: { ...running.data, status: 'succeeded', tool: { ...running.data.tool, generated: { hash } } } }
  const pending = generationCards([running]).get(5)?.[0]
  assert.equal(pending?.status, 'started')
  const result = generationCards([running, done, running])
  assert.equal(result.size, 1)
  assert.deepEqual(result.get(5), [{ key: pending?.key, kind: 'video', runId: 'run', source: 5, status: 'succeeded', hash }])
})

test('stopped execution is not reported as upstream cancellation', () => {
  assert.equal(generationCards([running, { sequence: 9, kind: 'run_finished', data: { runId: 'run' } }]).get(5)?.[0]?.status, 'stopped')
  const canceled = { sequence: 9, kind: 'tool', data: { ...running.data, status: 'failed', tool: { ...running.data.tool, mediaState: 'canceled' } } }
  assert.equal(generationCards([running, canceled]).get(5)?.[0]?.status, 'canceled')
})
