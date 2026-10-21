package provider

import modelcfg "vista/internal/model"

// RequestPurpose expresses the caller's task without exposing service-specific
// interaction headers or protocol-specific reasoning options to the caller.
type RequestPurpose string

const (
	PurposeConversation RequestPurpose = "conversation"
	PurposeSubagent     RequestPurpose = "subagent"
	PurposeCompaction   RequestPurpose = "compaction"
	PurposeTitle        RequestPurpose = "title"
)

// ForPurpose returns request options without changing the saved model or its
// nested options. Titles omit explicit reasoning controls; conversation and
// compaction requests preserve the chosen reasoning.
func ForPurpose(model modelcfg.Model, purpose RequestPurpose) modelcfg.Model {
	if purpose == PurposeTitle {
		// A title is a small text request. Do not select a catalog effort or
		// replay the conversation's reasoning controls; adapters use this marker
		// to suppress protocol defaults as well as explicit fields.
		model.ReasoningDisabled = true
		model.SessionID = ""
		model.InteractionType = ""
		model.ReasoningSummary = ""
		model.ReasoningEffort = ""
		model.ChatReasoningOverride = false
		model.Effort = ""
		model.Thinking = nil
		model.EnableThinking = nil
		model.ThinkingBudget = nil
		model.ThinkingLevel = ""
		if model.OpenRouter != nil {
			options := *model.OpenRouter
			options.Plugins = nil
			options.Reasoning = nil
			model.OpenRouter = &options
		}
		model.ReasoningSplit = nil
		model.PreserveThinking = nil
		model.ClearThinking = nil
	}
	if prepare := catalogAdapterFor(model.ProviderID).prepareRequest; prepare != nil {
		prepare(&model, purpose)
	}
	return model
}

func prepareCopilotRequest(model *modelcfg.Model, purpose RequestPurpose) {
	switch purpose {
	case PurposeConversation:
		model.InteractionType = "conversation-agent"
	case PurposeSubagent:
		model.InteractionType = "conversation-subagent"
	case PurposeCompaction:
		model.InteractionType = "conversation-compaction"
	}
}
