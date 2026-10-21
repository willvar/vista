import assert from 'node:assert/strict'
import test from 'node:test'
import type { StreamEvent } from '../client/types.ts'
import { buildRunTrace, buildTraceAgentHierarchy, projectToolOutput, toolOperationLabelKey, type RunTraceTask } from './runTrace.ts'

const event = (sequence: number, kind: string, data: any = {}): StreamEvent => ({ sequence, kind, createdAt: '2026-10-03T00:00:00Z', data: { runId: 'run', taskId: 'main', ...data } })
const options = { runId: 'run', active: true }

test('memory operations identify their changes and show saved preferences without IDs', () => {
  assert.equal(toolOperationLabelKey('memory'), 'traceOpMemory')
  for (const [action, summary] of [['add', 'memoryAdded'], ['update', 'memoryUpdated'], ['remove', 'memoryRemoved']]) {
    const output = projectToolOutput(JSON.stringify({ action, id: 'opaque-id', text: 'Keep answers concise.' }), 'memory')
    assert.equal(output.body, 'Keep answers concise.')
    assert.equal(output.resultSummaryKey, summary)
  }
  assert.equal(projectToolOutput(JSON.stringify({ memories: [{ id: 'opaque', text: 'Use metric units.' }] }), 'memory').body, 'Use metric units.')
  assert.equal(projectToolOutput('{"memories":[]}', 'memory').resultSummaryKey, 'memoriesEmpty')
  assert.equal(projectToolOutput('{"error":"concurrent change"}', 'memory').error, 'concurrent change')
})

test('terminal events retain run status without creating execution steps or duplicating the reply', () => {
  const content = "Agent run failed: model HTTP 403 (FreeTierError): OpenCode's free tier can only be used from within OpenCode."
  for (const status of ['failed', 'interrupted', 'canceled', 'succeeded']) {
    const finished = event(3, 'run_finished', { status, reason: 'Worker exited', message: { failure: status === 'failed', role: 'assistant', content } })
    for (const active of [true, false]) {
      const trace = buildRunTrace([
        event(1, 'model_request', { turn: 1, id: 'model' }),
        event(2, 'agent_task', { task: { id: 'main', status } }),
        finished,
      ], { ...options, active })
      assert.equal(trace.finished, finished)
      assert.deepEqual(trace.nodes, [])
      assert.deepEqual(trace.tasks[0]?.nodes, [])
    }
  }
  const modelOnly = buildRunTrace([
    event(1, 'model_request', { turn: 1, id: 'model' }),
    event(2, 'model_reasoning_delta', { turn: 1, content: 'Main reasoning' }),
    event(3, 'model_delta', { turn: 1, content: 'Main answer' }),
  ], options)
  assert.deepEqual(modelOnly.nodes.map(node => node.body), ['Main reasoning'])
  assert.deepEqual(buildRunTrace([event(1, 'model_delta', { turn: 1, content: 'Answer' })], options).nodes, [])
})

test('detached descendants retain live steps after foreground finalization', () => {
  const events = [
    event(1, 'agent_task', { taskId: 'child', parentTaskId: 'main', task: { id: 'child', parentId: 'main', join: 'detached', title: 'background', status: 'started' } }),
    event(2, 'agent_task', { taskId: 'grandchild', parentTaskId: 'child', task: { id: 'grandchild', parentId: 'child', join: 'required', title: 'nested', status: 'started' } }),
    event(3, 'tool', { taskId: 'grandchild', status: 'started', tool: { callId: 'read', name: 'read_file' } }),
    event(4, 'run_finished', { status: 'succeeded' }),
  ]
  const trace = buildRunTrace(events, { ...options, active: false })
  assert.equal(trace.tasks.find(task => task.id === 'grandchild')?.background, true)
  assert.equal(trace.nodes.find(node => node.kind === 'tool')?.status, 'loading')
  const stopped = buildRunTrace([...events, event(5, 'agent_task', { taskId: 'grandchild', task: { id: 'grandchild', status: 'canceled' } })], { ...options, active: false })
  assert.equal(stopped.nodes.find(node => node.kind === 'tool')?.status, 'abort')
})

