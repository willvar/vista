package provider

import (
	"fmt"
	"slices"
	"strings"
)

type catalogResolveState struct {
	config        CatalogConfig
	metadata      CatalogMetadata
	entry         CatalogProvider
	credentialEnv []string
}

// ResolveCatalogConfig applies provider routes, endpoints and credential defaults.
// The caller retains the raw input for persistence; only this result is executable.
func ResolveCatalogConfig(config CatalogConfig) (CatalogResolution, error) {
	return CurrentOMPCatalog().ResolveConfig(config)
}

// ResolveConfig prepares a model using this snapshot, without activating it.
func (catalog *OMPCatalogSnapshot) ResolveConfig(config CatalogConfig) (CatalogResolution, error) {
	state := catalogResolveState{config: config}
	if err := catalog.resolveConfig(&state); err != nil {
		return CatalogResolution{}, err
	}
	return CatalogResolution{Config: state.config, Metadata: state.metadata, CredentialEnvironment: state.credentialEnv}, nil
}

func (catalog *OMPCatalogSnapshot) resolveConfig(state *catalogResolveState) error {
	model := &state.config
	state.metadata.BaseURLExplicit = model.BaseURL != ""
	if model.Provider != "catalog" {
		directAnthropic := model.Provider == "anthropic" && (model.ProviderID == "" || model.ProviderID == "anthropic")
		directOpenAIResponses := model.Provider == "openai-responses" && (model.ProviderID == "" || model.ProviderID == "openai")
		directGemini := model.Provider == "gemini" || model.Provider == "vertex-gemini"
		if model.Route != "" || model.UseCompletionURLs != nil || model.UseDeploymentURLs != nil || model.OpenRouter != nil || model.Thinking != nil && !directAnthropic || model.EnableThinking != nil || model.ThinkingBudget != nil && !directGemini || model.ThinkingLevel != "" && !directGemini || model.MaxCompletionTokens != nil || model.RepetitionPenalty != nil || model.PreserveThinking != nil || model.ClearThinking != nil || model.ReasoningEffort != "" && !directOpenAIResponses || model.Effort != "" && !directAnthropic || model.SafetyIdentifier != "" || model.PromptCacheKey != "" || model.ReasoningSplit != nil || model.ToolStream != nil || model.ParallelToolCalls != nil || model.ZAIDoSample != nil || model.ChatResponseFormat != nil || model.ZAIRequestID != "" || model.ZAIUserID != "" {
			return fmt.Errorf("model %q native route and options are only supported for catalog models", model.Key)
		}
		// Apply live metadata to copies in Public()/Resolve(), keeping the
		// user's configuration editable and free of derived fields.
		return nil
	}
	if model.ProviderID == "" {
		return fmt.Errorf("catalog model %q requires providerId", model.Key)
	}
	entry, err := catalog.resolveModel(model.ProviderID, model.ID)
	if err != nil {
		return fmt.Errorf("catalog model %q: %w", model.Key, err)
	}
	if entry.Protocol == "" {
		return fmt.Errorf("catalog Provider %q model %q has no implemented protocol route", model.ProviderID, model.ID)
	}
	if !slices.Contains(entry.InputModalities, "text") || !slices.Contains(entry.OutputModalities, "text") {
		return fmt.Errorf("catalog model %q cannot provide text input and output for an Agent Session", model.Key)
	}
	if model.Tools != nil && *model.Tools && !entry.Tools {
		return fmt.Errorf("catalog model %q does not support tool calls in the pinned inventory", model.Key)
	}
	if model.Tools == nil {
		tools := entry.Tools
		model.Tools = &tools
	}
	if model.Context > 0 && entry.ContextLimit > 0 && model.Context > entry.ContextLimit {
		return fmt.Errorf("catalog model %q context exceeds pinned limit", model.Key)
	}
	if model.Context == 0 {
		model.Context = entry.ContextLimit
	}
	state.metadata.InputLimit = entry.InputLimit
	state.metadata.OutputLimit = entry.OutputLimit
	state.metadata.ResponsesRoute = entry.ResponsesRoute
	state.metadata.Reasoning = entry.Reasoning
	state.metadata.Resolved = true
	state.metadata.Status = entry.Status
	state.metadata.InputModalities = entry.InputModalities
	state.metadata.OutputModalities = entry.OutputModalities
	model.Provider = entry.Protocol
	state.metadata.WireID = entry.WireModelID

	state.entry = entry

	adapter := catalogAdapterFor(model.ProviderID)
	if adapter.resolveRoute != nil {
		if err := adapter.resolveRoute(state); err != nil {
			return err
		}
	} else if model.Route != "" {
		return fmt.Errorf("catalog model %q does not support route selection", model.Key)
	}
	if adapter.resolveEndpoint != nil {
		if err := adapter.resolveEndpoint(state); err != nil {
			return err
		}
	}
	credential := adapter.resolveCredential
	if credential == nil {
		credential = resolveCatalogCredential
	}
	if err := credential(state); err != nil {
		return err
	}
	if adapter.finishEndpoint != nil {
		if err := adapter.finishEndpoint(state); err != nil {
			return err
		}
	}
	if model.BaseURL == "" {
		if strings.Contains(entry.BaseURL, "${") {
			return fmt.Errorf("catalog model %q needs baseUrl because its pinned route contains an unresolved variable", model.Key)
		} else {
			model.BaseURL = entry.BaseURL
		}
	}

	if adapter.finish != nil {
		return adapter.finish(state)
	}
	return nil
}
