package provider

import (
	"fmt"
	"slices"
	"strings"
)

// Each service adapter supplies only the reasoning controls it accepts.
// Validation still runs in field order so the first reported error stays stable.
type catalogValidationPolicy struct {
	thinking        func(CatalogConfig) error
	messagesEffort  func(CatalogConfig) bool
	reasoningEffort func(CatalogConfig) error
}

var anthropicValidation = catalogValidationPolicy{
	thinking: validateAnthropicCatalogThinking,
	messagesEffort: func(item CatalogConfig) bool {
		return slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, item.Effort)
	},
}
var bedrockValidation = catalogValidationPolicy{
	thinking:        validateBedrockCatalogThinking,
	reasoningEffort: validateBedrockCatalogReasoningEffort,
}
var minimaxValidation = catalogValidationPolicy{
	thinking:        validateMiniMaxCatalogThinking,
	reasoningEffort: validateMiniMaxCatalogReasoningEffort,
}
var moonshotValidation = catalogValidationPolicy{
	thinking:        validateMoonshotCatalogThinking,
	messagesEffort:  func(item CatalogConfig) bool { return slices.Contains([]string{"low", "high", "max"}, item.Effort) },
	reasoningEffort: validateMoonshotCatalogReasoningEffort,
}
var alibabaValidation = catalogValidationPolicy{
	thinking: validateAlibabaCatalogThinking,
	messagesEffort: func(item CatalogConfig) bool {
		return slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.Effort)
	},
	reasoningEffort: validateAlibabaCatalogReasoningEffort,
}
var metaValidation = catalogValidationPolicy{
	thinking: validateMetaCatalogThinking,
	messagesEffort: func(item CatalogConfig) bool {
		return slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, item.Effort)
	},
	reasoningEffort: validateMetaCatalogReasoningEffort,
}
var zaiValidation = catalogValidationPolicy{
	thinking: validateZAICatalogThinking,
	messagesEffort: func(item CatalogConfig) bool {
		return (item.ProviderID == "zai-coding-plan" || item.ProviderID == "zhipuai-coding-plan") && slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.Effort)
	},
	reasoningEffort: validateZAICatalogReasoningEffort,
}
var xaiValidation = catalogValidationPolicy{reasoningEffort: validateXAICatalogReasoningEffort}
var openaiValidation = catalogValidationPolicy{reasoningEffort: validateOpenAICatalogReasoningEffort}

func catalogValidationFor(item CatalogConfig) catalogValidationPolicy {
	if item.ProviderID == "" && item.Provider == "anthropic" {
		return anthropicValidation
	}
	if policy := catalogAdapterFor(item.ProviderID).validation; policy != nil {
		return *policy
	}
	return catalogValidationPolicy{}
}

func unsupportedCatalogThinking(item CatalogConfig) error {
	return fmt.Errorf("model %q thinking requires a ZAI catalog model", item.Key)
}

func unsupportedCatalogReasoningEffort(item CatalogConfig) error {
	return fmt.Errorf("model %q reasoningEffort is unsupported for this catalog Provider", item.Key)
}

func validateAnthropicCatalogThinking(item CatalogConfig) error {
	if item.Provider != "anthropic" {
		return unsupportedCatalogThinking(item)
	}
	thinking := item.Thinking
	budget := thinking.BudgetTokens
	if budget == nil {
		budget = thinking.BudgetAlias
	}
	if thinking.ClearThinking != nil || thinking.Keep != "" || thinking.BudgetTokens != nil && thinking.BudgetAlias != nil || thinking.Display != "" && thinking.Display != "summarized" && thinking.Display != "omitted" || thinking.BlockBinding != nil && thinking.BlockBinding.PrefixMismatchBehavior != "" && thinking.BlockBinding.PrefixMismatchBehavior != "error" && thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" || thinking.Type == "enabled" && (budget == nil || *budget < 1024 || *budget > 32000) || thinking.Type != "enabled" && budget != nil || thinking.Type == "disabled" && (thinking.Display != "" || thinking.BlockBinding != nil) || thinking.Type != "enabled" && thinking.Type != "adaptive" && thinking.Type != "disabled" {
		return fmt.Errorf("model %q has invalid Anthropic thinking option", item.Key)
	}
	return nil
}

