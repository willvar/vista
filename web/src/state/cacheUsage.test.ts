import assert from 'node:assert/strict'
import { test } from 'node:test'
import { cacheReadCount, cacheTotals } from './cacheUsage.ts'
import type { ModelResultView } from '@vista/client'

const result = (cacheReadKnown?: boolean, cacheReadTokens = 0, cacheWriteTokens?: number) => ({
  usage: { inputTokens: 100, outputTokens: 2, reasoningTokens: 0, cacheReadKnown, cacheReadTokens, cacheWriteTokens },
}) as ModelResultView

test('cache usage distinguishes unknown, measured zero and older positive counts', () => {
  assert.equal(cacheReadCount(undefined), undefined)
  assert.equal(cacheReadCount(result().usage), undefined)
  assert.equal(cacheReadCount(result(false).usage), undefined)
  assert.equal(cacheReadCount(result(true).usage), 0)
  assert.equal(cacheReadCount(result(undefined, 7).usage), 7)
  assert.deepEqual(cacheTotals([result(true, 7, 2), result(true, 0, 0)]), { read: 7, write: 2 })
  assert.deepEqual(cacheTotals([result(true, 7, 2), result()]), { read: undefined, write: undefined })
  assert.deepEqual(cacheTotals([]), { read: undefined, write: undefined })
})
