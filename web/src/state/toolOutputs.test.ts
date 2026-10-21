import assert from 'node:assert/strict'
import test from 'node:test'
import { toolOutputErrors } from './toolOutputs.ts'

test('tool errors show once across event preview and artifact result', () => {
  const error = 'shell stopped: context deadline exceeded; partial output at output.log (5 bytes)'
  assert.deepEqual(toolOutputErrors({ error }, [{ id: 'a', runId: 'r', status: 'failed', bytes: 5, complete: false, error }], []), [error])
})

test('the full tool error replaces a shortened artifact error without hiding distinct errors', () => {
  const error = 'shell stopped: context deadline exceeded; partial output at output.log (5 bytes)'
  assert.deepEqual(toolOutputErrors({ error }, [{ id: 'a', runId: 'r', status: 'failed', bytes: 5, complete: false, error: error.slice(0, 30) }], [{ id: 'j', runId: 'r', directory: '.', command: 'work', status: 'failed', bytes: 0, exitCode: 1, complete: true, error: 'permission denied' }]), [error, 'permission denied'])
})
