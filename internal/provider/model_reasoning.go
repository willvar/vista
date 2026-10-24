package provider

import (
	"strings"

	modelcfg "vista/internal/model"
)

// Lookups only read the active provider-scoped OMP + models.dev catalog. Network
// access is confined to explicit catalog updates, never model requests.

// ModelLimit contains token limits published by the active model catalog. A
// zero field means that neither source published that limit.
type ModelLimit struct {
	Context int `json:"context,omitempty"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output,omitempty"`
}

// ModelReasoningOptionsFor returns the active thinking policy for a provider/model
// pair. Unknown models have no adjustable effort values.
func ModelReasoningOptionsFor(providerID, id string) []string {
	model, ok := ompModelForMetadata(providerID, id)
	if !ok {
		return nil
	}
	protocol := catalogProtocol(ompPackage(model.API, ""))
	if entry, err := ResolveCatalogModel(providerID, id); err == nil {
		protocol = entry.Protocol
	}
	return SessionReasoningOptions(modelcfg.Model{Provider: protocol, ProviderID: providerID, ID: id, OutputLimit: model.MaxTokens})
}

// ModelLimitsFor returns provider-scoped limits from the active model
// catalog. Unknown custom models intentionally keep unknown limits.
func ModelLimitsFor(providerID, id string) ModelLimit {
	model, ok := ompModelForMetadata(providerID, id)
	if !ok {
		return ModelLimit{}
	}
	return ModelLimit{Context: model.ContextWindow, Output: model.MaxTokens}
}

// ModelDisplayNameFor returns the catalog display name, or an empty string so
// callers can display the raw ID when neither source has metadata.
func ModelDisplayNameFor(providerID, id string) string {
	if model, ok := ompModelForMetadata(providerID, id); ok && strings.TrimSpace(model.Name) != "" {
		return model.Name
	}
	return ""
}

// ModelInputModalitiesFor returns provider-scoped input capabilities. A
// copy is returned so callers cannot mutate the embedded catalog's view.
func ModelInputModalitiesFor(providerID, id string) []string {
	model, ok := ompModelForMetadata(providerID, id)
	if !ok || len(model.Input) == 0 {
		return nil
	}
	return append([]string(nil), model.Input...)
}
