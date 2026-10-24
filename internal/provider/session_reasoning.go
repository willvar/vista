package provider

import (
	"errors"
	"slices"
	"strconv"
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
	model.ReasoningEffort, model.Effort, model.ThinkingLevel = "", "", ""
	model.Thinking, model.EnableThinking, model.ThinkingBudget = nil, nil, nil
	model.ChatReasoningOverride = false
	if model.OpenRouter != nil {
		options := *model.OpenRouter
		options.Reasoning = nil
		model.OpenRouter = &options
	}
	model.ReasoningChoice = effort
	if effort == "default" {
		return nil
	}
	if effort == "off" || effort == "on" {
		applyToggleControl(model, effort == "on")
		return nil
	}
	if value, ok := strings.CutPrefix(effort, "budget:"); ok {
		budget, _ := strconv.Atoi(value) // Membership above validates this preset.
		applyBudgetControl(model, budget)
		return nil
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
	controls := controlsFor(model)
	if policy := controls.fallback; policy != nil {
		switch model.Provider {
		case "anthropic", "vertex-anthropic":
			if !slices.Contains([]string{"budget", "anthropic-adaptive", "anthropic-budget-effort"}, policy.Mode) {
				return nil
			}
		case "gemini", "vertex-gemini":
			if !slices.Contains([]string{"budget", "google-level"}, policy.Mode) {
				return nil
			}
		case "openai-chat":
			if policy.Mode == "budget" {
				return nil
			}
		case "bedrock":
			if !strings.Contains(model.ID, "anthropic.") && !strings.Contains(model.ID, "openai.") && !strings.Contains(model.ID, "amazon.nova-2-lite") {
				return nil
			}
		}
		return controls.efforts
	}
	var choices []string
	if controls.toggle && supportsToggleControl(model, controls) {
		choices = append(choices, "off", "on")
	}
	if controls.budget != nil && supportsBudgetControl(model) {
		choices = append(choices, budgetChoices(model, *controls.budget)...)
	}
	switch model.Provider {
	case "anthropic", "vertex-anthropic":
	case "gemini", "vertex-gemini":
		for _, effort := range controls.efforts {
			if slices.Contains([]string{"minimal", "low", "medium", "high", "default"}, effort) {
				choices = append(choices, effort)
			}
		}
		return choices
	case "bedrock":
		if !strings.Contains(model.ID, "anthropic.") && !strings.Contains(model.ID, "openai.") && !strings.Contains(model.ID, "amazon.nova-2-lite") {
			return nil
		}
	}
	return append(choices, controls.efforts...)
}

// OMP-only models retain their complete published translation policy. These
// mappings are never consulted for a matching models.dev row.
func wireEffort(model modelcfg.Model, effort string) string {
	if policy := controlsFor(model).fallback; policy != nil && policy.EffortMap[effort] != "" {
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
	effort = wireEffort(*model, effort)
	if model.ProviderID == "openrouter" {
		options := modelcfg.OpenRouterOptions{}
		if model.OpenRouter != nil {
			options = *model.OpenRouter
		}
		options.Reasoning = &modelcfg.OpenRouterReasoning{Effort: effort}
		model.OpenRouter = &options
		return
	}
	if effort == "adaptive" {
		model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
		return
	}
	model.ReasoningEffort, model.ChatReasoningOverride = effort, true
	if model.ProviderID == "deepseek" || zaiChatProvider(model.ProviderID) {
		kind := "enabled"
		if effort == "none" {
			kind = "disabled"
		}
		model.Thinking = &modelcfg.ThinkingOption{Type: kind}
	}
}

func applyAnthropicThinking(model *modelcfg.Model) {
	controls := controlsFor(*model)
	version := claudeVersionPattern.FindStringSubmatch(strings.ToLower(model.ID))
	manual := version != nil && version[1] == "4" && version[2] == "5"
	if controls.budget != nil && (len(controls.efforts) == 0 || manual) {
		minimum, maximum := budgetBounds(*model, *controls.budget)
		// A toggle or native effort does not specify a token count. Use Vista's
		// existing manual-thinking default within the published/transport bounds.
		applyBudgetControl(model, min(max(16000, minimum), maximum))
	} else {
		model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
	}
}

func applyAnthropicReasoning(model *modelcfg.Model, effort string) {
	if policy := controlsFor(*model).fallback; policy != nil {
		wire := wireEffort(*model, effort)
		if policy.Mode == "anthropic-adaptive" {
			model.Thinking = &modelcfg.ThinkingOption{Type: "adaptive"}
			if wire != "adaptive" {
				model.Effort = wire
			}
			return
		}
		budget := thinkingBudget(*model, effort)
		model.Thinking = &modelcfg.ThinkingOption{Type: "enabled", BudgetTokens: &budget}
		if policy.Mode == "anthropic-budget-effort" {
			switch wire {
			case "minimal":
				wire = "low"
			case "xhigh", "max":
				wire = "high"
			}
			model.Effort = wire
		}
		return
	}
	if effort == "none" {
		model.Thinking = &modelcfg.ThinkingOption{Type: "disabled"}
	} else {
		applyAnthropicThinking(model)
		model.Effort = effort
	}
}

func applyGeminiReasoning(model *modelcfg.Model, effort string) {
	if policy := controlsFor(*model).fallback; policy != nil && policy.Mode == "budget" {
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
