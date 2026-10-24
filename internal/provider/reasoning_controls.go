package provider

import (
	"cmp"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	modelcfg "vista/internal/model"
)

func cloneReasoningOptions(options []ReasoningOption) []ReasoningOption {
	if options == nil {
		return nil
	}
	cloned := append([]ReasoningOption(nil), options...)
	for i := range cloned {
		cloned[i].Values = slices.Clone(cloned[i].Values)
		if cloned[i].Min != nil {
			value := *cloned[i].Min
			cloned[i].Min = &value
		}
		if cloned[i].Max != nil {
			value := *cloned[i].Max
			cloned[i].Max = &value
		}
	}
	return cloned
}

func validateReasoningOptions(options []ReasoningOption) error {
	seen := map[string]bool{}
	for _, option := range options {
		if seen[option.Type] {
			return errors.New("duplicate reasoning control")
		}
		seen[option.Type] = true
		switch option.Type {
		case "effort":
			if len(option.Values) == 0 || len(option.Values) > 16 {
				return errors.New("invalid reasoning effort list")
			}
			for _, value := range option.Values {
				if !validEffortValue(value) && value != "none" && value != "default" {
					return errors.New("invalid reasoning effort value")
				}
			}
		case "budget_tokens":
			if option.Min != nil && *option.Min < -1 || option.Max != nil && *option.Max < 0 || option.Min != nil && option.Max != nil && *option.Min > *option.Max {
				return errors.New("invalid reasoning budget range")
			}
		case "toggle":
		default:
			return errors.New("unknown reasoning control")
		}
	}
	return nil
}

type reasoningControls struct {
	efforts  []string
	budget   *ReasoningOption
	toggle   bool
	fallback *OMPThinking
}

func controlsFor(model modelcfg.Model) reasoningControls {
	metadata, ok := ompModelForMetadata(model.ProviderID, strings.TrimPrefix(model.ID, "models/"))
	var controls reasoningControls
	if !ok || !metadata.Reasoning {
		return controls
	}
	// The merge retains OMP's Thinking policy only for OMP-only rows. A
	// models.dev row replaces the whole policy, even when it has no controls;
	// its legacy saved effort list is recovered before reaching this lookup.
	if len(metadata.ReasoningOptions) == 0 && metadata.Thinking != nil {
		controls.efforts = slices.Clone(metadata.Thinking.Efforts)
		controls.fallback = metadata.Thinking
		return controls
	}
	for _, option := range metadata.ReasoningOptions {
		switch option.Type {
		case "effort":
			for _, value := range option.Values {
				if !slices.Contains(controls.efforts, value) {
					controls.efforts = append(controls.efforts, value)
				}
			}
		case "budget_tokens":
			copy := option
			controls.budget = &copy
		case "toggle":
			controls.toggle = true
		}
	}
	order := []string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
	slices.SortFunc(controls.efforts, func(a, b string) int {
		return cmp.Compare(slices.Index(order, a), slices.Index(order, b))
	})
	return controls
}

func supportsBudgetControl(model modelcfg.Model) bool {
	switch model.Provider {
	case "anthropic", "vertex-anthropic", "gemini", "vertex-gemini":
		return true
	case "bedrock":
		return strings.Contains(model.ID, "anthropic.")
	case "openai-chat":
		return model.ProviderID == "openrouter" || alibabaProvider(model.ProviderID)
	}
	return false
}

func supportsToggleControl(model modelcfg.Model, controls reasoningControls) bool {
	switch model.Provider {
	case "anthropic", "vertex-anthropic":
		return true
	case "bedrock":
		return supportsBudgetControl(model)
	case "gemini", "vertex-gemini":
		// The budget API defines 0 as off and -1 as dynamic thinking. A
		// toggle-only model may use a different API; don't invent that mapping.
		return controls.budget != nil
	case "openai-chat":
		return model.ProviderID == "openrouter" || alibabaProvider(model.ProviderID) || moonshotProvider(model.ProviderID) || zaiChatProvider(model.ProviderID) || minimaxProvider(model.ProviderID) || model.ProviderID == "deepseek"
	case "openai-responses":
		return alibabaProvider(model.ProviderID)
	}
	return false
}

