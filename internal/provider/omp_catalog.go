package provider

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// OMPModel is the stable subset of an oh-my-pi catalog row that Vista can use
// without importing OMP's TypeScript runtime or transport implementation.
// Zero limits mean that OMP did not publish that limit.
type OMPModel struct {
	Name          string       `json:"name"`
	API           string       `json:"api"`
	Reasoning     bool         `json:"reasoning"`
	Input         []string     `json:"input"`
	ContextWindow int          `json:"contextWindow"`
	MaxTokens     int          `json:"maxTokens"`
	SupportsTools *bool        `json:"supportsTools,omitempty"`
	Thinking      *OMPThinking `json:"thinking,omitempty"`
}

// OMPThinking preserves OMP's provider/model policy rather than reducing it
// to a generic effort list. The extra fields matter for mandatory
// reasoning and provider-specific remaps; callers that only need the selector
// should use ModelReasoningOptionsFor.
type OMPThinking struct {
	Mode            string            `json:"mode"`
	Efforts         []string          `json:"efforts"`
	DefaultLevel    string            `json:"defaultLevel,omitempty"`
	EffortMap       map[string]string `json:"effortMap,omitempty"`
	SupportsDisplay bool              `json:"supportsDisplay,omitempty"`
	RequiresEffort  bool              `json:"requiresEffort,omitempty"`
}

type ompCatalogFile struct {
	Source     string                         `json:"source"`
	Revision   string                         `json:"revision"`
	ModelCount int                            `json:"modelCount"`
	UpdatedAt  string                         `json:"updatedAt,omitempty"`
	Providers  map[string]map[string]OMPModel `json:"providers"`
	ModelsDev  *modelsDevCatalog              `json:"modelsDev,omitempty"`
}

//go:embed omp_models.json
var bundledOMPCatalog []byte

var (
	ompCatalogOnce sync.Once
	ompCatalogData atomic.Pointer[OMPCatalogSnapshot]
	ompCatalogErr  error
)

// OMPCatalogSnapshot is a validated immutable catalog. Downloads can be checked
// against saved connections before they become visible to concurrent readers.
type OMPCatalogSnapshot struct {
	data   ompCatalogFile
	models map[string]map[string]OMPModel
}

const maxOMPCatalogBytes = 8 << 20

func loadOMPCatalog() {
	catalog, err := decodeOMPCatalog(bundledOMPCatalog)
	ompCatalogErr = err
	if err == nil {
		ompCatalogData.Store(catalog)
	}
}

