import { readdirSync } from 'node:fs'

const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// Desktop runs every browser scenario. Additional viewports cover actual
// layout, pointer/touch, scrolling and responsive control behavior.
// Mixed contract/state suites opt in per scenario with @responsive rather
// than repeating every keyboard, value-rule and request assertion on phones.
const responsiveFiles = [
  'appearance', 'generation-tools', 'composer-sizing', 'drawer-swipe', 'images', 'layout',
  'main-outputs', 'modal-header', 'modal-motion', 'model-names', 'reduced-motion',
  'session-batch', 'session-bulk', 'session-reminders', 'settings-batch', 'settings-navigation',
  'sidebar-width', 'worker-status',
]
const responsiveTitles: Record<string, string> = {
  'agent-settings': 'full-screen instructions|Appearance and Personalization remain usable|Personalization saves delegation|automatic titles toggle',
  'compaction': 'short referenced summaries|an automatic summary appears|manual compaction preserves chat and draft|interrupted manual compaction',
  'execution-flow': 'execution messages appear|interleaved main and child|long chronological execution|new execution steps|expanded execution history|continuous history prepares|historical full text|earlier history uses one loading indicator|current action, timing|stopping a run|new runs clear|write_file diffs|truncated file and search|usage totals|multi-megabyte process logs|switching monitored processes|token usage uses',
  'iteration': 'configured defaults, pending controls|processing tier follows the selected model',
  'mcp-settings': 'MCP dialogs and long tools stay readable',
  'model-catalog': 'OMP and models.dev updates live|catalog conflicts identify',
  'onboarding': 'connect a provider and create the first session',
  'provider-setup': 'connect and edit use a second dialog|connect Z.ai with only a key',
  'question': 'question choices persist|question tabs switch',
  'resource-storage': 'originals survive compaction',
  'search-settings': 'search settings remain readable',
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
  navigation: ['layer-navigation', 'settings-navigation', 'modal-motion', 'drawer-swipe', 'images', 'follow-up'],
  conversation: ['service-drafts', 'generation-tools', 'background-process', 'compaction', 'composer-sizing', 'conversation-sync', 'detached', 'empty-reply', 'execution-flow', 'failure-analysis', 'follow-up', 'images', 'interjection', 'iteration', 'layout', 'main-outputs', 'message-revert', 'model-diagnostics', 'model-retry', 'provider-output', 'question', 'quoted-reply', 'usage', 'worker-status'],
  sessions: ['profiles', 'branch-workspace', 'management-pagination', 'session-actions', 'agent-session-tools', 'session-batch', 'session-bulk', 'session-close', 'session-flow', 'session-manager', 'session-sidebar', 'session-reminders', 'session-scope', 'state-sync'],
  models: ['generation-tools', 'codex-login', 'model-catalog', 'model-names', 'model-visibility', 'onboarding', 'provider-errors', 'provider-import', 'provider-setup', 'selection-flow', 'selection-stats'],
  personalization: ['agent-settings', 'long-term-instructions', 'extension-import', 'settings-batch'],
  skills: ['skills-settings', 'extension-import', 'settings-batch'],
  mcp: ['mcp-settings', 'extension-import', 'settings-batch'],
  search: ['search-settings'],
  network: ['network-settings', 'provider-setup', 'codex-login', 'search-settings', 'mcp-settings', 'settings-navigation', 'state-sync', 'modal-header'],
  controls: ['controls'],
  about: ['about-advanced', 'capacity-settings', 'environment-check', 'model-catalog', 'reset-settings', 'resource-destination', 'resource-storage', 'user-agent', 'ui-contracts', 'onboarding', 'execution-flow'],
  statistics: ['statistics'],
  operationProgress: ['environment-check', 'resource-storage', 'session-batch', 'settings-batch'],
  contextHeader: ['resource-storage', 'settings-navigation'],
  schedules: ['schedules'],
  sidebar: ['schedules', 'profiles', 'drawer-swipe', 'sidebar-width', 'settings-navigation'],
}
const consumers: [RegExp, string[]][] = [
  [/^web\/src\/composables\/routeLayers\.ts$|^web\/e2e\/controls\/LayerNavigation\.vue$/, ['navigation', 'about', 'models', 'skills', 'network', 'search', 'mcp', 'personalization']],
  [/^web\/src\/composables\/useDrawerSwipe\.ts$/, ['sidebar', 'navigation']],
  [/^web\/src\/(?:composables\/useMessageImageRoute|ui\/messageImageZoom)\.ts$/, ['navigation', 'conversation']],
  [/^internal\/(?:schedule\/|(?:server|store)\/schedules\.go$)|^web\/src\/pages\/SchedulesPage\.vue$/, ['schedules']],
  [/^web\/src\/state\/clientRuntime\.ts$/, ['about']],
  [/^web\/src\/(?:App|ProfileMenu)\.vue$/, ['sidebar']],
  [/^web\/src\/(?:SessionSortMenu\.vue|composables\/useSidebarSessions\.ts)$|^sdk\/src\/sessionPages\.ts$/, ['sessions', 'sidebar']],
  [/^internal\/server\/profiles\.go$|^web\/src\/(?:ProfileMenu\.vue|client\/profileScope\.)/, ['sessions', 'models', 'about']],
  [/^internal\/store\/(?:branches|branch_history|branch_workspace)\.go$|^internal\/server\/revert\.go$|^web\/src\/state\/conversationBranches\.ts$/, ['conversation', 'sessions']],
  [/^internal\/(?:store|server)\/drafts\.go$|^web\/src\/composables\/serviceDrafts\./, ['conversation', 'about', 'sessions']],
  [/^web\/src\/ContextCompaction\.vue$/, ['contextHeader']],
  [/^internal\/(?:generation\/|provider\/output_catalog|modelcatalog\/(?:generation|inventory|custom_model_metadata)|resources\/generated\.go|server\/generation_settings\.go)|^scripts\/onnx-decision\/|^web\/src\/(?:Generation|state\/generationCards)/, ['models', 'conversation']],
  [/^web\/src\/(?:App\.vue$|SessionActionDialog\.vue$)|^internal\/(?:server|store)\/(?:session|conversation)_actions\.go$|^internal\/agent\/tool_(?:session|history)\.go$/, ['sessions', 'conversation']],
  [/^sdk\/src\/(?:conversationSync\.ts|state\/workspace\.ts)$/, ['sessions', 'conversation']],
  [/^sdk\/src\/state\/(?:conversation|events|inputQueue|mainOutputs)\.ts$/, ['conversation']],
  [/^web\/src\/ui\/TabbedModal\.vue$/, ['about']],
  [/^web\/src\/ui\/Tabs\.vue$/, ['controls', 'about', 'models', 'conversation']],
  [/^web\/src\/ui\/Sender\.vue$/, ['conversation', 'models', 'sessions']],
  [/^web\/src\/(?:ui\/ProgressBar|BatchOperationModal)\.vue$/, ['operationProgress']],
  [/^web\/src\/ui\/Collapsible\.vue$/, ['conversation', 'models', 'skills', 'mcp']],
  [/^internal\/(?:network|codexauth|copilotauth|useragent)\/|^internal\/server\/(?:network_config|provider_network)|^web\/src\/(?:Network|ProxySelection|client\/network|composables\/useNetwork)/, ['network', 'models', 'search', 'mcp', 'conversation']],
  [/^web\/src\/(?:pages\/ConversationPage|Conversation|Message|Run|Trace|TimelineItem|Context|PendingInputs|QuestionForm|BackgroundProcesses|WorkerStatus)|^web\/src\/(?:client\/queries|state\/(?:conversationTimeline|runTrace|quotedReply|jobs|failureAnalysis)|composables\/use(?:Conversation|Presentation|FullOutput|RunHistory|ScrollbarDrag))|^internal\/(?:agent|worker|workerproto|provider|modeltrace|tool)\//, ['conversation']],
  [/^web\/src\/schema\/(?:question|QuestionChoiceControl)|^internal\/question\//, ['conversation']],
  [/^web\/src\/composables\/useQuestionComposer/, ['conversation']],
  [/^web\/src\/state\/cacheUsage/, ['conversation']],
  [/^web\/src\/(?:pages\/SessionManager|Session|InlineSessionTitle)|^web\/src\/composables\/useSessions/, ['sessions']],
  [/^internal\/store\/(?:main_outputs|model_output|run_overview)/, ['conversation']],
  [/^internal\/store\/(?:restart|runtime_events|jobs|output_links|checklist)|^internal\/server\/(?:runtime_events|worker_lifecycle|memory_settings|execution)/, ['conversation', 'sessions', 'about']],
  [/^internal\/store\/compaction|^internal\/server\/(?:compaction|worker_results_context)/, ['conversation']],
  [/^internal\/store\/(?:display_messages|message_results|conversation_snapshot|records)|^internal\/server\/(?:events|compaction_messages|message_results)/, ['conversation', 'sessions', 'statistics']],
  [/^internal\/provider\/(?:prompt_cache|cache_|bedrock_cache|codex_routing|request_state|catalog(?:_credentials|_resolve)?\.go$)|^internal\/modelcatalog\/|^internal\/server\/(?:model_settings|custom_models|local_models|model_directory|model_selection|model_credentials|provider_settings|provider_import|processing_tiers|connection_statistics)/, ['models', 'conversation']],
  [/^web\/src\/(?:Provider|Model|DefaultModel|Reasoning|ConversationResponseOptions|ConnectionUsage)|^web\/src\/(?:state\/model|composables\/useProvider)|^internal\/(?:model|models|selection)\//, ['models', 'conversation']],
  [/^web\/src\/(?:AgentSettings|Delegation)|^internal\/(?:agentconfig)\//, ['personalization', 'conversation']],
  [/^web\/src\/(?:Skill)|^internal\/skill/, ['skills', 'conversation']],
  [/^web\/src\/MCP|^internal\/mcp/, ['mcp', 'conversation']],
  [/^web\/src\/(?:Search|WebPreview)|^internal\/web(?:search|fetch)/, ['search', 'conversation']],
  [/^web\/src\/(?:About|Capacity|EnvironmentCheck|ResourceStorage|UserAgent|Reset|Restart)|^internal\/(?:capacity|useragent|buildinfo|server\/environment_check)/, ['about']],
  [/^web\/src\/(?:Statistics|Usage)|^internal\/(?:activity|usage)/, ['statistics']],
]

export function browserScope(paths: readonly string[], available: readonly string[]) {
  const selected = new Set<string>()
  const frontendSourceChanged = paths.some(path => /^(?:web|sdk)\/src\//.test(path) && !/\.(?:test|spec)\.ts$/.test(path))
  for (const path of paths) {
    if (/^(?:docs\/|\.vista\/)|(?:\.md|LICENSE|NOTICE)$/.test(path) || /^licenses\//.test(path)) continue
    if (frontendSourceChanged && /^internal\/server\/assets\//.test(path)) continue
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