func validateBedrockCatalogThinking(item CatalogConfig) error {
	thinking := item.Thinking
	budget := thinking.BudgetTokens
	if budget == nil {
		budget = thinking.BudgetAlias
	}
	if item.Provider != "bedrock" || !strings.Contains(item.ID, "anthropic.claude") || thinking.Type != "enabled" || budget == nil || *budget < 1024 || *budget > 32000 || thinking.BudgetTokens != nil && thinking.BudgetAlias != nil || thinking.ClearThinking != nil || thinking.Keep != "" || thinking.Display != "" || thinking.BlockBinding != nil {
		return fmt.Errorf("model %q has invalid Bedrock thinking option", item.Key)
	}
	return nil
}

func validateMiniMaxCatalogThinking(item CatalogConfig) error {
	if item.Provider != "openai-chat" && item.Provider != "anthropic" || item.Thinking.Type != "adaptive" && item.Thinking.Type != "disabled" || item.Thinking.ClearThinking != nil || item.Thinking.Keep != "" || item.Thinking.BudgetTokens != nil || item.Thinking.BudgetAlias != nil || item.Thinking.Display != "" || item.Thinking.BlockBinding != nil {
		return fmt.Errorf("model %q has invalid MiniMax thinking option", item.Key)
	}
	return nil
}

func validateMoonshotCatalogThinking(item CatalogConfig) error {
	if item.Provider != "openai-chat" || item.Thinking.Type != "enabled" && item.Thinking.Type != "disabled" || item.Thinking.ClearThinking != nil || item.Thinking.Keep != "" && item.Thinking.Keep != "all" || item.Thinking.BudgetTokens != nil || item.Thinking.BudgetAlias != nil || item.Thinking.Display != "" || item.Thinking.BlockBinding != nil {
		return fmt.Errorf("model %q has invalid Moonshot Chat thinking option", item.Key)
	}
	return nil
}

func validateAlibabaCatalogThinking(item CatalogConfig) error {
	chat := item.Provider == "openai-chat" && (item.Thinking.Type == "adaptive" || item.Thinking.Type == "disabled") && item.Thinking.BudgetTokens == nil && item.Thinking.BudgetAlias == nil
	budget := item.Thinking.BudgetTokens
	if budget == nil {
		budget = item.Thinking.BudgetAlias
	}
	messages := item.Provider == "anthropic" && (item.Thinking.Type == "enabled" || item.Thinking.Type == "disabled") && !(item.Thinking.BudgetTokens != nil && item.Thinking.BudgetAlias != nil) && (budget == nil || *budget >= -1 && *budget <= 4096)
	if !chat && !messages || item.Thinking.ClearThinking != nil || item.Thinking.Keep != "" || item.Thinking.Display != "" || item.Thinking.BlockBinding != nil {
		return fmt.Errorf("model %q has invalid Alibaba thinking option", item.Key)
	}
	return nil
}

func validateMetaCatalogThinking(item CatalogConfig) error {
	budget := item.Thinking.BudgetTokens
	if budget == nil {
		budget = item.Thinking.BudgetAlias
	}
	if item.Provider != "anthropic" || item.Thinking.Type != "adaptive" && item.Thinking.Type != "disabled" && item.Thinking.Type != "enabled" || item.Thinking.ClearThinking != nil || item.Thinking.Keep != "" || item.Thinking.BudgetTokens != nil && item.Thinking.BudgetAlias != nil || item.Thinking.Type == "enabled" && (budget == nil || *budget < 1024 || *budget > 4096) || item.Thinking.Type != "enabled" && budget != nil || item.Thinking.Type == "disabled" && item.Thinking.Display != "" || item.Thinking.Display != "" && item.Thinking.Display != "summarized" && item.Thinking.Display != "omitted" || item.Thinking.BlockBinding != nil {
		return fmt.Errorf("model %q has invalid Meta Messages thinking option", item.Key)
	}
	return nil
}