const hierarchyTask = (id: string, sequence: number, parentId?: string): RunTraceTask => ({ id, sequence, lastSequence: sequence, parentId, title: 'Same delegated instructions', status: 'started', nodes: [] })
const hierarchyIdentity = (tasks: readonly RunTraceTask[]) => buildTraceAgentHierarchy(tasks).map(({ task, number, depth, parentId }) => ({ id: task.id, number, depth, parentId }))

test('agent hierarchy identifies the main agent and distinct siblings with depth-first delegation order', () => {
  const tasks = [hierarchyTask('child-b', 3, 'main'), hierarchyTask('grandchild', 4, 'child-a'), hierarchyTask('main', 10), hierarchyTask('child-a', 2, 'main')]
  assert.deepEqual(hierarchyIdentity(tasks), [
    { id: 'main', number: 0, depth: 0, parentId: undefined },
    { id: 'child-a', number: 1, depth: 1, parentId: 'main' },
    { id: 'grandchild', number: 3, depth: 2, parentId: 'child-a' },
    { id: 'child-b', number: 2, depth: 1, parentId: 'main' },
  ])
  assert.equal(tasks[0]?.id, 'child-b', 'the input order is not mutated')
})

test('agent numbers ignore activity updates and stay stable when later agents arrive', () => {
  const original = [hierarchyTask('main', 1), hierarchyTask('a', 2, 'main'), hierarchyTask('b', 3, 'main')]
  const updated = original.map(task => ({ ...task, lastSequence: task.id === 'a' ? 500 : 100, status: 'succeeded' }))
  assert.deepEqual(hierarchyIdentity(updated), hierarchyIdentity(original))
  const added = hierarchyIdentity([...updated, hierarchyTask('new-child', 10, 'a')])
  assert.deepEqual(added.filter(task => task.id !== 'new-child'), hierarchyIdentity(original))
  assert.deepEqual(added.find(task => task.id === 'new-child'), { id: 'new-child', number: 3, depth: 2, parentId: 'a' })
})

test('missing parents, self references, and cycles remain visible without false ancestry', () => {
  const tasks = [hierarchyTask('main', 1, 'cycle-a'), hierarchyTask('missing', 2, 'absent'), hierarchyTask('cycle-a', 3, 'cycle-b'),
    hierarchyTask('cycle-b', 4, 'cycle-a'), hierarchyTask('self', 5, 'self'), hierarchyTask('leaf', 6, 'cycle-a')]
  const hierarchy = hierarchyIdentity(tasks)
  assert.deepEqual(hierarchy.map(task => task.id), ['main', 'missing', 'cycle-a', 'leaf', 'cycle-b', 'self'])
  for (const id of ['missing', 'cycle-a', 'cycle-b', 'self']) {
    assert.equal(hierarchy.find(task => task.id === id)?.parentId, undefined)
    assert.equal(hierarchy.find(task => task.id === id)?.depth, 1)
  }
  assert.deepEqual(hierarchy.find(task => task.id === 'leaf'), { id: 'leaf', number: 5, depth: 2, parentId: 'cycle-a' })
  assert.equal(tasks.find(task => task.id === 'cycle-a')?.parentId, 'cycle-b', 'the recorded parent is not mutated')
})

test('a hierarchy without the main agent keeps partial-history agents and sorts sequence ties by identity', () => {
  assert.deepEqual(hierarchyIdentity([hierarchyTask('b', 2, 'main'), hierarchyTask('a', 2, 'main'), hierarchyTask('nested', 3, 'b')]), [
    { id: 'a', number: 1, depth: 1, parentId: undefined },
    { id: 'b', number: 2, depth: 1, parentId: undefined },
    { id: 'nested', number: 3, depth: 2, parentId: 'b' },
  ])
  assert.deepEqual(buildTraceAgentHierarchy([]), [])
})

