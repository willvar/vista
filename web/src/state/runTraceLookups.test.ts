import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { buildRunTrace, type RunTraceNode } from './runTrace.ts'
import { runTraceLookupRows, summarizeTraceLookups } from './runTraceLookups.ts'
import type { StreamEvent } from '../client/types.ts'

function tool(sequence: number, toolName: string, summary: string, overrides: Partial<RunTraceNode> = {}): RunTraceNode {
  return { key: `tool/${sequence}`, sequence, lastSequence: sequence, kind: 'tool', title: '', body: '',
    taskId: 'main', status: 'success', toolName, summary, artifactIds: [], jobIds: [], eventData: {}, ...overrides }
}

test('counts completed lookups without collapsing identical searches or counting repeated file pages twice', () => {
  const nodes = [tool(1, 'websearch', 'timeout'), tool(2, 'read_file', '/src/main.go'), tool(3, 'search', 'timeout'),
    tool(4, 'read_file', '/src/main.go', { rawBody: '{"startLine":50,"endLine":80}' }), tool(5, 'webfetch', 'https://example.test/docs?token=private'),
    tool(6, 'find_files', '/src'), tool(7, 'list_files', '/src'), tool(8, 'read_file', '/src/other.go', { status: 'loading' }),
    tool(9, 'webfetch', 'https://failed.test/', { status: 'error', error: 'Permission denied' }),
    tool(10, 'read_file', '/src/stopped.go', { status: 'abort' })]
  const [row] = runTraceLookupRows(nodes, [], {}, [])
  const summary = summarizeTraceLookups(row.members!)
  assert.deepEqual(summary.counts, { search: 3, file: 1, page: 1, directory: 1 })
  assert.equal(summary.preview.length, 5)
  assert.equal(summary.omitted, 1)
  assert.equal(summary.preview.find(step => step.kind === 'file')?.hint?.key, 'traceLookupLines')
  assert.equal(summary.preview.find(step => step.kind === 'page')?.label, 'example.test/docs')
  assert.deepEqual(summary.unsettled.map(step => step.hint?.key), ['statusRunning', 'statusFailed', 'statusInterrupted'])
  assert.equal(summary.unsettled[1].reason, 'Permission denied')
})

test('groups only adjacent steps from the same task, with messages and visible non-lookups as boundaries', () => {
  const nodes = [tool(1, 'read_file', '/a'), tool(2, 'search', 'a'), tool(4, 'read_file', '/b'), tool(5, 'webfetch', 'https://docs.test'),
    tool(6, 'shell', 'cat b'), tool(7, 'read_file', '/b'), tool(8, 'question', 'Choose'), tool(9, 'search', 'b'),
    tool(10, 'generate_image', 'Diagram'), tool(11, 'read_file', '/a'), tool(12, 'read_file', '/a', { taskId: 'child' }),
    tool(13, 'websearch', 'b', { taskId: 'child' }), tool(14, 'write_file', '/b'), tool(15, 'mcp/read_file', 'Custom')]
  const rows = runTraceLookupRows(nodes, [3], {}, [])
  assert.deepEqual(rows.map(row => row.members?.map(step => step.item.sequence) ?? row.item.sequence), [
    [1, 2], [4, 5], 6, 7, 8, 9, 10, 11, [12, 13], 14, 15,
  ])
})

test('growing a live group keeps open individual output and step identities intact', () => {
  const nodes = [tool(1, 'read_file', '/a'), tool(2, 'read_file', '/b', { status: 'loading' })]
  const key = 'lookup/tool/1'
  assert.deepEqual(runTraceLookupRows(nodes.slice(0, 1), [], {}, ['tool/1']).map(row => row.key), ['tool/1'])
  const rows = runTraceLookupRows(nodes, [], {}, ['tool/1'])
  assert.deepEqual(rows.map(row => row.key), [key, 'tool/1', 'tool/2'])
  assert.equal(rows[1].item, nodes[0])
  assert.deepEqual(runTraceLookupRows(nodes, [], { [key]: false }, ['tool/1']).map(row => row.key), [key])
  assert.equal(runTraceLookupRows([...nodes, tool(3, 'search', 'c')], [], { [key]: true }, [])[0].key, key)
})

test('replays known empty, partial and failed results without inferring outcomes from missing previews', () => {
  const event = (sequence: number, status: string, callId: string, content: string, truncated = false): StreamEvent => ({
    kind: 'tool', sequence, data: { runId: 'r', status, tool: { name: 'search', summary: callId, callId, truncated }, content },
  })
  const events = [event(1, 'succeeded', 'empty', '{"matches":[],"count":0}'),
    event(2, 'succeeded', 'partial', '{"matches":[],"truncated":true}'),
    event(3, 'succeeded', 'preview', '{"matches":[', true),
    event(4, 'failed', 'failed', '{"error":"Permission denied"}')]
  const { nodes } = buildRunTrace(events, { runId: 'r', active: false })
  const [row] = runTraceLookupRows(nodes, [], {}, [])
  assert.deepEqual(row.members!.map(step => step.hint?.key), ['traceNoMatches', 'traceSearchPartial', undefined, 'statusFailed'])
  assert.equal(summarizeTraceLookups(row.members!).counts.search, 3)
})
