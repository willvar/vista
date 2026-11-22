import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { ConversationBranch } from '@vista/client'
import { branchNavigators } from './conversationBranches.ts'

test('the message arrows group successive rewrites, not unrelated forks', () => {
  const root = { rootId: 'root', createdAt: '', open: true }
  const variants: ConversationBranch[] = [
    { ...root, id: 'root' },
    { ...root, id: 'edit', parentId: 'root', forkMessageId: 'original', forkBefore: 12, firstMessageId: 'edited', firstMessageSequence: 15 },
    { ...root, id: 'unrelated', parentId: 'edit', forkMessageId: 'later', forkBefore: 23 },
    { ...root, id: 'closed', parentId: 'root', forkMessageId: 'original', forkBefore: 12, open: false },
    { ...root, id: 'draft', parentId: 'edit', forkMessageId: 'edited', forkBefore: 15 },
  ]
  const { messages, draft } = branchNavigators(variants, 'draft')
  assert.deepEqual(messages.get('original'), {
    position: 1, total: 3, previous: undefined, next: { id: 'edit', message: { key: 'edited', sequence: 15 } },
  })
  assert.deepEqual(messages.get('edited'), {
    position: 2, total: 3, previous: { id: 'root', message: { key: 'original', sequence: 12 } },
    next: { id: 'draft', message: undefined },
  })
  assert.deepEqual(draft, {
    position: 3, total: 3, previous: { id: 'edit', message: { key: 'edited', sequence: 15 } }, next: undefined,
  })
  assert.equal(messages.get('later')?.total, 2)
  assert.equal(messages.get('later')?.next?.id, 'unrelated')
  assert.equal(branchNavigators(variants, 'root').draft, undefined)
})
