import { join } from 'node:path'
import { test as base, expect, nativeFixture } from './fixtures'
import { saveHistoryState } from './history-state'
import { sharedFixture } from './shared-fixture'

type HistorySeed = { sessionId: string; directory: string; modelCalls: number }

export const test = base.extend<{ historySeed: HistorySeed }>({
  historySeed: [async ({}, use) => {
    const seed = await sharedFixture('long-history', async directory => {
      let sessionId = '', modelCalls = 0
      await nativeFixture.use({ progressCalls: 105, longProgress: true }, async (vista, stop) => {
        const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
        sessionId = session.id
        await vista.api(`/sessions/${sessionId}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Many progress calls' }) })
        await expect.poll(async () => (await vista.api('/sessions')).find((item: { id: string }) => item.id === sessionId)?.executing, { timeout: 60_000 }).toBe(false)
        const messages = await vista.api(`/sessions/${sessionId}/messages`)
        expect(messages).toHaveLength(2)
        expect(messages[1].content).toBe('Final progress answer.')
        modelCalls = vista.modelRequests.length
        expect(modelCalls).toBe(106)
        saveHistoryState(await stop(), join(directory, 'state'))
      })
      return { sessionId, directory: join(directory, 'state'), modelCalls }
    })
    await use(seed)
  }, { timeout: 90_000 }],
  vista: async ({ historySeed }, use) => {
    await nativeFixture.use({}, use, historySeed.directory)
  },
})

export { expect }
