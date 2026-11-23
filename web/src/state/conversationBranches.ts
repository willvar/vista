import type { ConversationBranch } from '@vista/client'

export type BranchTarget = { id: string; message?: { key: string; sequence: number } }
export type BranchNavigation = { position: number; total: number; previous?: BranchTarget; next?: BranchTarget }

export function branchNavigators(branches: readonly ConversationBranch[], activeId: string | undefined) {
  const replaces = new Map(branches.flatMap(branch => branch.firstMessageId && branch.forkMessageId
    ? [[branch.firstMessageId, branch.forkMessageId] as const] : []))
  const groups = new Map<string, BranchTarget[]>()
  for (const branch of branches) {
    if (!branch.forkMessageId || !branch.forkBefore || !branch.parentId) continue
    // A rewrite of a replacement is another version of that same message.
    let key = branch.forkMessageId
    while (replaces.has(key)) key = replaces.get(key)!
    let versions = groups.get(key)
    if (!versions) {
      versions = []
      groups.set(key, versions)
    }
    if (key === branch.forkMessageId && !versions.some(version => version.message?.key === key)) {
      versions.unshift({ id: branch.parentId, message: { key, sequence: branch.forkBefore } })
    }
    versions.push({ id: branch.id, message: branch.firstMessageId && branch.firstMessageSequence
      ? { key: branch.firstMessageId, sequence: branch.firstMessageSequence } : undefined })
  }

  const messages = new Map<string, BranchNavigation>()
  let draft: BranchNavigation | undefined
  // Closed versions are still part of the message history. Choosing one
  // explicitly can reopen it without starting the rest of the conversation.
  for (const versions of groups.values()) {
    if (versions.length < 2) continue
    versions.forEach((version, index) => {
      const navigation = { position: index + 1, total: versions.length, previous: versions[index - 1], next: versions[index + 1] }
      if (version.message) messages.set(version.message.key, navigation)
      else if (version.id === activeId) draft = navigation
    })
  }
  return { messages, draft }
}
