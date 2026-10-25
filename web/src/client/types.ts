export type QuestionOption = { label: string; description?: string }
export type QuestionPrompt = { header?: string; question: string; options: QuestionOption[]; multiple?: boolean; custom?: boolean }
export type QuestionAnswer = { options: string[]; text?: string }
export type PendingReply = { runId: string; kind: 'question'; content: string; questions: QuestionPrompt[] }
export type Session = { id: string; title: string; modelKey: string; open: boolean; running: boolean; executing: boolean; createdAt: string; pendingReply?: PendingReply; inputQueuePaused?: boolean; readSequence?: number; latestReplySequence?: number; latestRunStatus?: string; pendingInputCount?: number; selectionMode?: 'manual' | 'auto'; selectionTier?: 'speed' | 'quality'; manualModelKey?: string; manualReasoningEffort?: string; selectionReasoningEffort?: string }
export type SessionListSnapshot = { items: Session[]; throughSequences: Record<string, number> }
export type ModelInfo = { key: string; connectionId?: string; name?: string; modelName?: string; provider: string; providerId?: string; id: string; baseUrl?: string; tools?: boolean; context?: number; inputLimit?: number; outputLimit?: number; catalogReasoning?: boolean; status?: string; inputModalities?: string[]; outputModalities?: string[]; discoverySource?: string; reasoningOptions?: string[]; reasoningEffort?: string }
export type CopilotAuthOptions = { enterpriseUrl?: string; clientId?: string }
export type ModelSetting = {
  headerNames?: string[]; copilotAuth?: CopilotAuthOptions; key: string; connectionId?: string; kind?: string; name?: string; provider: string; providerId?: string; id: string; baseUrl?: string; hasApiKey: boolean; hasAccessToken?: boolean; credentialMode?: string; region?: string; project?: string; location?: string; resourceName?: string; deploymentId?: string; apiVersion?: string; editable: boolean }
