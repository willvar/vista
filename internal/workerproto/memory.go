package workerproto

// MemoryChange carries one preference edit or an atomic group of updates and
// removals to the Server. Workers never write the shared database; the ACK
// confirms the entire change was committed.
type MemoryChange struct {
	Changes      []MemoryEdit `json:"changes,omitempty"`
	Action       string       `json:"action"`
	ID           string       `json:"id,omitempty"`
	Text         string       `json:"text,omitempty"`
	PreviousText string       `json:"previousText,omitempty"`
}

// MemoryEdit identifies an existing preference and its expected text. An update
// with Changes must not also supply the single-edit fields above.
type MemoryEdit struct {
	Action       string `json:"action"`
	ID           string `json:"id"`
	Text         string `json:"text,omitempty"`
	PreviousText string `json:"previousText"`
}
