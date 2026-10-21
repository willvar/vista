package workerproto

// ModelObservation contains no prompts, answers, credentials, or raw endpoint.
// A nil FirstTextMS is not a zero-latency success.
type ModelObservation struct {
	RequestID       string   `json:"requestId"`
	ModelID         string   `json:"modelId"`
	ReasoningEffort string   `json:"reasoningEffort"`
	Endpoint        string   `json:"endpoint"`
	InputTokens     int64    `json:"inputTokens"`
	FirstTextMS     *float64 `json:"firstTextMs,omitempty"`
	CacheReadTokens int64    `json:"cacheReadTokens"`
	Outcome         string   `json:"outcome"`
	Purpose         string   `json:"purpose"`
}
