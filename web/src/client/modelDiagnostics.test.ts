import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeDiagnosticSummaries, type ModelDiagnosticSummary } from './modelDiagnostics.ts'

test('diagnostic index updates are scoped, ordered and deduplicated without bodies', () => {
  const a: ModelDiagnosticSummary = { id: 'a', sessionId: 'session', runId: 'run', provider: 'mock', protocol: 'chat', model: 'fixture', startedAt: '2026-10-25T00:00:00Z', durationMs: 10, truncated: false }
  const b = { ...a, id: 'b', startedAt: '2026-10-25T00:00:01Z' }
  const updated = { ...a, durationMs: 20 }
  assert.deepEqual(mergeDiagnosticSummaries([a], [b, updated, { ...b, id: 'foreign', sessionId: 'other' }, { ...b, id: 'other-run', runId: 'other' }], 'session', 'run'), [updated, b])
  assert.equal(a.durationMs, 10)
})
