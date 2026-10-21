package workerproto

// MemoryChange carries one explicit preference edit to the Server. Workers
// never write the shared database; the ACK confirms the change was committed.
type MemoryChange struct {
	Action       string `json:"action"`
	ID           string `json:"id,omitempty"`
	Text         string `json:"text,omitempty"`
	PreviousText string `json:"previousText,omitempty"`
}