test('reasoning and text belong to actual sibling task identities, independently of depth and turn', () => {
  const events = [
    event(1, 'agent_task', { task: { id: 'a', parentId: 'main', title: 'Read code', status: 'started' } }),
    event(2, 'agent_task', { task: { id: 'b', parentId: 'main', title: 'Review docs', status: 'started' } }),
    event(3, 'model_reasoning_delta', { taskId: 'a', depth: 1, turn: 2, content: 'Reason A' }),
    event(4, 'model_reasoning_delta', { taskId: 'b', depth: 1, turn: 2, content: 'Reason B' }),
    event(5, 'model_delta', { taskId: 'a', depth: 1, turn: 2, content: 'Result A' }),
    event(6, 'model_delta', { taskId: 'b', depth: 1, turn: 2, content: 'Result B' }),
    event(7, 'model_result', { taskId: 'a', depth: 1, turn: 2, modelResult: { id: 'model-a', reasoningEffort: 'high' } }),
    event(8, 'agent_task', { task: { id: 'a', parentId: 'main', title: 'Read code', status: 'succeeded', report: { runId: 'run', turn: 2, bytes: 999 } } }),
  ]
  const trace = buildRunTrace(events, options)
  const a = trace.tasks.find(task => task.id === 'a')!
  const b = trace.tasks.find(task => task.id === 'b')!
  assert.deepEqual(a.nodes.map(node => node.body), ['Reason A', 'Result A'])
  assert.deepEqual(b.nodes.map(node => node.body), ['Reason B', 'Result B'])
  assert.equal(a.nodes[0]?.status, 'success')
  assert.equal(b.nodes[0]?.status, 'loading')
  assert.equal(a.report?.bytes, 999)
  assert.equal(a.lastSequence, 8)
})

test('a child return and later unrelated successes never resolve failures or complete checklist items', () => {
  const trace = buildRunTrace([
    event(1, 'checklist', { items: [{ text: 'Fix production bug', status: 'blocked' }] }),
    event(2, 'tool', { status: 'started', tool: { callId: 'bad', name: 'shell' } }),
    event(3, 'tool', { status: 'started', tool: { callId: 'good', name: 'read_file' } }),
    event(4, 'tool', { status: 'failed', tool: { callId: 'bad', name: 'shell' }, content: '{"error":"network refused","artifactId":"hidden-id"}' }),
    event(5, 'tool', { status: 'succeeded', tool: { callId: 'good', name: 'read_file' }, content: '{"content":"File text"}' }),
    event(6, 'agent_task', { task: { id: 'child', parentId: 'main', title: 'Inspect bug', status: 'succeeded' } }),
  ], options)
  const failures = trace.nodes.filter(node => node.status === 'error')
  assert.equal(failures.length, 1)
  assert.equal(failures[0]?.error, 'network refused')
  assert.equal(failures[0]?.sequence, 2)
  assert.equal(failures[0]?.lastSequence, 4)
  assert.equal(trace.checklist?.[0]?.status, 'blocked')
})

test('successful async delegation and waiting keep each child running until its own outcome arrives', () => {
  const events = [
    event(1, 'agent_task', { task: { id: 'main', title: 'Investigate', status: 'started' } }),
    event(2, 'agent_task', { taskId: 'main/a', depth: 1, task: { id: 'main/a', parentId: 'main', title: 'Read code', status: 'started' } }),
    event(3, 'agent_task', { taskId: 'main/b', depth: 1, task: { id: 'main/b', parentId: 'main', title: 'Read docs', status: 'started' } }),
    event(4, 'tool', { status: 'succeeded', tool: { callId: 'a', name: 'delegate' }, content: '{"taskId":"main/a","status":"running"}' }),
    event(5, 'tool', { status: 'succeeded', tool: { callId: 'b', name: 'delegate' }, content: '{"taskId":"main/b","status":"running"}' }),
    event(6, 'model_reasoning_delta', { taskId: 'main/a', depth: 1, turn: 2, content: 'Inspect code' }),
    event(7, 'model_reasoning_delta', { taskId: 'main/b', depth: 1, turn: 3, content: 'Inspect docs' }),
  ]
  const running = buildRunTrace(events, options)
  assert.equal(running.nodes.find(node => node.callId === 'a')?.status, 'success')
  assert.equal(running.nodes.find(node => node.callId === 'a')?.resultSummaryKey, 'traceAgentStarted')
  assert.equal(running.tasks.find(task => task.id === 'main/a')?.status, 'started')
  assert.equal(running.tasks.find(task => task.id === 'main/a')?.nodes[0]?.status, 'loading')

  const returned = buildRunTrace([...events,
    event(8, 'agent_task', { taskId: 'main/a', depth: 1, task: { id: 'main/a', parentId: 'main', title: 'Read code', status: 'succeeded', report: { runId: 'run', turn: 2, bytes: 99 } } }),
    event(9, 'tool', { status: 'succeeded', tool: { callId: 'wait', name: 'await_agent' } }),
    event(10, 'tool', { status: 'succeeded', tool: { callId: 'cancel', name: 'cancel_agent' } }),
  ], options)
  assert.equal(returned.tasks.find(task => task.id === 'main')?.status, 'started')
  assert.equal(returned.tasks.find(task => task.id === 'main/a')?.status, 'succeeded')
  assert.equal(returned.tasks.find(task => task.id === 'main/a')?.report?.bytes, 99)
  assert.equal(returned.tasks.find(task => task.id === 'main/b')?.status, 'started')
  assert.equal(returned.tasks.find(task => task.id === 'main/b')?.nodes[0]?.status, 'loading')

  const canceled = buildRunTrace([...events,
    event(8, 'agent_task', { taskId: 'main/b', depth: 1, task: { id: 'main/b', parentId: 'main', title: 'Read docs', status: 'canceled' } }),
  ], options)
  assert.equal(canceled.tasks.find(task => task.id === 'main/b')?.nodes[0]?.status, 'abort')
})

