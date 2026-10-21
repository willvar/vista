import assert from 'node:assert/strict'
import test from 'node:test'
import { createConversationState, reduceConversationEvent } from './conversation.ts'
import { mergeSessionSnapshot, sessionHasUnread, sessionNeedsAttention, sessionSnapshotIsCurrent } from './workspace.ts'
import type { Session } from '../client/types'

const session: Session = { id: 'a', title: 'New session', modelKey: '', open: true, running: true, executing: false, createdAt: '' }

test('all five reminders are independent and an empty paused queue needs no action', () => {
  assert.equal(sessionNeedsAttention(session), false)
  assert.equal(sessionNeedsAttention({ ...session, latestReplySequence: 10, readSequence: 9 }), true)
  assert.equal(sessionNeedsAttention({ ...session, pendingReply: { runId: 'r', kind: 'question', content: '', questions: [] } }), true)
  assert.equal(sessionNeedsAttention({ ...session, inputQueuePaused: true, pendingInputCount: 1 }), true)
  assert.equal(sessionNeedsAttention({ ...session, inputQueuePaused: true, pendingInputCount: 0 }), false)
  assert.equal(sessionNeedsAttention(session, false), true)
  assert.equal(sessionNeedsAttention({ ...session, open: false }, false), false)
  assert.equal(sessionNeedsAttention({ ...session, latestRunStatus: 'failed' }, true), true)
  assert.equal(sessionNeedsAttention({ ...session, latestRunStatus: 'running' }, true), false)
  assert.equal(sessionHasUnread({ ...session, latestReplySequence: 10, readSequence: 10 }), false)
})

test('a slow poll cannot undo a read receipt or a streamed failure and paused queue', () => {
  const live = { ...session, readSequence: 10, latestReplySequence: 11, latestRunStatus: 'failed', inputQueuePaused: true, pendingInputCount: 2 }
  const snapshot = [{ ...session, readSequence: 0, latestReplySequence: 10, latestRunStatus: 'running', pendingInputCount: 0 }]
  assert.equal(mergeSessionSnapshot(snapshot, live, { sessionId: 'a', eventSequence: 1 }, 1)[0].readSequence, 10)
  const merged = mergeSessionSnapshot(snapshot, live, { sessionId: 'a', eventSequence: 1 }, 2)[0]
  assert.equal(merged.latestReplySequence, 11)
  assert.equal(merged.latestRunStatus, 'failed')
  assert.equal(merged.pendingInputCount, 2)
  assert.equal(merged.inputQueuePaused, true)
})

test('a stale poll cannot undo a streamed title and newly started execution', () => {
  let state = createConversationState({ ...session, pendingReply: { runId: 'previous', kind: 'question', content: 'Choose a path', questions: [{ question: 'Choose a path', options: [{ label: 'src' }, { label: 'web' }] }] } })
  const boundary = { sessionId: session.id, eventSequence: state.lastEvent }
  const staleSnapshot = [state.selected!]
  state = reduceConversationEvent(state, { sequence: 1, kind: 'message', data: { id: 'user', runId: 'new-run', sequence: 1, role: 'user', content: 'Hi', createdAt: '' } })
  state = reduceConversationEvent(state, { sequence: 2, kind: 'session_title', data: { title: 'A useful title' } })
  const merged = mergeSessionSnapshot(staleSnapshot, state.selected, boundary, state.lastEvent)
  assert.equal(merged[0].title, 'A useful title')
  assert.equal(merged[0].executing, true)
  assert.equal(merged[0].pendingReply, undefined)
  assert.equal(staleSnapshot[0].title, 'New session')
})

test('server worker and open state remain authoritative when live metadata is protected', () => {
  const live = { ...session, title: 'Streamed title', executing: true }
  const merged = mergeSessionSnapshot([{ ...session, open: false, running: false }], live, { sessionId: 'a', eventSequence: 1 }, 2)
  assert.equal(merged[0].title, live.title)
  assert.equal(merged[0].open, false)
  assert.equal(merged[0].running, false)
})

test('a poll without intervening events or with a changed selection uses the server snapshot', () => {
  const snapshot = [{ ...session, title: 'Server title' }]
  assert.equal(mergeSessionSnapshot(snapshot, session, { sessionId: 'a', eventSequence: 2 }, 2), snapshot)
  assert.equal(mergeSessionSnapshot(snapshot, { ...session, id: 'b' }, { sessionId: 'a', eventSequence: 2 }, 3), snapshot)
})

test('a poll started on home cannot apply an empty snapshot after the first session is created', () => {
  const started = null
  assert.equal(sessionSnapshotIsCurrent(null, started), true)
  const created = { ...session, id: 'first', executing: true }
  assert.equal(sessionSnapshotIsCurrent(created, started), false)
  // The refresh after first send can safely apply the updated server state.
  assert.equal(sessionSnapshotIsCurrent(created, { sessionId: 'first', eventSequence: 1 }), true)
})

test('changing sessions or returning home rejects the prior refresh while new SSE events preserve its boundary', () => {
  const started = { sessionId: 'a', eventSequence: 1 }
  assert.equal(sessionSnapshotIsCurrent({ ...session, id: 'b' }, started), false)
  assert.equal(sessionSnapshotIsCurrent(null, started), false)
  assert.equal(sessionSnapshotIsCurrent({ ...session, executing: true, title: 'Streamed' }, started), true)
})
