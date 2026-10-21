package workerproto

import (
	modelcfg "vista/internal/model"
	"vista/internal/question"
	"vista/internal/resources"
)

// Frames are newline-delimited JSON on a dedicated Worker stdin/stdout pipe.
// Result frames have a byte limit; command fields are validated after decoding.
type Command struct {
	TaskID              string           `json:"taskId,omitempty"`
	ParentTaskID        string           `json:"parentTaskId,omitempty"`
	Join                string           `json:"join,omitempty"`
	Action              string           `json:"action,omitempty"`
	AckID               string           `json:"ackId,omitempty"`
	Input               *Command         `json:"input,omitempty"`
	Error               string           `json:"error,omitempty"`
	JobID               string           `json:"jobId,omitempty"`
	SessionID           string           `json:"sessionId,omitempty"`
	MessageSequence     int64            `json:"messageSequence,omitempty"`
	InputID             string           `json:"inputId,omitempty"`
	RunID               string           `json:"runId"`
	Prompt              string           `json:"prompt"`
	ReplyLanguage       string           `json:"replyLanguage,omitempty"`
	QuestionCallID      string           `json:"questionCallId,omitempty"`
	Images              []Image          `json:"images,omitempty"`
	Model               modelcfg.Model   `json:"model"`
	History             []Message        `json:"history,omitempty"`
	MCPServers          []MCPServer      `json:"mcpServers,omitempty"`
	SearchProviders     []SearchProvider `json:"searchProviders,omitempty"`
	SearchSummaryLength int              `json:"searchSummaryLength,omitempty"`
	Webfetch            WebfetchConfig   `json:"webfetch,omitzero"`
}
type WebfetchConfig struct {
	Order          []string `json:"order"`
	JinaAPIKey     string   `json:"jinaApiKey,omitempty"`
	DeepSeekAPIKey string   `json:"deepseekApiKey,omitempty"`
	PreviewLength  int      `json:"previewLength"`
}
type SearchProvider struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey,omitempty"`
}
type MCPServer struct {
	ID           string   `json:"id"`
	Disabled     bool     `json:"disabled,omitempty"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args,omitempty"`
	Directory    string   `json:"directory,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Token        string   `json:"token,omitempty"`
	OAuth        bool     `json:"oauth,omitempty"`
	ClientID     string   `json:"clientId,omitempty"`
	ClientSecret string   `json:"clientSecret,omitempty"`
}

type Message struct {
	Role    string  `json:"role"`
	Content string  `json:"content"`
	Images  []Image `json:"images,omitempty"`
}

type Image = resources.Ref

type CredentialRequest struct {
	Key      string `json:"key"`
	Ref      string `json:"ref"`
	Deadline int64  `json:"deadline,omitempty"`
}

type Result struct {
	Memory       *MemoryChange      `json:"memory,omitempty"`
	Credential   *CredentialRequest `json:"credential,omitempty"`
	TaskID       string             `json:"taskId,omitempty"`
	ParentTaskID string             `json:"parentTaskId,omitempty"`
	Task         *AgentTask         `json:"task,omitempty"`
	RunID        string             `json:"runId"`
	Turn         int32              `json:"turn,omitempty"`
	Depth        int                `json:"depth,omitempty"`
	FullBytes    int                `json:"fullBytes,omitempty"`
	InputID      string             `json:"inputId,omitempty"`
	AckID        string             `json:"ackId,omitempty"`
	Kind         string             `json:"kind,omitempty"`
	Status       string             `json:"status"`
	Content      string             `json:"content"`
	Tool         *ToolProgress      `json:"tool,omitempty"`
	Job          *JobProgress       `json:"job,omitempty"`
	Artifact     *Artifact          `json:"artifact,omitempty"`
	ModelUse     *ModelUse          `json:"modelUse,omitempty"`
	ModelResult  *ModelResult       `json:"modelResult,omitempty"`
	Checklist    *Checklist         `json:"checklist,omitempty"`
	Pending      *PendingReply      `json:"pendingReply,omitempty"`
	Metrics      *Metrics           `json:"metrics,omitempty"`
	Context      *CompactionStatus  `json:"context,omitempty"`
	ContextUsage *ContextUsage      `json:"contextUsage,omitempty"`
	Checkpoint   *ContextCheckpoint `json:"checkpoint,omitempty"`
	History      *ModelHistory      `json:"history,omitempty"`
	Observation  *ModelObservation  `json:"observation,omitempty"`
	Reply        *CompletedReply    `json:"reply,omitempty"`
}

// CompletedReply is retained when a user input continues the same execution.
// It is committed before that input so the finished answer keeps its place in
// the conversation, without ending the run or duplicating ordinary final replies.
type CompletedReply struct {
	Turn      int32  `json:"turn"`
	FullBytes int    `json:"fullBytes"`
	Content   string `json:"content"`
}
type PendingReply struct {
	CallID    string              `json:"callId"`
	Kind      string              `json:"kind"`
	Content   string              `json:"content"`
	Questions []question.Question `json:"questions,omitempty"`
}
type Checklist struct {
	Items []ChecklistItem `json:"items"`
}
type ChecklistItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}
type ModelUse struct {
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Provider        string `json:"provider"`
	ID              string `json:"id"`
	Key             string `json:"key,omitempty"`
	Purpose         string `json:"purpose,omitempty"`
}
type ModelResult struct {
	Purpose         string      `json:"purpose,omitempty"`
	ReasoningEffort string      `json:"reasoningEffort,omitempty"`
	Provider        string      `json:"provider"`
	ID              string      `json:"id"`
	FinishReason    string      `json:"finishReason"`
	Usage           *ModelUsage `json:"usage,omitempty"`
	DurationMS      float64     `json:"durationMs,omitempty"`
}
type ModelUsage struct {
	InputTokens     int64 `json:"inputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	CacheReadTokens int64 `json:"cacheReadTokens"`
	ReasoningTokens int64 `json:"reasoningTokens"`
}