test('an explicit retry preserves the failed attempt and associates output with the owning call and task', () => {
  const trace = buildRunTrace([
    event(1, 'tool', { status: 'started', taskId: 'a', tool: { name: 'shell', callId: 'call' } }),
    event(2, 'tool', { status: 'failed', taskId: 'a', tool: { name: 'shell', callId: 'call' }, content: 'first failure' }),
    event(3, 'tool', { status: 'started', taskId: 'a', tool: { name: 'shell', callId: 'call' } }),
    event(4, 'artifact', { taskId: 'a', callId: 'call', id: 'artifact-a' }),
    event(5, 'job', { taskId: 'b', callId: 'call', id: 'job-b' }),
    event(6, 'tool', { status: 'succeeded', taskId: 'a', tool: { name: 'shell', callId: 'call', outputRef: { artifactId: 'artifact-a', bytes: 42 } }, content: 'retry result' }),
  ], options)
  assert.equal(trace.nodes.length, 2)
  assert.equal(trace.nodes.find(node => node.status === 'error')?.body, 'first failure')
  const retry = trace.nodes[1]!
  assert.deepEqual(retry.artifactIds, ['artifact-a'])
  assert.deepEqual(retry.jobIds, [])
  assert.equal(retry.outputRef?.bytes, 42)
})

test('missing outcomes are not invented when loading only a bounded piece of completed history', () => {
  const trace = buildRunTrace([event(1, 'tool', { status: 'started', tool: { name: 'shell', callId: 'call' } })], { runId: 'run', active: false })
  assert.equal(trace.nodes[0]?.status, undefined)
  assert.equal(trace.tasks[0]?.status, '')
})

test('main and child reasoning remain complete in the trace while main text stays in messages', () => {
  const trace = buildRunTrace([
    event(1, 'model_reasoning_delta', { taskId: 'child', depth: 1, turn: 2, content: '思'.repeat(5000) }),
    event(2, 'model_delta', { turn: 2, content: 'Final reply' }),
    event(3, 'model_reasoning_delta', { turn: 2, content: 'Main reasoning' }),
  ], { runId: 'run', active: false, replyTurns: [2] })
  assert.equal(trace.nodes.length, 2)
  assert.equal(trace.nodes[0]?.truncated, undefined)
  assert.equal(trace.nodes[0]?.body, '思'.repeat(5000))
  assert.equal(trace.nodes[1]?.body, 'Main reasoning')
})

