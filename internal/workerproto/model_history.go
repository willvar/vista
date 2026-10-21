package workerproto

// ModelHistory points to one immutable model message, separate from UI traces
// and formal replies. Index identifies a tool result within its model turn.
type ModelHistory struct {
	Path   string `json:"path"`
	Index  int    `json:"index,omitempty"`
	Tokens int64  `json:"tokens"`
}
