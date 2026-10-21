package workerproto

// ContextUsage estimates the retained main conversation, never model capacity
// or the accumulated usage of past requests.
type ContextUsage struct {
	RunID    string `json:"runId,omitempty"`
	Tokens   int64  `json:"tokens"`
	Sequence int64  `json:"sequence,omitempty"`
}
