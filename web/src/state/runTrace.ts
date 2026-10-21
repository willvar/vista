import type { AgentTaskView, ChecklistItem, ModelResultView, StreamEvent, ToolOutputRef } from '../client/types.ts'
import { inRunTraceRange, type RunTraceRange } from './runTraceRange.ts'

export type TraceStatus = 'loading' | 'success' | 'error' | 'abort'
export type TraceSearchOutput = { matches: { path: string; line?: number; text: string }[]; filesScanned?: number; truncated: boolean }
export type TraceInstructionOutput = { path: string; bytes?: number }[]
export type RunTraceNode = {
  key: string; sequence: number; lastSequence: number; kind: 'tool' | 'reasoning' | 'text' | 'source'
  taskId: string; parentTaskId?: string; title: string; summary?: string; body: string; status?: TraceStatus
  turn?: number; modelId?: string; reasoningEffort?: string; truncated?: boolean
  callId?: string; toolName?: string; outputRef?: ToolOutputRef; artifactIds: string[]; jobIds: string[]; eventData: any
  rawBody?: string; markdown?: boolean
  input?: { command: string; directory: string; timeoutSeconds: number }
  error?: string; resultSummaryKey?: string; resultSummaryParams?: Record<string, string | number>
  search?: TraceSearchOutput; instructions?: TraceInstructionOutput
}
export type RunTraceTask = AgentTaskView & { sequence: number; lastSequence: number; background?: boolean; modelId?: string; modelKey?: string; modelProvider?: string; reasoningEffort?: string; nodes: RunTraceNode[] }
export type TraceAgentHierarchyEntry = { task: RunTraceTask; number: number; depth: number; parentId?: string }

export function buildTraceAgentHierarchy(tasks: readonly RunTraceTask[]): TraceAgentHierarchyEntry[] {
  const byId = new Map(tasks.map(task => [task.id, task]))
  const compare = (a: RunTraceTask, b: RunTraceTask) => a.sequence - b.sequence || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
  const ordered = [...byId.values()].sort(compare)
  // Numbers follow creation order, independently of hierarchy or later activity.
  const numbers = new Map(ordered.filter(task => task.id !== 'main').map((task, index) => [task.id, index + 1]))
  const parents = new Map<string, string>()
  for (const task of ordered) {
    if (task.id !== 'main' && task.parentId && task.parentId !== task.id && byId.has(task.parentId)) {
      parents.set(task.id, task.parentId)
    }
  }
  // Invalid ancestry must not hide agents or invent a delegation relationship.
  const resolved = new Set<string>()
  for (const task of ordered) {
    const path: string[] = []
    const positions = new Map<string, number>()
    let id: string | undefined = task.id
    while (id && !resolved.has(id) && !positions.has(id)) {
      positions.set(id, path.length)
      path.push(id)
      id = parents.get(id)
    }
    if (id && positions.has(id)) for (const cyclicId of path.slice(positions.get(id)!)) parents.delete(cyclicId)
    for (const visited of path) resolved.add(visited)
  }
  const children = new Map<string, RunTraceTask[]>()
  const roots: RunTraceTask[] = []
  for (const task of ordered) {
    const parentId = parents.get(task.id)
    if (!parentId) roots.push(task)
    else {
      const siblings = children.get(parentId) || []
      siblings.push(task)
      children.set(parentId, siblings)
    }
  }
  roots.sort((a, b) => a.id === 'main' ? -1 : b.id === 'main' ? 1 : compare(a, b))
  const entries: TraceAgentHierarchyEntry[] = []
  function visit(task: RunTraceTask, depth: number) {
    entries.push({ task, number: task.id === 'main' ? 0 : numbers.get(task.id)!, depth, parentId: parents.get(task.id) })
    for (const child of children.get(task.id) || []) visit(child, depth + 1)
  }
  for (const root of roots) visit(root, root.id === 'main' ? 0 : 1)
  return entries
}

