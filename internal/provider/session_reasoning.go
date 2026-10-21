package provider

import (
	"errors"
	"slices"
	"strings"

	modelcfg "vista/internal/model"
)

// ApplyReasoningEffort translates a supported session choice into protocol
// request options. An empty choice preserves the configured model defaults.
func ApplyReasoningEffort(model *modelcfg.Model, effort string) error {
	if effort == "" {
		return nil
	}
	if !slices.Contains(SessionReasoningOptions(*model), effort) {
		return errors.New("selected model does not support this reasoning effort")
	}
	apply := protocolAdapters[model.Provider].applyReasoning
	if apply == nil {
		return errors.New("selected model has no adjustable reasoning effort")
	}
	apply(model, effort)
	return nil
}

// SessionReasoningOptions exposes only controls that the selected transport
// can encode. A model with no controllable effort remains eligible for use
// with its defaults, including automatic selection.
func SessionReasoningOptions(model modelcfg.Model) []string {
	if protocolAdapters[model.Provider].applyReasoning == nil {
		return nil
	}
	if model.Provider == "gemini" || model.Provider == "vertex-gemini" {
		model.ID = strings.TrimPrefix(model.ID, "models/")
	}
	policy := sessionThinking(model)
	switch model.Provider {
	case "anthropic", "vertex-anthropic":
		if policy == nil || !slices.Contains([]string{"budget", "anthropic-adaptive", "anthropic-budget-effort"}, policy.Mode) {
			return nil
		}
	case "gemini", "vertex-gemini":
		if policy == nil || !slices.Contains([]string{"budget", "google-level"}, policy.Mode) {
			return nil
		}
	case "openai-chat":
		if policy != nil && policy.Mode == "budget" {
			return nil
		}
	case "bedrock":
		if policy == nil {
			return nil
		}
		if !strings.Contains(model.ID, "anthropic.") && !strings.Contains(model.ID, "openai.") && !strings.Contains(model.ID, "amazon.nova-2-lite") {
			return nil
		}
	}
	return ModelReasoningOptionsFor(model.ProviderID, model.ID)
}

func sessionThinking(model modelcfg.Model) *OMPThinking {
	if model.Provider == "gemini" || model.Provider == "vertex-gemini" {
		model.ID = strings.TrimPrefix(model.ID, "models/")
	}
	metadata, ok := ompModelForMetadata(model.ProviderID, model.ID)
	if !ok {
		return nil
	}
	return metadata.Thinking
}

func wireEffort(model modelcfg.Model, effort string) string {
	if policy := sessionThinking(model); policy != nil && policy.EffortMap[effort] != "" {
		return policy.EffortMap[effort]
	}
	return effort
}

func thinkingBudget(model modelcfg.Model, effort string) int {
	budget := map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 32768, "max": 32768}[effort]
	if model.OutputLimit > 0 {
		budget = min(budget, max(1024, model.OutputLimit-1024))
	}
	return budget
}

func applyResponsesReasoning(model *modelcfg.Model, effort string) {
	model.ReasoningEffort = wireEffort(*model, effort)
}

func applyChatReasoning(model *modelcfg.Model, effort string) {
	wire := wireEffort(*model, effort)
	if model.ProviderID == "openrouter" {
		options := modelcfg.OpenRouterOptions{}
		if model.OpenRouter != nil {
			options = *model.OpenRouter
		}
		options.Reasoning = &modelcfg.OpenRouterReasoning{Effort: wire}
		model.OpenRouter = &options
		return
	}
	if wire == "adaptive" {
		model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
		return
	}
	model.ReasoningEffort, model.ChatReasoningOverride = wire, true
	if model.ProviderID == "deepseek" || zaiChatProvider(model.ProviderID) {
		model.Thinking = &modelcfg.ThinkingOption{Type: "enabled"}
	}
}

func applyAnthropicReasoning(model *modelcfg.Model, effort string) {
	policy := sessionThinking(*model)
	model.Effort = ""
	if policy.Mode == "anthropic-adaptive" {
		model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
		if wire := wireEffort(*model, effort); wire != "adaptive" {
			model.Effort = wire
		}
		return
	}
	budget := thinkingBudget(*model, effort)
	model.Thinking = &modelcfg.ThinkingOption{Type: "enabled", BudgetTokens: &budget}
	if policy.Mode == "anthropic-budget-effort" {
		// Budget-era Claude effort has three wire levels; its normalized
		// budget tiers are still independently represented by budget_tokens.
		wire := wireEffort(*model, effort)
		switch wire {
		case "minimal":
			wire = "low"
		case "xhigh", "max":
			wire = "high"
		}
		model.Effort = wire
	}
}

func applyGeminiReasoning(model *modelcfg.Model, effort string) {
	model.ThinkingBudget, model.ThinkingLevel = nil, ""
	if sessionThinking(*model).Mode == "budget" {
		budget := thinkingBudget(*model, effort)
		if strings.Contains(model.ID, "flash") {
			budget = min(budget, 24576)
		}
		model.ThinkingBudget = &budget
	} else {
		model.ThinkingLevel = wireEffort(*model, effort)
	}
}

func applyBedrockReasoning(model *modelcfg.Model, effort string) {
	if strings.Contains(model.ID, "anthropic.") {
		applyAnthropicReasoning(model, effort)
		model.ReasoningEffort, model.Effort = model.Effort, ""
	} else {
		applyResponsesReasoning(model, effort)
	}
}