func decodeOMPCatalog(data []byte) (*OMPCatalogSnapshot, error) {
	var catalog ompCatalogFile
	if len(data) > maxOMPCatalogBytes {
		return nil, errors.New("OMP model catalog exceeds 8 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode OMP model catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("OMP model catalog has extra data")
	}
	if catalog.Source != "oh-my-pi" || catalog.Revision == "" || len(catalog.Providers) == 0 || len(catalog.Providers) > 256 || catalog.ModelCount <= 0 || catalog.ModelCount > 20000 {
		return nil, errors.New("invalid OMP model catalog header")
	}
	if err := validateCatalogModels(catalog.Providers, catalog.ModelCount); err != nil {
		return nil, err
	}
	models := catalog.Providers
	if catalog.ModelsDev != nil {
		if catalog.ModelsDev.Revision == "" {
			return nil, errors.New("invalid models.dev catalog revision")
		}
		if err := validateCatalogModels(catalog.ModelsDev.Providers, catalog.ModelsDev.ModelCount); err != nil {
			return nil, fmt.Errorf("invalid models.dev catalog: %w", err)
		}
		revision, err := catalogRevision(catalog.ModelsDev.Providers)
		if err != nil || revision != catalog.ModelsDev.Revision {
			return nil, errors.New("models.dev catalog digest does not match its metadata")
		}
		for _, previous := range catalog.ModelsDev.History {
			if len(previous.Revision) != 64 || previous.ReplacedAt.IsZero() {
				return nil, errors.New("invalid models.dev catalog history")
			}
		}
		models = mergeModelsDevCatalog(catalog.Providers, catalog.ModelsDev.Providers)
	}
	return &OMPCatalogSnapshot{data: catalog, models: models}, nil
}

func validateCatalogModels(providers map[string]map[string]OMPModel, modelCount int) error {
	if len(providers) == 0 || len(providers) > 256 || modelCount <= 0 || modelCount > 20000 {
		return errors.New("invalid model catalog size")
	}
	count := 0
	for providerID, models := range providers {
		if providerID == "" || len(providerID) > 128 || len(models) == 0 {
			return errors.New("invalid OMP provider entry")
		}
		for modelID, model := range models {
			if modelID == "" || len(modelID) > 512 || model.Name == "" || len(model.Name) > 512 || model.API == "" || len(model.API) > 128 || len(model.Input) == 0 || len(model.Input) > 5 {
				return errors.New("invalid OMP model entry")
			}
			seen := map[string]bool{}
			for _, modality := range model.Input {
				switch modality {
				case "text", "image", "audio", "video", "pdf":
				default:
					return errors.New("invalid OMP model modality")
				}
				if seen[modality] {
					return errors.New("duplicate OMP model modality")
				}
				seen[modality] = true
			}
			if model.ContextWindow < 0 || model.MaxTokens < 0 {
				return errors.New("invalid OMP model limits")
			}
			if model.Thinking != nil {
				if model.Thinking.Mode == "" || len(model.Thinking.Mode) > 64 || len(model.Thinking.Efforts) == 0 || len(model.Thinking.Efforts) > 16 {
					return errors.New("invalid OMP thinking policy")
				}
				efforts := map[string]bool{}
				for _, effort := range model.Thinking.Efforts {
					if !validEffortValue(effort) || efforts[effort] {
						return errors.New("invalid OMP thinking effort")
					}
					efforts[effort] = true
				}
				if model.Thinking.DefaultLevel != "" && !efforts[model.Thinking.DefaultLevel] {
					return errors.New("invalid OMP default thinking effort")
				}
				for effort, wire := range model.Thinking.EffortMap {
					if !efforts[effort] || wire == "" || len(wire) > 64 {
						return errors.New("invalid OMP thinking effort map")
					}
				}
			}
			count++
		}
	}
	if count != modelCount {
		return errors.New("OMP model count does not match snapshot")
	}
	return nil
}

func validEffortValue(value string) bool {
	switch value {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// CurrentOMPCatalog returns the active, immutable metadata snapshot.
func CurrentOMPCatalog() *OMPCatalogSnapshot {
	ompCatalogOnce.Do(loadOMPCatalog)
	return ompCatalogData.Load()
}

// RestoreBundledOMPCatalog drops downloaded metadata from the active process
// after a full reset, without fetching any external catalog.
func RestoreBundledOMPCatalog() error {
	catalog, err := decodeOMPCatalog(bundledOMPCatalog)
	if err == nil {
		catalog.Activate()
	}
	return err
}

func ensureOMPCatalog() bool { return CurrentOMPCatalog() != nil }

// Activate publishes a fully validated snapshot in a single operation.
func (catalog *OMPCatalogSnapshot) Activate() {
	ompCatalogOnce.Do(loadOMPCatalog)
	ompCatalogData.Store(catalog)
}

// OMPCatalogRevision reports the active catalog without making a network call.
func OMPCatalogRevision() string {
	if catalog := CurrentOMPCatalog(); catalog != nil {
		return catalog.data.Revision
	}
	return ""
}

func ompProviderCandidates(providerID string) []string {
	switch providerID {
	case "amazon-bedrock":
		// Vista's AWS connection serves both Converse and Mantle endpoints.
		return []string{"amazon-bedrock", "bedrock-mantle"}
	case "opencode":
		// OMP calls the paid Zen endpoint opencode-zen; Vista keeps the
		// shorter OpenCode provider ID for its existing settings format.
		return []string{"opencode-zen", "opencode"}
	case "moonshotai", "moonshotai-cn":
		return []string{"moonshot"}
	case "togetherai":
		return []string{"together"}
	case "vercel":
		return []string{"vercel-ai-gateway"}
	case "fireworks-ai":
		return []string{"fireworks"}
	case "minimax-coding-plan":
		return []string{"minimax-code"}
	case "minimax-cn-coding-plan":
		return []string{"minimax-code-cn"}
	case "kimi-code-plan-cn", "kimi-code-plan-global":
		return []string{"kimi-code"}
	case "zhipuai-coding-plan":
		return []string{"zhipu-coding-plan"}
	case "zai-coding-plan":
		return []string{"zai"}
	default:
		return []string{providerID}
	}
}

// An online provider's exact identity wins before OMP aliases. Regional and
// plan catalogs can then keep different metadata without overwriting each other.
func catalogProviderCandidates(providerID string) []string {
	if providerID == "opencode" {
		return ompProviderCandidates(providerID)
	}
	return append([]string{providerID}, ompProviderCandidates(providerID)...)
}

// ompTextModel excludes non-conversation APIs. OMP does not publish output
// modalities; its explicit no-tools, no-output-token rows describe generation
// endpoints (including OpenAI images), rather than text conversations.
func ompTextModel(model OMPModel) bool {
	if !containsCatalogModality(model.Input, "text") || model.MaxTokens == 0 && model.SupportsTools != nil && !*model.SupportsTools {
		return false
	}
	switch model.API {
	case "openai-completions", "openai-responses", "openai-codex-responses", "azure-openai-responses", "anthropic-messages", "google-generative-ai", "google-gemini-cli", "google-vertex", "bedrock-converse-stream", "openrouter":
		return true
	default:
		return false
	}
}

func ompModel(providerID, modelID string) (OMPModel, bool) {
	return CurrentOMPCatalog().model(providerID, modelID)
}

func (catalog *OMPCatalogSnapshot) model(providerID, modelID string) (OMPModel, bool) {
	if catalog == nil || modelID == "" {
		return OMPModel{}, false
	}
	for _, candidate := range catalogProviderCandidates(providerID) {
		if model, ok := catalog.models[candidate][modelID]; ok {
			model.Input = append([]string(nil), model.Input...)
			if model.SupportsTools != nil {
				tools := *model.SupportsTools
				model.SupportsTools = &tools
			}
			if model.Thinking != nil {
				thinking := *model.Thinking
				thinking.Efforts = append([]string(nil), thinking.Efforts...)
				thinking.EffortMap = mapsClone(thinking.EffortMap)
				model.Thinking = &thinking
			}
			return model, true
		}
	}
	return OMPModel{}, false
}

// ompModelForMetadata supplies metadata for custom connections without choosing
// their wire protocol. Provider-scoped rows always win; for a custom or unknown
// service identity, prefer the canonical OpenAI row and then stable common
// Provider identities before falling back to lexical order. This is still a
// local catalog lookup and never merges rows from different Providers.
func ompModelForMetadata(providerID, modelID string) (OMPModel, bool) {
	catalog := CurrentOMPCatalog()
	if model, ok := catalog.model(providerID, modelID); ok {
		return model, true
	}
	if providerID != "" && providerID != "custom" || catalog == nil || modelID == "" {
		return OMPModel{}, false
	}
	preferred := []string{"openai", "opencode-zen", "opencode-go", "anthropic", "google", "deepseek"}
	seen := map[string]bool{}
	for _, candidate := range preferred {
		seen[candidate] = true
		if _, ok := catalog.models[candidate][modelID]; ok {
			return catalog.model(candidate, modelID)
		}
	}
	providers := make([]string, 0, len(catalog.models))
	for candidate := range catalog.models {
		if !seen[candidate] {
			providers = append(providers, candidate)
		}
	}
	sort.Strings(providers)
	for _, candidate := range providers {
		if _, ok := catalog.models[candidate][modelID]; ok {
			return catalog.model(candidate, modelID)
		}
	}
	return OMPModel{}, false
}

// OMPModelMetadata returns a copy of the active row for a Provider/model pair.
func OMPModelMetadata(providerID, modelID string) (OMPModel, bool) {
	return ompModel(providerID, modelID)
}

// OMPModelIDs returns the sorted active model IDs for a Provider. Runtime
// /models discovery remains authoritative for account availability.
func OMPModelIDs(providerID string) []string {
	return CurrentOMPCatalog().modelIDs(providerID)
}

func (catalog *OMPCatalogSnapshot) modelIDs(providerID string) []string {
	if catalog == nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, candidate := range catalogProviderCandidates(providerID) {
		for modelID := range catalog.models[candidate] {
			if !seen[modelID] {
				seen[modelID] = true
				ids = append(ids, modelID)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func mapsClone(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