type Metrics struct {
	HeapBytes         uint64 `json:"heapBytes"`
	HeapObjects       uint64 `json:"heapObjects,omitempty"`
	HeapInuseBytes    uint64 `json:"heapInuseBytes,omitempty"`
	HeapIdleBytes     uint64 `json:"heapIdleBytes,omitempty"`
	HeapReleasedBytes uint64 `json:"heapReleasedBytes,omitempty"`
	StackInuseBytes   uint64 `json:"stackInuseBytes,omitempty"`
	OtherSysBytes     uint64 `json:"otherSysBytes,omitempty"`
	SysBytes          uint64 `json:"sysBytes,omitempty"`
	GCCount           uint32 `json:"gcCount,omitempty"`
	RSSBytes          uint64 `json:"rssBytes"`
	PeakRSSBytes      uint64 `json:"peakRssBytes,omitempty"`
	TotalAllocBytes   uint64 `json:"totalAllocBytes"`
	Goroutines        int    `json:"goroutines"`
	SampledAt         string `json:"sampledAt"`
}
type ToolProgress struct {
	Input            *ToolInput `json:"input,omitempty"`
	OutputRef        *OutputRef `json:"outputRef,omitempty"`
	Truncated        bool       `json:"truncated,omitempty"`
	CallID           string     `json:"callId"`
	Name             string     `json:"name"`
	Summary          string     `json:"summary"`
	ProviderExecuted bool       `json:"providerExecuted,omitempty"`
}

// ToolInput contains only displayable shell metadata, never raw tool arguments.
// Command is redacted before the worker emits it.
type ToolInput struct {
	Command        string `json:"command"`
	Directory      string `json:"directory"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type JobProgress struct {
	StartedAt         string `json:"startedAt,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	Signal            string `json:"signal,omitempty"`
	TerminationReason string `json:"terminationReason,omitempty"`
	CallID            string `json:"callId,omitempty"`
	TaskID            string `json:"taskId,omitempty"`
	ParentTaskID      string `json:"parentTaskId,omitempty"`
	ID                string `json:"id"`
	Directory         string `json:"directory"`
	Command           string `json:"command"`
	OutputPath        string `json:"outputPath"`
	Bytes             int64  `json:"bytes"`
	ExitCode          int    `json:"exitCode"`
	Complete          bool   `json:"complete"`
	Error             string `json:"error,omitempty"`
}
type Artifact struct {
	CallID       string `json:"callId,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
	ParentTaskID string `json:"parentTaskId,omitempty"`
	ID           string `json:"id"`
	OutputPath   string `json:"outputPath"`
	Bytes        int64  `json:"bytes"`
	Complete     bool   `json:"complete"`
	Error        string `json:"error,omitempty"`
}

type OutputRef struct {
	ArtifactID string `json:"artifactId,omitempty"`
	OutputPath string `json:"outputPath,omitempty"`
	RunID      string `json:"runId,omitempty"`
	Turn       int32  `json:"turn,omitempty"`
	Bytes      int64  `json:"bytes"`
}

type AgentTask struct {
	ID       string     `json:"id"`
	ParentID string     `json:"parentId,omitempty"`
	Join     string     `json:"join,omitempty"`
	Title    string     `json:"title"`
	Status   string     `json:"status"`
	Report   *OutputRef `json:"report,omitempty"`
}
