package workerproto

// CompactionStatus describes work, not an inferred model context capacity.
type CompactionStatus struct {
	RunID    string `json:"runId"`
	Phase    string `json:"phase"`
	Manual   bool   `json:"manual"`
	Error    string `json:"error,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type ContextCheckpoint struct {
	Sequence        int64  `json:"-"`
	InputTokens     int64  `json:"inputTokens"`
	Path            string `json:"path"`
	ThroughSequence int64  `json:"throughSequence"`
}
