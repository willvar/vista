import type { Session } from '../client/types'

export type SessionSnapshotBoundary = { sessionId: string; eventSequence: number }

export function sessionHasUnread(session: Session) {
  return (session.latestReplySequence || 0) > (session.readSequence || 0)
}

export function sessionNeedsAttention(session: Session, workerAvailable?: boolean) {
  return sessionHasUnread(session) || !!session.pendingReply
    || !!session.inputQueuePaused && (session.pendingInputCount || 0) > 0
    || session.open && workerAvailable === false || session.latestRunStatus === 'failed'
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