func validateZAICatalogThinking(item CatalogConfig) error {
	if item.ProviderID != "zai-coding-plan" && item.ProviderID != "zai" && item.ProviderID != "zhipuai" && item.ProviderID != "zhipuai-coding-plan" {
		return fmt.Errorf("model %q thinking requires a ZAI catalog model", item.Key)
	}
	switch item.Provider {
	case "openai-chat":
		if item.Thinking.Type != "enabled" && item.Thinking.Type != "disabled" || item.Thinking.Keep != "" || item.Thinking.BudgetTokens != nil || item.Thinking.BudgetAlias != nil || item.Thinking.Display != "" || item.Thinking.BlockBinding != nil {
			return fmt.Errorf("model %q has invalid ZAI Chat thinking type", item.Key)
		}
	case "anthropic":
		if item.ProviderID != "zai-coding-plan" && item.ProviderID != "zhipuai-coding-plan" || item.Thinking.Type != "enabled" && item.Thinking.Type != "adaptive" && item.Thinking.Type != "disabled" || item.Thinking.ClearThinking != nil || item.Thinking.Keep != "" || item.Thinking.BudgetTokens != nil || item.Thinking.BudgetAlias != nil || item.Thinking.Display != "" || item.Thinking.BlockBinding != nil {
			return fmt.Errorf("model %q has invalid ZAI Messages thinking option", item.Key)
		}
	default:
		return fmt.Errorf("model %q thinking is unsupported for this route", item.Key)
	}
	return nil
}

func validateBedrockCatalogReasoningEffort(item CatalogConfig) error {
	bedrockConverse := item.Provider == "bedrock" && slices.Contains([]string{"low", "medium", "high", "max"}, item.ReasoningEffort)
	mantleResponses := item.Provider == "bedrock-mantle-responses" && slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort)
	if !bedrockConverse && !mantleResponses {
		return fmt.Errorf("model %q has invalid Bedrock reasoningEffort", item.Key)
	}
	return nil
}

func validateZAICatalogReasoningEffort(item CatalogConfig) error {
	if !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid ZAI reasoningEffort", item.Key)
	}
	return nil
}

func validateMoonshotCatalogReasoningEffort(item CatalogConfig) error {
	if !slices.Contains([]string{"low", "high", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid Moonshot reasoningEffort", item.Key)
	}
	return nil
}

func validateMiniMaxCatalogReasoningEffort(item CatalogConfig) error {
	if !slices.Contains([]string{"minimax", "minimax-cn", "minimax-coding-plan", "minimax-cn-coding-plan"}, item.ProviderID) {
		return unsupportedCatalogReasoningEffort(item)
	}
	if item.Provider != "openai-responses" || !slices.Contains([]string{"none", "minimal", "low", "medium", "high"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid MiniMax Responses reasoningEffort", item.Key)
	}
	return nil
}

func validateAlibabaCatalogReasoningEffort(item CatalogConfig) error {
	if !slices.Contains([]string{"alibaba", "alibaba-cn", "alibaba-coding-plan", "alibaba-coding-plan-cn", "alibaba-token-plan", "alibaba-token-plan-cn"}, item.ProviderID) {
		return unsupportedCatalogReasoningEffort(item)
	}
	if item.Provider != "openai-responses" && item.Provider != "openai-chat" || !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid Alibaba reasoningEffort", item.Key)
	}
	return nil
}

func validateMetaCatalogReasoningEffort(item CatalogConfig) error {
	if item.Provider != "openai-responses" || !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid Meta Responses reasoningEffort", item.Key)
	}
	return nil
}

func validateXAICatalogReasoningEffort(item CatalogConfig) error {
	if !slices.Contains([]string{"openai-responses", "openai-chat"}, item.Provider) || !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid xAI reasoningEffort", item.Key)
	}
	return nil
}

func validateOpenAICatalogReasoningEffort(item CatalogConfig) error {
	if item.Provider != "openai-responses" || !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, item.ReasoningEffort) {
		return fmt.Errorf("model %q has invalid OpenAI Responses reasoningEffort", item.Key)
	}
	return nil
}
