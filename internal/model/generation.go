package model

import "encoding/json"

const (
	ImageOutput    = "image"
	VideoOutput    = "video"
	DecisionOutput = "decision"
)

// GenerationBinding is stored with the existing model connection settings. It
// never contains credentials; Connection names the saved credential source.
type GenerationBinding struct {
	Kind       string          `json:"kind"`
	Connection string          `json:"connection,omitempty"`
	Options    json.RawMessage `json:"options,omitempty"`
}

// GenerationTarget carries a private, per-execution connection snapshot. It is
// sent only over worker IPC, not in catalog responses or conversation events.
type GenerationTarget struct {
	Kind            string          `json:"kind"`
	Options         json.RawMessage `json:"options,omitempty"`
	Model           Model           `json:"model"`
	InputModalities []string        `json:"inputModalities,omitempty"`
	Connection      *Model          `json:"connection,omitempty"`
	Error           string          `json:"error,omitempty"`
}
