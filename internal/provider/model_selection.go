package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	modelcfg "vista/internal/model"
)

// ModelReasoningEffort returns the named setting used for display. It must not
// expose the statistics configuration key or infer a level from token budgets.
func ModelReasoningEffort(model modelcfg.Model) string {
	if model.ReasoningChoice != "" {
		return model.ReasoningChoice
	}
	for _, effort := range []string{model.ReasoningEffort, model.Effort, model.ThinkingLevel} {
		if effort != "" {
			return effort
		}
	}
	if model.OpenRouter != nil && model.OpenRouter.Reasoning != nil {
		return model.OpenRouter.Reasoning.Effort
	}
	return ""
}

// ModelSelectionEffort identifies the actual reasoning configuration. Simple
// effort configurations retain their readable name; combined/budget settings
// get a deterministic key so incompatible observations cannot be mixed.
func ModelSelectionEffort(model modelcfg.Model) string {
	fields := map[string]any{}
	if model.ReasoningEffort != "" {
		fields["effort"] = model.ReasoningEffort
	}
	if model.Effort != "" {
		fields["anthropicEffort"] = model.Effort
	}
	if model.ThinkingLevel != "" {
		fields["thinkingLevel"] = model.ThinkingLevel
	}
	if model.Thinking != nil {
		fields["thinking"] = model.Thinking
	}
	if model.EnableThinking != nil {
		fields["enableThinking"] = *model.EnableThinking
	}
	if model.ThinkingBudget != nil {
		fields["thinkingBudget"] = *model.ThinkingBudget
	}
	if model.OpenRouter != nil && model.OpenRouter.Reasoning != nil {
		fields["openRouterReasoning"] = model.OpenRouter.Reasoning
	}
	if len(fields) == 0 {
		return "default"
	}
	if len(fields) == 1 {
		for _, name := range []string{"effort", "anthropicEffort", "thinkingLevel"} {
			if value, ok := fields[name].(string); ok {
				return value
			}
		}
	}
	encoded, _ := json.Marshal(fields)
	return "config:" + string(encoded)
}

// ModelEndpointID is safe to share: credentials, userinfo, URL query and
// fragment never enter the digest or the statistics database.
func ModelEndpointID(model modelcfg.Model) string {
	address := model.BaseURL
	if parsed, err := url.Parse(address); err == nil {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawPath = ""
		address = parsed.String()
	} else {
		address = ""
	}
	data, _ := json.Marshal([]string{model.Provider, model.ProviderID, address, model.AWSRegion})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