export type ModelSettings = { default: string; models: ModelSetting[] }
export type ProviderSetup = { id: string; name: string; aliases?: string; baseUrl?: string; fields?: string[]; credentialModes?: string[]; apiFormats?: string[]; models?: { id: string; name: string }[] }
export type AgentSettings = { instructions: string; instructionsPath: string; delegationDepth: number; maxDelegationDepth: number; automaticTitles: boolean }
export type Memory = { id: string; text: string; createdAt: string; updatedAt: string }
export type Memories = { memories: Memory[]; maxCount: number; maxBytes: number; maxTotalBytes: number }
export type LoginUpdate = { id: string; status: string; error?: string }
export type MCPCheck = { status: string; checkedAt?: string; toolCount: number; error?: string }
export type MCPServer = {
  id: string; name: string; transport: 'stdio' | 'http'; disabled?: boolean
  command?: string; args?: string[]; directory?: string; endpoint?: string
  oauth?: boolean; clientId?: string; hasToken: boolean; hasClientSecret: boolean
  headerNames?: string[]; authorized: boolean; check: MCPCheck
}
export type MCPConfig = { servers: MCPServer[]; maxServers: number; defaultDirectory: string }
export type MCPState = MCPConfig & { logins: LoginUpdate[] }
export type ResourceStorageStats = { totalBytes: number; totalCount: number; referencedBytes: number; referencedCount: number; cleanableBytes: number; cleanableCount: number; scannedAt: string }
export type ResourceStorageStatus = { phase: string; action?: string; stats?: ResourceStorageStats; deletedCount: number; deletedBytes: number; error?: string }
export type ModelCatalog = { default: string; models: ModelInfo[] }
export type OMPCatalogInfo = {
  revision: string
  modelsDevRevision?: string
  modelCount: number
  providerCount: number
  updatedAt?: string
  checkedAt?: string
  nextCheckAt?: string
  checkError?: string
  lastCheckChanged?: boolean
  updating?: boolean
  history?: CatalogCheckRecord[]
}
export type UserAgentSettings = {
  preset: string
  custom: string
  value: string
  presets: { id: string; name: string; value: string }[]
  maxLength: number
}
export type CatalogCheckRecord = Pick<OMPCatalogInfo, 'revision' | 'modelsDevRevision' | 'modelCount' | 'providerCount' | 'updatedAt'> & {
  checkedAt: string
  automatic: boolean
  changed: boolean
  error?: string
}
export type Message = { failure?: boolean; id: string; runId: string; provider?: string; modelId?: string; modelKey?: string; outputTurn?: number; outputBytes?: number; imageCount?: number; sequence: number; resultsAfter?: number; role: string; content: string; createdAt: string; modelResults?: ModelResultView[] }
export type StreamEvent = { sequence: number; kind: string; createdAt?: string; data: any }
export type SyncCheckpoint = { throughSequence: number; revertedSequence?: number; session: Session }
export type ConversationSnapshot = SyncCheckpoint & { messages: Message[]; events: StreamEvent[]; hasOlder: boolean; hasOlderEvents: boolean; eventsBefore?: number }
export type ToolOutputRef = { artifactId?: string; outputPath?: string; runId?: string; turn?: number; bytes: number }
export type AgentTaskView = { id: string; parentId?: string; join?: 'required' | 'best_effort' | 'detached'; title: string; status: string; report?: { runId: string; turn: number; bytes: number } }
export type RunOverview = { modelCalls: number; firstSequence?: number; throughSequence: number; startedAt: string; finishedAt?: string; lastActivityAt?: string; events: StreamEvent[] }
export type ModelDraft = { runId: string; turn: number; depth: number; sequence: number; content: string; finished: boolean }
export type MainOutput = { runId: string; taskId: string; turn: number; sequence: number; lastSequence: number; createdAt: string; provider?: string; modelId?: string; modelKey?: string; bytes: number; complete: boolean }
export type ModelResultView = { sequence: number; runId: string; turn: number; depth: number; createdAt?: string; taskId?: string; parentTaskId?: string; purpose?: string; provider: string; id: string; finishReason: string; reasoningEffort?: string; durationMs?: number; usage?: { inputTokens: number; outputTokens: number; cacheReadTokens: number; reasoningTokens: number } }
export type ContextNotice = { sequence: number; runId?: string; message: string; summary?: string }
export type CompactionStatus = { runId: string; phase: 'compacting' | 'compacted' | 'failed'; manual: boolean; error?: string; sequence?: number }
export type ContextUsage = { tokens: number; sequence: number; runId?: string }
export type ChecklistItem = { text: string; status: 'pending' | 'in_progress' | 'done' | 'blocked' }
export type QueuedInput = { id: string; mode: 'now' | 'later'; status: string; content: string; imageCount?: number }
export type RunProgress = { runId: string; phase: 'starting' | 'model' | 'reasoning' | 'response' | 'tool' | 'processing' | 'finished'; tool?: string }
export type Job = { id: string; runId: string; callId?: string; taskId?: string; parentTaskId?: string; directory: string; command: string; status: string; bytes: number; exitCode: number; complete: boolean; error?: string; createdAt?: string; startedAt?: string; expiresAt?: string; finishedAt?: string; signal?: string; terminationReason?: string }
export type Artifact = { id: string; runId: string; callId?: string; taskId?: string; parentTaskId?: string; status: string; bytes: number; complete: boolean; error?: string }
export type OutputView = { content: string; offset: number; nextOffset: number; availableBytes: number; lastSequence?: number; complete?: boolean }
export type ServerStatus = {
  state: string
  version: string
  binaryHash: string
  sourceCommit: string
  sourceModified: boolean
  hostOS: string
  address: string
  provider: string
  model: string
  defaultModel: string
  defaultModelKey: string
  uptimeSeconds: number
  sessions: { total: number; open: number; closed: number; running: number; idle: number; unavailable: number; workers: number }
  managedBackgroundProcesses: number
  eventStreams: number
  eventStreamLimit: number
  memory: { heapBytes: number; rssBytes: number; peakRssBytes: number }
  workerMetrics: Array<{ sessionId: string; pid: number; memory: { heapBytes: number; rssBytes: number; sampledAt: string } }>
  sampledAt: string
}
export type QuestionReplyInput = { runId: string; answers: QuestionAnswer[] }
export type SendMessageInput = { content?: string; images?: Array<{ mediaType: string; data: string }>; questionReply?: QuestionReplyInput; mode?: 'now' | 'later'; modelKey?: string; reasoningEffort?: string; selectionMode?: 'manual' | 'auto'; selectionTier?: 'speed' | 'quality' }
export type SendMessageResult = { runId?: string; message?: Message; input?: QueuedInput; event: StreamEvent }
export type ModelSpeedStats = { count: number; failures: number; medianMs: number | null; p90Ms: number | null; lowConfidence: boolean }
export type ModelSelectionStats = {
  speedScope: 'global'; qualityScope: 'user'; speedWindowDays: number; qualityWindowSize: number
  models: Array<{
    modelId: string; reasoningEffort: string; providers: string[]
    speed: ModelSpeedStats & { buckets: Array<ModelSpeedStats & { inputBucket: number; endpointMatched: boolean }> }
    quality: { up: number; down: number; count: number; score: number; lowConfidence: boolean }
  }>
}
