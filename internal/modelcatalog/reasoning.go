package modelcatalog

import (
	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

// ApplyReasoningEffort applies a session preference through the model protocol.
func ApplyReasoningEffort(model *modelcfg.Model, effort string) error {
	return provider.ApplyReasoningEffort(model, effort)
}
