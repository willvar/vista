import { createHash } from 'node:crypto'
import { lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

// Keep local model fixtures independent of live catalog updates. Catalog update
// behavior has its own Go and browser tests with controlled upstream responses.
export function prepareVistaTestConfig(state) {
  const directory = join(state, 'vista')
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  writeFileSync(join(directory, 'model-catalog-status.json'), JSON.stringify({
    nextCheckAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
  }), { mode: 0o600 })
}

// Scripted tool models run inside the checkout, whose real directory rules
// apply to their fixtures too. Read those pages through Vista's actual tool,
// then hide only these bootstrap messages from unrelated fixture assertions.
// No tool result or runtime rule check is bypassed.
export function projectRulesFixture(directory) {
  const pages = []
  for (let ancestor = directory; ; ancestor = dirname(ancestor)) {
    for (const name of ['AGENTS.md', 'CLAUDE.md', '.github/copilot-instructions.md']) {
      const path = join(ancestor, name)
      if (!lstatSync(path, { throwIfNoEntry: false })?.isFile()) continue
      const bytes = readFileSync(path)
      const hash = createHash('sha256').update(bytes).digest('hex')
      for (let offset = 0; ;) {
        pages.push({ path, offset, hash })
        if (offset + 1536 >= bytes.length) break
        let end = offset + 1536
        while ((bytes[end] & 0xc0) === 0x80) end--
        offset = end
      }
    }
    if (dirname(ancestor) === ancestor) break
  }
  let requests = 0
  const handle = (body, response, protocol = 'chat') => {
    const calls = new Map()
    const seen = new Set()
    const key = (path, offset, hash) => JSON.stringify([path, offset, hash])
    if (protocol === 'gemini') {
      for (const message of body.contents || []) for (const part of message.parts || []) {
        if (part.functionCall?.name === 'read_project_instruction') calls.set(part.functionCall.id, part.functionCall.args)
        const result = part.functionResponse
        if (result?.name === 'read_project_instruction' && calls.has(result.id)) {
          const page = JSON.parse(result.response.result)
          seen.add(key(page.path, calls.get(result.id).offset || 0, page.sha256))
        }
      }
    } else {
      for (const message of body.messages || []) {
        for (const call of message.tool_calls || []) if (call.function.name === 'read_project_instruction') calls.set(call.id, JSON.parse(call.function.arguments))
        if (message.role === 'tool' && calls.has(message.tool_call_id)) {
          const page = JSON.parse(message.content)
          seen.add(key(page.path, calls.get(message.tool_call_id).offset || 0, page.sha256))
        }
      }
    }
    const missing = pages.filter(page => !seen.has(key(page.path, page.offset, page.hash)))
    if (missing.length) {
      requests++
      const items = missing.map((page, index) => ({ id: `fixture_rule_${requests}_${index}`, name: 'read_project_instruction', args: { path: page.path, offset: page.offset } }))
      const event = protocol === 'gemini'
        ? { candidates: [{ content: { parts: items.map(item => ({ functionCall: item })) }, finishReason: 'STOP' }] }
        : { choices: [{ delta: { tool_calls: items.map((item, index) => ({ index, id: item.id, type: 'function', function: { name: item.name, arguments: JSON.stringify(item.args) } })) }, finish_reason: 'tool_calls' }] }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' })
      response.end(`data: ${JSON.stringify(event)}\n\n${protocol === 'gemini' ? '' : 'data: [DONE]\n\n'}`)
      return true
    }
    if (protocol === 'gemini') {
      body.contents = body.contents.map(message => ({ ...message, parts: message.parts.filter(part => part.functionCall?.name !== 'read_project_instruction' && part.functionResponse?.name !== 'read_project_instruction') })).filter(message => message.parts.length)
    } else {
      body.messages = body.messages.map(message => ({ ...message, ...(message.tool_calls ? { tool_calls: message.tool_calls.filter(call => call.function.name !== 'read_project_instruction') } : {}) }))
        .filter(message => !(message.role === 'tool' && calls.has(message.tool_call_id)) && !(message.role === 'assistant' && message.tool_calls?.length === 0 && !message.content))
    }
    return false
  }
  Object.defineProperty(handle, 'requests', { get: () => requests })
  return handle
}