test('main reasoning follows event order alongside tools and child output, excluding main text', () => {
  const events = [
    event(1, 'model_reasoning_delta', { turn: 1, content: 'Consider the first question' }),
    event(2, 'model_delta', { turn: 1, content: 'First answer' }),
    event(3, 'model_result', { turn: 1, modelResult: { finishReason: 'stop' } }),
    event(4, 'model_delta', { turn: 2, content: 'Checking the requested price' }),
    event(5, 'model_result', { turn: 2, modelResult: { finishReason: 'tool_calls' } }),
    event(6, 'tool', { turn: 2, status: 'succeeded', tool: { name: 'websearch', callId: 'search' }, content: 'Price found' }),
    event(7, 'model_delta', { turn: 1, depth: 1, taskId: 'child', content: 'Child result with the same turn number' }),
    event(8, 'model_delta', { turn: 3, content: 'Followup answer' }),
    event(9, 'model_result', { turn: 3, modelResult: { finishReason: 'stop' } }),
  ]
  for (const active of [true, false]) {
    const trace = buildRunTrace(events, { runId: 'run', active, replyTurns: [1, 3] })
    assert.deepEqual(trace.nodes.filter(node => node.kind === 'text' && node.taskId === 'main').map(node => node.turn), [])
    assert.equal(trace.nodes.find(node => node.taskId === 'child')?.body, 'Child result with the same turn number')
    assert.deepEqual(trace.nodes.map(node => node.kind), ['reasoning', 'tool', 'text'])
    assert.equal(trace.nodes[0]?.body, 'Consider the first question')
    assert.equal(trace.nodes.some(node => node.kind === 'tool'), true)
  }
})

test('followup trace ranges isolate model identity, checklist, failures and execution completion', () => {
  const events = [
    event(1, 'agent_task', { task: { id: 'main', title: 'First question', status: 'started' } }),
    event(2, 'model_request', { turn: 1, id: 'first-model', reasoningEffort: 'low' }),
    event(3, 'checklist', { items: [{ text: 'Explain the recommendation', status: 'done' }] }),
    event(4, 'model_delta', { turn: 1, content: 'First answer' }),
    event(5, 'model_result', { turn: 1, modelResult: { finishReason: 'stop', id: 'first-model' } }),
    event(6, 'message', { role: 'assistant', outputTurn: 1 }),
    event(7, 'message', { role: 'user', content: 'What does it cost?' }),
    event(8, 'model_request', { turn: 2, id: 'followup-model', reasoningEffort: 'high' }),
    event(9, 'checklist', { items: [{ text: 'Check current prices', status: 'blocked' }] }),
    event(10, 'tool', { turn: 2, status: 'failed', tool: { name: 'websearch', callId: 'prices' }, content: 'Price endpoint unavailable' }),
    event(11, 'agent_task', { task: { id: 'main', title: 'First question', status: 'failed' } }),
    event(12, 'run_finished', { status: 'failed', reason: 'Search failed' }),
  ]
  const first = buildRunTrace(events, { runId: 'run', active: false, after: 0, through: 6, replyTurns: [1] })
  assert.equal(first.finished, undefined)
  assert.equal(first.nodes.some(node => node.status === 'error'), false)
  assert.equal(first.tasks[0]?.modelId, 'first-model')
  assert.equal(first.tasks[0]?.reasoningEffort, 'low')
  assert.equal(first.tasks[0]?.status, 'started')
  assert.deepEqual(first.checklist, [{ text: 'Explain the recommendation', status: 'done' }])
  assert.equal(first.nodes.some(node => node.kind === 'tool'), false)
  const followup = buildRunTrace(events, { runId: 'run', active: false, after: 6, replyTurns: [1] })
  assert.equal(followup.finished?.sequence, 12)
  assert.equal(followup.tasks[0]?.modelId, 'followup-model')
  assert.equal(followup.tasks[0]?.reasoningEffort, 'high')
  assert.deepEqual(followup.checklist, [{ text: 'Check current prices', status: 'blocked' }])
  assert.equal(followup.nodes.find(node => node.kind === 'tool')?.callId, 'prices')
  assert.ok(followup.nodes.every(node => node.sequence > 6))
})

test('JSON metadata remains in technical details while visible output uses known fields', () => {
  const raw = '{"artifactId":"uuid","outputPath":"/private/path","preview":"Useful output"}'
  assert.deepEqual(projectToolOutput(raw), { body: 'Useful output', rawBody: raw })
  assert.equal(projectToolOutput('{"answer":"**report**"}').markdown, true)
  assert.equal(projectToolOutput('{"unknown":"uuid"}').body, '')
  assert.deepEqual(projectToolOutput('plain output'), { body: 'plain output' })
  assert.equal(projectToolOutput('{"artifactId":"uuid","preview":"truncated').body, '')
  assert.equal(projectToolOutput('[INFO] plain log').body, '[INFO] plain log')
})

