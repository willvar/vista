import assert from 'node:assert/strict'
import test from 'node:test'
import { conversationTimeline, recentSentMessage } from './conversationTimeline.ts'
import type { ConversationItem } from '@vista/client'
import { formatQuotedReply, readQuotedReply, sentMessageBody } from './quotedReply.ts'

test('messages and execution steps share event order and stable keys across prepends', () => {
  const message = (sequence: number): ConversationItem => ({ id: `m${sequence}`, displayKey: `m${sequence}`, sequence, displaySequence: sequence, runId: 'run', role: 'assistant', content: 'text', createdAt: '' })
  const traces = { run: [{ key: 'tool', sequence: 3, lastSequence: 5, estimate: 80 }] }
  const before = conversationTimeline([message(2), message(6)], traces, 2)
  const after = conversationTimeline([message(1), message(2), message(6)], traces, 1)
  assert.deepEqual(before.map(row => row.sequence), [2, 3, 6])
  assert.deepEqual(after.slice(1).map(row => row.key), before.map(row => row.key))
  assert.equal(conversationTimeline([message(6)], traces, 6).length, 1)
  const sent = { ...message(2), role: 'user', content: 'The latest request' }
  const rows = [{ ...message(1), role: 'user' }, sent, message(6)]
  assert.equal(recentSentMessage(rows), undefined)
  assert.equal(recentSentMessage(rows, 1), undefined, 'Reading earlier history must not pin the latest request')
  assert.equal(recentSentMessage(rows, 2), undefined, 'A partly visible source does not need a duplicate reference')
  assert.equal(recentSentMessage(rows, 3), sent)
  assert.equal(recentSentMessage([...rows, { ...message(8), role: 'user' }], 6), undefined)
  const image = { ...sent, content: '', imageCount: 2 }
  assert.equal(recentSentMessage([image, message(6)], 6), image)
  assert.equal(recentSentMessage([{ ...sent, content: '' }, message(6)], 6), undefined)
  assert.equal(recentSentMessage([message(6)], 6), undefined)
  const sourceQuote = 'Quote 1 · Quoted reply · #5 · 2026-01-01T12:00:00.000Z\n> Earlier text\n> \n> More quoted text'
  const secondQuote = '引用2 · 引用消息 · #6 · 2026-01-01T12:01:00.000Z\n> 另一段引用'
  const combined = sourceQuote + '\n\n' + secondQuote + '\n\nMy own question.\n\nKeep this paragraph.'
  assert.equal(sentMessageBody(combined), 'My own question.\n\nKeep this paragraph.')
  assert.equal(sentMessageBody(sourceQuote + '\n\n'), '')
  assert.equal(sentMessageBody('> Earlier single quote\n> \n> More context\n\nMy own question.'), 'My own question.')
  const ordinary = 'My heading\r\n> Ordinary Markdown\r\n\r\nMy question'
  assert.equal(sentMessageBody(ordinary), ordinary, 'Do not guess at unrecognized quote headers')
  assert.equal(rows[1]!.content, 'The latest request', 'Reference selection never rewrites canonical messages')
})

test('restored source-less quotes keep their full text while previews show the question', t => {
  const text = 'Older quoted text 中文 🙂\r\n\r\n'.repeat(40)
  const storage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify({ role: 'assistant', text }) } })
  t.after(() => {
    if (storage) Object.defineProperty(globalThis, 'localStorage', storage)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  })
  const restored = readQuotedReply('legacy-session')!
  assert.deepEqual(restored.items, [{ id: 1, role: 'assistant', text }])
  const question = 'My actual question.\n\nKeep this paragraph.'
  const formatted = formatQuotedReply(question, restored, quote => `引用 ${quote.id} · 引用回复`)
  assert.equal(sentMessageBody(formatted), question)
  assert.ok(formatted.startsWith('> 引用 1 · 引用回复\n> Older quoted text 中文 🙂\n> \n'))
  assert.equal(formatted.match(/Older quoted text 中文 🙂/g)?.length, 40)
  assert.ok(formatted.endsWith('\n\n' + question))
  const mixed = { items: [...restored.items, { id: 2, text: 'Current quote', role: 'user' as const,
    source: { key: 'message/current', sequence: 6, createdAt: '2026-01-01T12:01:00.000Z' } }], nextId: 3 }
  assert.equal(sentMessageBody(formatQuotedReply(question, mixed, quote => `Quote ${quote.id}`)), question)
  assert.equal(sentMessageBody(formatQuotedReply('', restored, () => 'Quote 1')), '')
})

test('draft quote numbers follow the remaining order without changing identity or source', () => {
  const source = { key: 'message/source', sequence: 8, createdAt: '2026-01-01T12:00:00.000Z' }
  const reply = { nextId: 10, items: [
    { id: 2, text: 'First remaining', role: 'assistant' as const, source },
    { id: 9, text: 'Second remaining', role: 'user' as const },
  ] }
  const before = JSON.stringify(reply)
  const text = formatQuotedReply('Question', reply, (_quote, number) => `Quote ${number}`)
  assert.equal(text, 'Quote 1 · #8 · 2026-01-01T12:00:00.000Z\n> First remaining\n\n> Quote 2\n> Second remaining\n\nQuestion')
  assert.equal(JSON.stringify(reply), before)
  assert.equal(sentMessageBody(text), 'Question')
  assert.equal(formatQuotedReply('Next', { nextId: 11, items: [reply.items[1]!] }, (_quote, number) => `Quote ${number}`),
    '> Quote 1\n> Second remaining\n\nNext')
  assert.equal(formatQuotedReply('No quotes', { nextId: 11, items: [] }, () => 'unused'), 'No quotes')
})
