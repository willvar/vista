package workerproto

// TokenUsage carries only a per-attempt identity and reported token total.
// It is independent of conversation history, which can be withdrawn or deleted.
type TokenUsage struct {
	ID     string `json:"id"`
	Tokens *int64 `json:"tokens"`
}
