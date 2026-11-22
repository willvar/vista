import type { Session, SessionBranchGroup } from '../types.js'

export type SessionSnapshotBoundary = { sessionId: string; eventSequence: number }

export function compareSessionOrder(a: Session, b: Session) {
  return Number(!!b.pinned) - Number(!!a.pinned) || b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id)
}

export function sessionHasUnread(session: Session) {
  return (session.latestReplySequence || 0) > (session.readSequence || 0)
}

export function sessionEntryId(session: Session) {
  return session.branchGroup?.entryId ?? (session.open ? session.id : undefined)
}

export function sessionListActivity(session: Session): SessionBranchGroup {
  return session.branchGroup ?? {
    entryId: sessionEntryId(session),
    unreadId: sessionHasUnread(session) ? session.id : undefined,
    awaitingId: session.pendingReply ? session.id : undefined,
    pausedId: session.inputQueuePaused && (session.pendingInputCount || 0) > 0 ? session.id : undefined,
    failedId: session.latestRunStatus === 'failed' ? session.id : undefined,
    executingId: session.executing ? session.id : undefined,
    runningId: session.running ? session.id : undefined,
  }
}

export function sessionNeedsAttention(session: Session, workerAvailable?: boolean) {
  const activity = sessionListActivity(session)
  return !!(activity.unreadId || activity.awaitingId || activity.pausedId || session.open && workerAvailable === false)
}

export function sessionSnapshotIsCurrent(selected: Session | null, started: SessionSnapshotBoundary | null) {
  return (selected?.id || '') === (started?.sessionId || '')
}

// A snapshot can arrive by HTTP or global SSE after newer conversation events.
// Its saved boundary keeps those events authoritative in the selected view.
export function mergeSessionSnapshot(
  snapshot: Session[], selected: Session | null, started: SessionSnapshotBoundary | null, lastEvent: number,
): Session[] {
  if (!selected || selected.id !== started?.sessionId) return snapshot
  const live = lastEvent > started.eventSequence
  if (!live && (selected.readSequence || 0) <= (snapshot.find(session => session.id === selected.id)?.readSequence || 0)) return snapshot
  return snapshot.map(session => session.id === selected.id ? {
    ...session, readSequence: Math.max(session.readSequence || 0, selected.readSequence || 0),
    ...(live ? {
      title: selected.title, pendingReply: selected.pendingReply, executing: selected.executing, inputQueuePaused: selected.inputQueuePaused,
      latestReplySequence: Math.max(session.latestReplySequence || 0, selected.latestReplySequence || 0),
      latestRunStatus: selected.latestRunStatus, pendingInputCount: selected.pendingInputCount,
    } : {}),
  } : session)
}