const toolOperationLabels: Record<string, string> = {
  shell: 'traceOpShell', bash: 'traceOpShell', shell_background: 'traceOpBackground', background: 'traceOpBackground',
  read_file: 'traceOpRead', read: 'traceOpRead', write_file: 'traceOpWrite', write: 'traceOpWrite',
  list_files: 'traceOpList', search_files: 'traceOpSearch', search: 'traceOpSearch', grep: 'traceOpSearch',
  delegate: 'traceOpDelegate', await_agent: 'traceOpAwaitAgent', cancel_agent: 'traceOpCancelAgent',
  update_checklist: 'traceOpChecklist', fetch_url: 'traceOpFetch',
  webfetch: 'traceOpFetch', websearch: 'traceOpWebSearch', start_background: 'traceOpBackground', stop_background: 'traceOpStopBackground',
  project_instructions: 'traceOpFindInstructions', read_project_instruction: 'traceOpReadInstructions',
  list_skills: 'traceOpListSkills', read_skill: 'traceOpReadSkill', history_index: 'traceOpBrowseHistory',
  read_history: 'traceOpReadHistory', read_model_output: 'traceOpReadAnswer', read_tool_output: 'traceOpReadToolOutput',
  list_resources: 'traceOpListResources', read_resource: 'traceOpReadResource',
  mcp_list_tools: 'traceOpListTools', mcp_call_tool: 'traceOpRemoteTool',
  memory: 'traceOpMemory',
}
export function toolOperationLabelKey(name?: string): string { return toolOperationLabels[(name || '').split('/').at(-1) || ''] || 'traceOpTool' }