test('overview failure keys stay stable when the original start arrives in a history page', () => {
  const failed = event(10, 'tool', { status: 'failed', tool: { name: 'shell', callId: 'call' }, content: 'failed' })
  const overview = buildRunTrace([failed], options)
  const full = buildRunTrace([event(1, 'tool', { status: 'started', tool: { name: 'shell', callId: 'call' } }), failed], options)
  const overviewFailure = overview.nodes.find(node => node.status === 'error')
  const fullFailure = full.nodes.find(node => node.status === 'error')
  assert.equal(overviewFailure?.key, fullFailure?.key)
  assert.equal(fullFailure?.sequence, 1)
  assert.equal(fullFailure?.lastSequence, 10)
})

test('a paged reasoning node uses factual model completion from the message snapshot', () => {
  const trace = buildRunTrace([event(5, 'model_reasoning_delta', { taskId: 'child', depth: 1, turn: 2, content: 'Completed reasoning' })], {
    ...options, modelResults: [{ runId: 'run', sequence: 20, taskId: 'child', depth: 1, turn: 2, provider: 'provider', id: 'model', finishReason: 'stop' }],
  })
  assert.equal(trace.nodes[0]?.status, 'success')
})

test('tool and child failures stay with their steps while run failure adds only terminal metadata', () => {
  const trace = buildRunTrace([
    event(1, 'tool', { status: 'failed', tool: { callId: 'independent', name: 'shell' }, content: 'An earlier independent failure' }),
    event(2, 'agent_task', { task: { id: 'delegated', parentId: 'main', title: 'Inspect code', status: 'failed' } }),
    event(3, 'tool', { status: 'failed', tool: { callId: 'delegated', name: 'subagent/delegate' }, content: '{"error":"Child request timed out"}' }),
    event(4, 'agent_task', { task: { id: 'main', title: 'Main', status: 'failed' } }),
    event(5, 'run_finished', { status: 'failed', reason: 'Provider stopped' }),
    event(6, 'tool', { status: 'succeeded', tool: { callId: 'other', name: 'read_file' }, content: 'Unrelated success' }),
  ], { ...options, active: false })
  const failures = trace.nodes.filter(node => node.status === 'error')
  assert.equal(trace.nodes.length, 3)
  assert.equal(failures.length, 2)
  assert.equal(failures[0]?.body, 'An earlier independent failure')
  assert.equal(failures[1]?.error, 'Child request timed out')
  assert.equal(trace.tasks.find(task => task.id === 'delegated')?.status, 'failed')
  assert.equal(trace.finished?.data.status, 'failed')
  assert.equal(trace.nodes.some(node => node.body.includes('Provider stopped')), false)
})

test('nested tool names and main tools use the same friendly operation labels', () => {
  assert.equal(toolOperationLabelKey('subagent/shell'), toolOperationLabelKey('shell'))
  assert.equal(toolOperationLabelKey('subagent/subagent/start_background'), 'traceOpBackground')
  assert.equal(toolOperationLabelKey('read_project_instruction'), 'traceOpReadInstructions')
  assert.equal(toolOperationLabelKey('subagent/await_agent'), 'traceOpAwaitAgent')
  assert.equal(toolOperationLabelKey('cancel_agent'), 'traceOpCancelAgent')
  assert.equal(toolOperationLabelKey('read_tool_output'), 'traceOpReadToolOutput')
  assert.equal(toolOperationLabelKey('unknown-private-tool-id'), 'traceOpTool')
})

test('awaiting and canceling children show their outcomes without exposing task IDs as output', () => {
  const pending = '{"status":"running","pendingTaskIds":["main/a","main/b"]}'
  assert.deepEqual(projectToolOutput(pending, 'await_agent'), { body: '', rawBody: pending, resultSummaryKey: 'traceAgentsRunning', resultSummaryParams: { count: 2 } })
  const canceled = '{"taskId":"main/a","status":"canceled","error":"context canceled"}'
  for (const tool of ['await_agent', 'subagent/cancel_agent']) assert.deepEqual(projectToolOutput(canceled, tool), { body: '', rawBody: canceled, resultSummaryKey: 'traceAgentCanceled' })
  const returned = '{"taskId":"main/a","status":"succeeded","answer":"**Findings**","alreadyCollected":true}'
  assert.deepEqual(projectToolOutput(returned, 'await_agent'), { body: '**Findings**', rawBody: returned, markdown: true })
  assert.equal(projectToolOutput('{"taskId":"main/a","status":"failed","error":"Provider failed"}', 'await_agent').error, 'Provider failed')
  assert.equal(projectToolOutput('{"content":"Saved output","nextOffset":12}', 'read_tool_output').body, 'Saved output')
})

