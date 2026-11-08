import { readdirSync } from 'node:fs'

const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// Desktop runs every browser scenario. Additional viewports cover actual
// layout, pointer/touch, scrolling and responsive control behavior.
const responsiveFiles = [
  'appearance', 'background-process', 'composer-sizing', 'controls', 'drawer-swipe', 'images', 'layout',
  'main-outputs', 'modal-header', 'modal-motion', 'model-names', 'quoted-reply', 'reduced-motion',
  'session-batch', 'session-bulk', 'session-reminders', 'settings-batch', 'settings-navigation',
  'sidebar-width', 'ui-contracts', 'worker-status',
]
const responsiveTitles: Record<string, string> = {
  'agent-settings': 'full-screen instructions|Appearance and Personalization remain usable',
  'compaction': 'an automatic summary appears|manual compaction preserves chat and draft',
  'execution-flow': 'execution messages appear|interleaved main and child|long chronological execution|crossing the step virtualization|expanded execution history|current action, timing|stopping a run|new runs clear|write_file diffs|truncated file and search|usage totals|multi-megabyte process logs|switching monitored processes|token usage uses',
  'iteration': 'independent selection, voluntary feedback',
  'mcp-settings': 'MCP dialogs and long tools stay readable',
  'memory-settings': 'memory manager keeps long text|memories in Personalization support editing',
  'model-catalog': 'OMP and models.dev updates live|catalog conflicts identify',
  'onboarding': 'connect a provider and create the first session',
  'provider-setup': 'connect and edit use a second dialog|connect Z.ai with only a key',
  'question': 'question choices persist|question tabs switch',
  'resource-storage': 'originals survive compaction',
  'search-settings': 'search settings remain readable',
  'selection-flow': 'reasoning controls align',
  'selection-stats': 'automatic mode exposes scoped learning statistics',
  'session-actions': 'executing sessions show progress|pointer menu dismissal|an open session menu|header titles save|sidebar rename|sidebar deletion',
  'session-manager': 'manager rows share|fixed manager controls',
  'skills-settings': 'file tree preserves separate drafts',
  'statistics': 'token comparisons change reference',
  'usage': 'message actions and usage retain',
}
export const responsiveGrep = new RegExp([
  '@responsive',
  ...responsiveFiles.map(name => `${escape(name + '.spec.ts')} `),
  ...Object.entries(responsiveTitles).map(([name, titles]) => `${escape(name + '.spec.ts')} .*?(?:${titles})`),
].join('|'))

const groups: Record<string, string[]> = {
  conversation: ['background-process', 'compaction', 'composer-sizing', 'conversation-sync', 'detached', 'empty-reply', 'execution-flow', 'failure-analysis', 'follow-up', 'images', 'interjection', 'iteration', 'main-outputs', 'message-revert', 'model-diagnostics', 'model-retry', 'provider-output', 'question', 'quoted-reply', 'usage', 'worker-status'],
  sessions: ['management-pagination', 'session-actions', 'session-batch', 'session-bulk', 'session-close', 'session-flow', 'session-manager', 'session-reminders', 'session-scope', 'state-sync'],
  models: ['codex-login', 'model-catalog', 'model-names', 'model-visibility', 'onboarding', 'provider-errors', 'provider-import', 'provider-setup', 'selection-flow', 'selection-stats'],
  personalization: ['agent-settings', 'memory-settings', 'extension-import', 'settings-batch'],
  skills: ['skills-settings', 'extension-import', 'settings-batch'],
  mcp: ['mcp-settings', 'extension-import', 'settings-batch'],
  search: ['search-settings'],
  about: ['capacity-settings', 'model-catalog', 'reset-settings', 'resource-storage', 'user-agent'],
  statistics: ['statistics'],
}
const consumers: [RegExp, string[]][] = [
  [/^web\/src\/(?:pages\/ConversationPage|Conversation|Message|Run|Context|PendingInputs|QuestionForm|BackgroundProcesses|WorkerStatus)|^web\/src\/(?:state\/(?:conversation|checklist|mainOutputs|runTrace|quotedReply|jobs|failureAnalysis)|composables\/use(?:Conversation|Presentation|RunHistory))|^internal\/(?:agent|worker|workerproto|provider|modeltrace|tool)\//, ['conversation']],
  [/^web\/src\/(?:pages\/SessionManager|Session|InlineSessionTitle)|^web\/src\/composables\/useSessions/, ['sessions']],
  [/^internal\/store\/(?:display_messages|message_results|conversation_snapshot|records)|^internal\/server\/(?:events|compaction_messages|message_results)/, ['conversation', 'sessions', 'statistics']],
  [/^web\/src\/(?:Provider|Model|DefaultModel|Reasoning)|^web\/src\/(?:state\/model|composables\/useProvider)|^internal\/(?:model|models|selection)\//, ['models', 'conversation']],
  [/^web\/src\/(?:AgentSettings|Memory|Delegation)|^internal\/(?:agentconfig|memory)\//, ['personalization', 'conversation']],
  [/^web\/src\/(?:Skill)|^internal\/skill/, ['skills', 'conversation']],
  [/^web\/src\/MCP|^internal\/mcp/, ['mcp', 'conversation']],
  [/^web\/src\/(?:Search|WebPreview)|^internal\/web(?:search|fetch)/, ['search', 'conversation']],
  [/^web\/src\/(?:About|Capacity|ResourceStorage|UserAgent|Reset)|^internal\/(?:capacity|useragent|buildinfo)/, ['about']],
  [/^web\/src\/(?:Statistics|Usage)|^internal\/(?:activity|usage)/, ['statistics']],
]

export function browserScope(paths: readonly string[], available: readonly string[]) {
  const selected = new Set<string>()
  const frontendSourceChanged = paths.some(path => /^web\/src\//.test(path) && !/\.(?:test|spec)\.ts$/.test(path))
  for (const path of paths) {
    if (/^(?:docs\/|\.vista\/)|(?:\.md|LICENSE|NOTICE)$/.test(path) || /^licenses\//.test(path)) continue
    if (frontendSourceChanged && /^internal\/server\/assets\/(?:app\.js|vista-web\.css)$/.test(path)) continue
    if (/^web\/e2e\/[^/]+\.spec\.ts$/.test(path)) { selected.add(path.slice('web/e2e/'.length)); continue }
    if (/\.(?:test|spec)\.ts$|_test\.(?:go|py)$/.test(path)) continue
    const matches = consumers.filter(([pattern]) => pattern.test(path))
    if (!matches.length) return { full: true, files: [...available].sort() }
    for (const [, names] of matches) for (const group of names) for (const name of groups[group]!) selected.add(name + '.spec.ts')
  }
  return { full: false, files: [...selected].filter(file => available.includes(file)).sort() }
}

export function browserSelection(files: readonly string[], full = false) {
  return full ? undefined : ['@smoke', ...files.map(escape)].join('|')
}

export function localBrowserArgumentsValid(args: readonly string[]) {
  return args.every(arg => /^(?:--list|--headed|--(?:max-failures|workers|timeout)=\d+|--reporter=\S+)$/.test(arg))
}

export function browserSpecs(directory: string) {
  return readdirSync(directory).filter(file => file.endsWith('.spec.ts')).sort()
}