// Budget choices are Vista presets, labelled with the actual token count. They
// are not native effort values, and are limited by both the published range and
// the selected transport's existing output/budget constraints.
func budgetBounds(model modelcfg.Model, option ReasoningOption) (int, int) {
	minimum, maximum := 1024, 32768
	if option.Min != nil {
		minimum = max(0, *option.Min)
	}
	if option.Max != nil {
		maximum = *option.Max
	}
	switch model.Provider {
	case "anthropic", "vertex-anthropic":
		output := 32000
		if model.OutputLimit > 0 {
			output = min(output, model.OutputLimit)
		}
		minimum, maximum = max(1024, minimum), min(maximum, output/2)
	case "bedrock":
		minimum, maximum = max(1024, minimum), min(maximum, 32768)
		output := model.OutputLimit
		if output <= 0 {
			output = 4096
		}
		maximum = min(maximum, min(output, 64000)/2)
	case "gemini", "vertex-gemini":
		maximum = min(maximum, 32768)
	case "openai-chat":
		if model.ProviderID == "openrouter" && model.OutputLimit > 0 {
			maximum = min(maximum, model.OutputLimit/2)
		}
		if alibabaProvider(model.ProviderID) && model.MaxCompletionTokens != nil {
			maximum = min(maximum, *model.MaxCompletionTokens/2)
		}
	}
	return minimum, maximum
}

func budgetChoices(model modelcfg.Model, option ReasoningOption) []string {
	minimum, maximum := budgetBounds(model, option)
	if minimum > maximum {
		return nil
	}
	values := []int{minimum, maximum}
	for _, value := range []int{1024, 2048, 8192, 16384, 32768} {
		if value > minimum && value < maximum {
			values = append(values, value)
		}
	}
	sort.Ints(values)
	values = slices.Compact(values)
	choices := make([]string, 0, len(values))
	for _, value := range values {
		choices = append(choices, "budget:"+strconv.Itoa(value))
	}
	return choices
}

func applyBudgetControl(model *modelcfg.Model, budget int) {
	switch model.Provider {
	case "anthropic", "vertex-anthropic", "bedrock":
		model.Thinking = &modelcfg.ThinkingOption{Type: "enabled", BudgetTokens: &budget}
	case "gemini", "vertex-gemini":
		model.ThinkingBudget = &budget
	case "openai-chat":
		if model.ProviderID == "openrouter" {
			options := modelcfg.OpenRouterOptions{}
			if model.OpenRouter != nil {
				options = *model.OpenRouter
			}
			options.Reasoning = &modelcfg.OpenRouterReasoning{MaxTokens: &budget}
			model.OpenRouter = &options
		} else {
			enabled := true
			model.EnableThinking, model.ThinkingBudget = &enabled, &budget
		}
	}
}

func applyToggleControl(model *modelcfg.Model, enabled bool) {
	switch model.Provider {
	case "gemini", "vertex-gemini":
		budget := 0
		if enabled {
			budget = -1
		}
		model.ThinkingBudget = &budget
	case "anthropic", "vertex-anthropic", "bedrock":
		if !enabled {
			model.Thinking = &modelcfg.ThinkingOption{Type: "disabled"}
		} else {
			applyAnthropicThinking(model)
		}
	case "openai-chat", "openai-responses":
		switch {
		case model.ProviderID == "openrouter":
			options := modelcfg.OpenRouterOptions{}
			if model.OpenRouter != nil {
				options = *model.OpenRouter
			}
			options.Reasoning = &modelcfg.OpenRouterReasoning{Enabled: &enabled}
			model.OpenRouter = &options
		case alibabaProvider(model.ProviderID):
			model.EnableThinking = &enabled
		default:
			kind := "disabled"
			if enabled {
				kind = "enabled"
				if minimaxProvider(model.ProviderID) {
					kind = "adaptive"
				}
			}
			model.Thinking = &modelcfg.ThinkingOption{Type: kind}
		}
	}
}