test('collecting failed and canceled child results preserves their outcome even when the tool call succeeded', () => {
  const trace = buildRunTrace([
    event(1, 'agent_task', { taskId: 'main/a', depth: 1, task: { id: 'main/a', parentId: 'main', title: 'Read code', status: 'failed' } }),
    event(2, 'tool', { status: 'succeeded', tool: { callId: 'wait', name: 'await_agent' }, content: '{"taskId":"main/a","status":"failed","error":"Provider failed"}' }),
    event(3, 'agent_task', { taskId: 'main/b', depth: 1, task: { id: 'main/b', parentId: 'main', title: 'Read docs', status: 'canceled' } }),
    event(4, 'tool', { status: 'succeeded', tool: { callId: 'cancel', name: 'cancel_agent' }, content: '{"taskId":"main/b","status":"canceled","error":"context canceled"}' }),
  ], options)
  assert.equal(trace.nodes.find(node => node.callId === 'wait')?.status, 'error')
  assert.equal(trace.nodes.find(node => node.callId === 'cancel')?.status, 'abort')
  assert.equal(trace.nodes.find(node => node.callId === 'wait')?.error, 'Provider failed')
  assert.equal(trace.tasks.find(task => task.id === 'main/a')?.status, 'failed')
  assert.equal(trace.tasks.find(task => task.id === 'main/b')?.status, 'canceled')
  const overview = buildRunTrace([
    event(1, 'agent_task', { taskId: 'main/a', depth: 1, task: { id: 'main/a', parentId: 'main', title: 'Read code', status: 'failed' } }),
    event(2, 'tool', { status: 'succeeded', tool: { callId: 'wait', name: 'await_agent', truncated: true }, content: '{"taskId":"main/a","status":"failed","error":"Provider' }),
  ], options)
  assert.equal(overview.nodes[0]?.status, undefined, 'an incomplete overview must not invent a successful child result')
  assert.equal(overview.tasks.find(task => task.id === 'main/a')?.status, 'failed')
})

test('structured search results and empty instruction discovery are readable without technical details', () => {
  const search = projectToolOutput(JSON.stringify({ matches: [{ path: 'src/app.ts', line: 42, text: 'sendMessage()' }], filesScanned: 12, truncated: true }), 'search')
  assert.deepEqual(search.search, { matches: [{ path: 'src/app.ts', line: 42, text: 'sendMessage()' }], filesScanned: 12, truncated: true })
  assert.deepEqual(projectToolOutput('[]', 'project_instructions').instructions, [])
  assert.deepEqual(projectToolOutput('[{"path":"AGENTS.md","bytes":120}]', 'subagent/project_instructions').instructions, [{ path: 'AGENTS.md', bytes: 120 }])
})

test('empty skills and checklist changes have readable summaries, while errors stay separate from output', () => {
  assert.equal(projectToolOutput('[]', 'list_skills').resultSummaryKey, 'traceNoSkills')
  assert.deepEqual(projectToolOutput('{"items":[{"text":"Inspect","status":"done"}]}', 'update_checklist').resultSummaryParams, { count: 1 })
  assert.deepEqual(projectToolOutput('{"error":"Timed out","preview":"Partial output"}', 'shell'), { body: 'Partial output', error: 'Timed out', rawBody: '{"error":"Timed out","preview":"Partial output"}' })
  const skills = projectToolOutput('[{"name":"Review","description":"Read code","path":"skills/review/SKILL.md"}]', 'list_skills')
  assert.equal(skills.body, 'Review\nRead code\nskills/review/SKILL.md')
  assert.equal(projectToolOutput('{"temperature":22}', 'mcp_call_tool').body, '{\n  "temperature": 22\n}')
  assert.equal(projectToolOutput('{"path":"a.txt","sha256":"digest"}', 'write_file').body, '')
})

