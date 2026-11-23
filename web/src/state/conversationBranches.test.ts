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
    position: 1, total: 4, previous: undefined, next: { id: 'edit', message: { key: 'edited', sequence: 15 } },
  })
  assert.deepEqual(messages.get('edited'), {
    position: 2, total: 4, previous: { id: 'root', message: { key: 'original', sequence: 12 } },
    next: { id: 'closed', message: undefined },
  })
  assert.deepEqual(draft, {
    position: 4, total: 4, previous: { id: 'closed', message: undefined }, next: undefined,
  })
  assert.equal(messages.get('later')?.total, 2)
  assert.equal(messages.get('later')?.next?.id, 'unrelated')
  assert.equal(branchNavigators(variants, 'root').draft, undefined)
  assert.equal(branchNavigators(variants, 'closed').draft?.position, 3)
})
