import type { ServerResponse } from 'node:http'

export function prepareVistaTestConfig(state: string): void

export function projectRulesFixture(directory: string): {
  (body: object, response: ServerResponse, protocol?: 'chat' | 'gemini'): boolean
  readonly requests: number
}