export function traceStatus(status: string | undefined): TraceStatus | undefined {
  if (status === 'started' || status === 'running') return 'loading'
  if (status === 'succeeded' || status === 'completed') return 'success'
  if (status === 'failed') return 'error'
  if (status === 'canceled' || status === 'cancelled' || status === 'interrupted') return 'abort'
}
export function traceTaskId(event: StreamEvent): string {
  return event.data?.taskId || 'main'
}
export function traceEventTime(event: StreamEvent): string | undefined { return event.createdAt || event.data?.createdAt }
export function compactTraceText(text = '', limit = 110): string {
  const line = text.replace(/\s+/g, ' ').trim()
  return line.length > limit ? line.slice(0, limit - 1) + '…' : line
}
export function projectToolOutput(raw: string, toolName?: string): Pick<RunTraceNode, 'body' | 'rawBody' | 'markdown' | 'search' | 'instructions' | 'error' | 'resultSummaryKey' | 'resultSummaryParams'> {
  try {
    const value: unknown = JSON.parse(raw)
    if (Array.isArray(value) && toolOperationLabelKey(toolName) === 'traceOpFindInstructions') {
      return { body: '', rawBody: raw, instructions: value.filter(item => item && typeof item.path === 'string').map(item => ({ path: item.path, bytes: typeof item.bytes === 'number' ? item.bytes : undefined })) }
    }
    if (Array.isArray(value) && toolOperationLabelKey(toolName) === 'traceOpListSkills') {
      const skills = value.filter(item => item && typeof item.name === 'string' && typeof item.path === 'string')
      return { body: skills.map(item => [item.name, item.description, item.path].filter(Boolean).join('\n')).join('\n\n'), rawBody: raw, resultSummaryKey: skills.length ? 'traceSkillsFound' : 'traceNoSkills', resultSummaryParams: { count: skills.length } }
    }
    if (toolName && Array.isArray(value)) return value.length ? { body: JSON.stringify(value, null, 2), rawBody: raw } : { body: '', rawBody: raw, resultSummaryKey: 'traceNoResults' }
    if (toolName && typeof value === 'string') return { body: value, rawBody: raw }
    if (!value || typeof value !== 'object' || Array.isArray(value)) return { body: '', rawBody: raw }
    const fields = value as Record<string, unknown>
    const error = typeof fields.error === 'string' ? fields.error : fields.error && typeof fields.error === 'object' && typeof (fields.error as any).message === 'string' ? (fields.error as any).message : ''
    const preview = typeof fields.preview === 'string' ? fields.preview : ''
    const operation = toolOperationLabelKey(toolName)
    if (['traceOpAwaitAgent', 'traceOpCancelAgent'].includes(operation) && fields.status === 'canceled' && typeof fields.taskId === 'string') return { body: '', rawBody: raw, resultSummaryKey: 'traceAgentCanceled' }
    if (error) return { body: preview, error, rawBody: raw }
    if (operation === 'traceOpMemory' && Array.isArray(fields.memories)) {
      const memories = fields.memories.filter(item => item && typeof item.text === 'string')
      return { body: memories.map(item => item.text).join('\n\n'), rawBody: raw, resultSummaryKey: memories.length ? undefined : 'memoriesEmpty' }
    }
    if (operation === 'traceOpMemory' && typeof fields.text === 'string') {
      const resultSummaryKey = ({ add: 'memoryAdded', update: 'memoryUpdated', remove: 'memoryRemoved' } as Record<string, string>)[String(fields.action)]
      return { body: fields.text, rawBody: raw, resultSummaryKey }
    }
    if (operation === 'traceOpShell' && typeof fields.signal === 'string' && fields.signal) return { body: preview, error: `terminated by signal: ${fields.signal}`, rawBody: raw }
    if (operation === 'traceOpShell' && fields.complete === true && typeof fields.exitCode === 'number' && fields.exitCode !== 0) return { body: preview, error: `exit code ${fields.exitCode}`, rawBody: raw }
    if (operation === 'traceOpBackground' && typeof fields.jobId === 'string') return { body: '', rawBody: raw, resultSummaryKey: 'traceProcessStarted' }
    if (operation === 'traceOpStopBackground' && fields.status === 'stopping') return { body: '', rawBody: raw, resultSummaryKey: 'traceProcessStopRequested' }
    if (operation === 'traceOpDelegate' && typeof fields.taskId === 'string' && fields.status === 'running') return { body: '', rawBody: raw, resultSummaryKey: 'traceAgentStarted' }
    if (operation === 'traceOpAwaitAgent' && fields.status === 'running' && Array.isArray(fields.pendingTaskIds)) return { body: '', rawBody: raw, resultSummaryKey: 'traceAgentsRunning', resultSummaryParams: { count: fields.pendingTaskIds.length } }
    if (Array.isArray(fields.items) && toolOperationLabelKey(toolName) === 'traceOpChecklist') return { body: '', rawBody: raw, resultSummaryKey: 'traceChecklistUpdated', resultSummaryParams: { count: fields.items.length } }
    if (Array.isArray(fields.matches)) {
      const matches = fields.matches.filter(item => item && typeof item.path === 'string' && typeof item.text === 'string').map(item => ({ path: item.path, line: typeof item.line === 'number' ? item.line : undefined, text: item.text }))
      return { body: '', rawBody: raw, search: { matches, filesScanned: typeof fields.filesScanned === 'number' ? fields.filesScanned : undefined, truncated: fields.truncated === true } }
    }
    if (typeof fields.answer === 'string') return { body: fields.answer, rawBody: raw, markdown: true }
    for (const name of ['preview', 'content', 'text', 'output']) if (typeof fields[name] === 'string') return { body: fields[name], rawBody: raw }
    // Preserve structured tool data when it has no dedicated presentation;
    // lifecycle metadata stays in the run's diagnostics.
    const metadata = new Set(['id', 'artifactId', 'jobId', 'outputPath', 'runId', 'turn', 'bytes', 'directory', 'status', 'exitCode', 'signal', 'complete', 'truncated', 'path', 'sha256', 'nextOffset'])
    if (toolName && Object.keys(fields).some(key => !metadata.has(key))) return { body: JSON.stringify(value, null, 2), rawBody: raw }
    return { body: '', rawBody: raw }
  } catch {
    // A compact overview may contain only the start of a JSON envelope. Keep its metadata in details too.
    return /^(?:\{|\[\s*(?:\{|"|\d|true|false|null|\]))/.test(raw.trimStart()) ? { body: '', rawBody: raw } : { body: raw }
  }
}

export function buildRunTrace(events: readonly StreamEvent[], options: RunTraceRange & { runId: string; active: boolean; replyTurns?: readonly number[]; modelResults?: readonly ModelResultView[] }) {
  const tasks = new Map<string, RunTraceTask>()
  const nodes = new Map<string, RunTraceNode>()
  const toolCalls = new Map<string, string>()
  const completed = new Set<string>()
  const models = new Map<string, { id?: string; reasoningEffort?: string }>()
  const outputLinks = new Map<string, { artifacts: Set<string>; jobs: Set<string> }>()
  let checklist: ChecklistItem[] | undefined
  let finished: StreamEvent | undefined
  for (const result of options.modelResults || []) {
    if (result.runId !== options.runId || !inRunTraceRange(result.sequence, options)) continue
    const id = result.taskId || 'main'
    const turn = `${id}/${result.turn}`
    completed.add(turn)
    models.set(turn, { id: result.id, reasoningEffort: result.reasoningEffort })
  }
  function task(id: string, event: StreamEvent): RunTraceTask {
    let value = tasks.get(id)
    if (!value) {
      value = { id, parentId: event.data?.parentTaskId, title: '', status: '', sequence: event.sequence, lastSequence: event.sequence, nodes: [] }
      tasks.set(id, value)
    }
    return value
  }
  const ordered = [...events].filter(event => event.data?.runId === options.runId && inRunTraceRange(event.sequence, options)).sort((a, b) => a.sequence - b.sequence)
  for (const event of ordered) {
    const data = event.data || {}
    const id = traceTaskId(event)
    const turn = `${id}/${data.turn}`
    if (event.kind === 'model_result') {
      completed.add(turn)
      models.set(turn, { id: data.modelResult?.id, reasoningEffort: data.modelResult?.reasoningEffort })
    }
    if ((event.kind === 'artifact' || event.kind === 'job') && data.callId && data.id) {
      const key = `${id}/${data.callId}`
      const links = outputLinks.get(key) || { artifacts: new Set<string>(), jobs: new Set<string>() }
      ;(event.kind === 'artifact' ? links.artifacts : links.jobs).add(data.id)
      outputLinks.set(key, links)
    }
  }
  for (const event of ordered) {
    const data = event.data || {}
    const id = traceTaskId(event)
    const turn = `${id}/${data.turn}`
    if (event.kind === 'agent_task' && data.task?.id) {
      const current = task(data.task.id, event)
      Object.assign(current, data.task)
      current.lastSequence = event.sequence
    } else if (event.kind === 'checklist' && !data.depth && (id === 'main') && Array.isArray(data.items || data.checklist?.items)) {
      checklist = (data.items || data.checklist.items).slice(0, 16)
    } else if (event.kind === 'model_request' && data.purpose !== 'summary') {
      const current = task(id, event)
      current.modelId = data.id || current.modelId
      current.modelKey = data.key || current.modelKey
      current.modelProvider = data.provider || current.modelProvider
      current.reasoningEffort = data.reasoningEffort
      models.set(turn, { id: data.id, reasoningEffort: data.reasoningEffort })
    } else if (event.kind === 'tool' && data.tool) {
      const call = `${id}/${data.tool.callId || event.sequence}`
      let key = toolCalls.get(call)
      // An explicit new start is a new attempt; preserve a previous failed attempt.
      if (!key) { key = `tool/${call}`; toolCalls.set(call, key) }
      else if (data.status === 'started' && nodes.get(key)?.status !== 'loading') { key = `tool/${call}/retry/${event.sequence}`; toolCalls.set(call, key) }
      const previous = nodes.get(key)
      const links = outputLinks.get(call)
      const rawBody = data.content ?? previous?.rawBody ?? previous?.body ?? ''
      const artifactIds = new Set([...(previous?.artifactIds || []), ...(links?.artifacts || [])])
      const jobIds = new Set([...(previous?.jobIds || []), ...(links?.jobs || [])])
      let status = traceStatus(data.status)
      try {
        const result = JSON.parse(rawBody)
        if (typeof result?.artifactId === 'string') artifactIds.add(result.artifactId)
        if (typeof result?.jobId === 'string') jobIds.add(result.jobId)
        if (toolOperationLabelKey(data.tool.name) === 'traceOpShell' && status === 'success' && (result?.signal || result?.complete === true && typeof result?.exitCode === 'number' && result.exitCode !== 0)) status = 'error'
        if (['traceOpAwaitAgent', 'traceOpCancelAgent'].includes(toolOperationLabelKey(data.tool.name)) && typeof result?.taskId === 'string') {
          if (status === 'success' && ['failed', 'canceled'].includes(result.status)) status = traceStatus(result.status)
        }
      } catch {
        // A compact overview may omit the outcome. Wait for the full result
        // before presenting a successfully collected child result as successful.
        if (data.tool.truncated && status === 'success' && ['traceOpAwaitAgent', 'traceOpCancelAgent'].includes(toolOperationLabelKey(data.tool.name))) status = undefined
      }
      const node: RunTraceNode = {
        key, sequence: previous?.sequence ?? event.sequence, lastSequence: event.sequence, kind: 'tool', taskId: id, parentTaskId: data.parentTaskId,
        title: data.tool.name, toolName: data.tool.name, summary: data.tool.summary, turn: data.turn ?? previous?.turn, ...projectToolOutput(rawBody, data.tool.name),
        callId: data.tool.callId, input: data.tool.input ?? previous?.input, outputRef: data.tool.outputRef, truncated: data.tool.truncated,
        artifactIds: [...artifactIds], jobIds: [...jobIds],
        status, eventData: data,
      }
      nodes.set(key, node)
      task(id, event)
    } else if (['model_delta', 'model_reasoning_delta'].includes(event.kind) && !data.purpose && data.turn > 0 && typeof data.content === 'string' && data.content) {
      const reasoning = event.kind === 'model_reasoning_delta'
      // Main answer text stays in the conversation. Reasoning and child output
      // follow the original event order within their execution process.
      if (id === 'main' && !reasoning) continue
      const key = `${reasoning ? 'reasoning' : 'text'}/${turn}`
      const previous = nodes.get(key)
      const body = (previous?.body || '') + data.content
      const node: RunTraceNode = {
        key, sequence: previous?.sequence ?? event.sequence, lastSequence: event.sequence, kind: reasoning ? 'reasoning' : 'text', title: '',
        taskId: id, parentTaskId: data.parentTaskId, turn: data.turn, body,
        truncated: previous?.truncated, status: completed.has(turn) ? 'success' : 'loading',
        modelId: models.get(turn)?.id, reasoningEffort: models.get(turn)?.reasoningEffort,
        artifactIds: [], jobIds: [], eventData: data,
      }
      nodes.set(key, node)
      task(id, event)
    } else if (event.kind === 'source' && /^https?:\/\//.test(data.content || '')) {
      const key = `source/${event.sequence}`
      nodes.set(key, { key, sequence: event.sequence, lastSequence: event.sequence, kind: 'source', taskId: id, title: '', body: data.content, status: 'success', artifactIds: [], jobIds: [], eventData: data })
      task(id, event)
    } else if (event.kind === 'run_finished') {
      finished = event
    }
  }
  const allNodes = [...nodes.values()].sort((a, b) => a.sequence - b.sequence)
  for (const task of tasks.values()) {
    const visited = new Set<string>()
    for (let ancestor: RunTraceTask | undefined = task; ancestor && !visited.has(ancestor.id); ancestor = tasks.get(ancestor.parentId || '')) {
      visited.add(ancestor.id)
      if (ancestor.join === 'detached') { task.background = true; break }
    }
  }
  for (const node of allNodes) {
    const owner = tasks.get(node.taskId)!
    if (node.status === 'loading' && ((!owner.background && (finished || !options.active)) || ['succeeded', 'failed', 'canceled', 'interrupted'].includes(owner.status))) {
      node.status = owner.status === 'canceled' || finished?.data.status === 'canceled' ? 'abort' : undefined
    }
    owner.nodes.push(node)
  }
  return {
    tasks: [...tasks.values()].sort((a, b) => a.id === 'main' ? -1 : b.id === 'main' ? 1 : a.sequence - b.sequence),
    nodes: allNodes, checklist, finished,
  }
}