test('tool command information survives completion events without duplicating the output body', () => {
  const input = { command: 'git status --short', directory: '/work/project', timeoutSeconds: 60 }
  const tool = { callId: 'shell-input', name: 'shell', summary: '/work/project' }
  const trace = buildRunTrace([event(1, 'tool', { status: 'started', tool: { ...tool, input } }), event(2, 'tool', { status: 'succeeded', tool, content: '{"preview":"clean"}' })], options)
  assert.deepEqual(trace.nodes[0]?.input, input)
  assert.equal(trace.nodes[0]?.body, 'clean')
})

test('background and shell result IDs retain process links and saved output without lifecycle events', () => {
  const trace = buildRunTrace([
    event(1, 'tool', { status: 'succeeded', tool: { callId: 'job', name: 'start_background' }, content: '{"jobId":"background-output"}' }),
    event(2, 'tool', { status: 'succeeded', tool: { callId: 'shell', name: 'shell' }, content: '{"artifactId":"shell-output","preview":"ok"}' }),
  ], options)
  assert.deepEqual(trace.nodes[0]?.jobIds, ['background-output'])
  assert.equal(trace.nodes[0]?.resultSummaryKey, 'traceProcessStarted')
  assert.deepEqual(trace.nodes[1]?.artifactIds, ['shell-output'])
})

test('shell outcome cannot be presented as successful when the command exited nonzero or by signal', () => {
  for (const result of [{ exitCode: 7, complete: true, preview: 'evidence' }, { exitCode: -1, complete: true, signal: 'SIGTERM', preview: 'evidence' }]) {
    const raw = JSON.stringify(result)
    const trace = buildRunTrace([event(1, 'tool', { status: 'succeeded', tool: { callId: 'shell', name: 'shell' }, content: raw })], options)
    assert.equal(trace.nodes[0]?.status, 'error')
    assert.equal(trace.nodes[0]?.body, 'evidence')
    assert.match(trace.nodes[0]?.error || '', result.exitCode === 7 ? /exit code 7/ : /SIGTERM/)
  }
  assert.equal(projectToolOutput('{"jobId":"job","status":"stopping"}', 'stop_background').resultSummaryKey, 'traceProcessStopRequested')
})

test('tool steps retain their model turn association', () => {
  const trace = buildRunTrace([event(3, 'tool', { turn: 1, tool: { callId: 'read', name: 'read_file' }, status: 'succeeded' })], options)
  assert.equal(trace.nodes[0]?.turn, 1)
})

test('reasoning follows tools across turns, excludes empty and auxiliary fragments, and respects reply boundaries', () => {
  const events = [
    event(1, 'model_reasoning_delta', { turn: 1, content: '' }),
    event(2, 'model_reasoning_delta', { turn: 1, content: 'Before ' }),
    event(3, 'model_reasoning_delta', { turn: 1, content: 'tool' }),
    event(4, 'tool', { tool: { name: 'read_file', callId: 'read' }, status: 'succeeded', content: 'File' }),
    event(5, 'model_reasoning_delta', { turn: 2, content: 'After tool' }),
    event(6, 'message', { role: 'assistant', outputTurn: 2 }),
    event(7, 'model_reasoning_delta', { turn: 3, content: 'Followup' }),
    event(8, 'model_reasoning_delta', { turn: 4, content: 'Auxiliary summary', purpose: 'summary' }),
    event(9, 'run_finished', { status: 'canceled' }),
  ]
  const trace = buildRunTrace(events, { ...options, active: false })
  assert.deepEqual(trace.nodes.map(node => [node.sequence, node.body]), [[2, 'Before tool'], [4, 'File'], [5, 'After tool'], [7, 'Followup']])
  assert.ok(trace.nodes.filter(node => node.kind === 'reasoning').every(node => node.status === 'abort'))
  assert.deepEqual(buildRunTrace(events, { ...options, through: 6 }).nodes.map(node => node.sequence), [2, 4, 5])
  assert.deepEqual(buildRunTrace(events, { ...options, after: 6 }).nodes.map(node => node.sequence), [7])
})
