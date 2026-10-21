import type { Artifact, Job, ToolOutputRef } from '../client/types'

type OutputNode = {
  callId?: string
  artifactIds?: string[]
  jobIds?: string[]
  outputRef?: ToolOutputRef
  error?: string
}

export function toolOutputErrors(node: OutputNode, artifacts: readonly Artifact[], jobs: readonly Job[]): string[] {
  const errors = [...new Set([node.error, ...jobs.map(item => item.error), ...artifacts.map(item => item.error)].filter((error): error is string => !!error?.trim()))]
  // Artifact events shorten long errors. Prefer the full tool error to its duplicate prefix.
  return errors.filter(error => !errors.some(other => other !== error && other.startsWith(error)))
}

export function artifactsForTool(node: OutputNode, runId: string, artifacts: readonly Artifact[]): Artifact[] {
  const ids = new Set(node.artifactIds || [])
  if (node.outputRef?.artifactId) ids.add(node.outputRef.artifactId)
  const matched = artifacts.filter(item => item.runId === runId && (ids.has(item.id) || !!node.callId && item.callId === node.callId))
  // Older outputs may be outside the bounded Session list, but their tool
  // result still contains a durable artifact ID that the output API can read.
  for (const id of ids) if (!matched.some(item => item.id === id)) matched.push({
    id, runId, status: 'succeeded', bytes: node.outputRef?.artifactId === id ? node.outputRef.bytes : 0, complete: true,
  })
  return matched
}

export function jobsForTool(node: OutputNode, runId: string, jobs: readonly Job[]): Job[] {
  const ids = new Set(node.jobIds || [])
  return jobs.filter(item => item.runId === runId && (ids.has(item.id) || !!node.callId && item.callId === node.callId))
}
