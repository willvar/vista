package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/klauspost/compress/zstd"
)

const modelsDevCatalogURL = "https://models.dev/api.json"
const openCodeModelsCatalogURL = "https://models.opencode.ai/api.json"
const stencilCatalogURL = "https://catalog.stencil.so/models.json.zstd"

type modelsDevCatalog struct {
	Schema     int                            `json:"schema,omitempty"`
	Revision   string                         `json:"revision"`
	ModelCount int                            `json:"modelCount"`
	Providers  map[string]map[string]OMPModel `json:"providers"`
	Types      map[string]map[string]string   `json:"types,omitempty"`
	History    []modelsDevRevision            `json:"history,omitempty"`
	ETags      map[string]string              `json:"etags,omitempty"`
}

type modelsDevRevision struct {
	Revision   string    `json:"revision"`
	ReplacedAt time.Time `json:"replacedAt"`
}

const modelsDevHistoryWindow = 3 * 24 * time.Hour
const modelsDevSchema = 1

type modelsDevModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Reasoning bool   `json:"reasoning"`
	Tools     *bool  `json:"tool_call"`
	Provider  struct {
		Package string `json:"npm"`
	} `json:"provider"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	ReasoningOptions []ReasoningOption `json:"reasoning_options"`
}

func fetchModelsDevCatalog(ctx context.Context, client *http.Client) (*modelsDevCatalog, error) {
	urls := []string{modelsDevCatalogURL, openCodeModelsCatalogURL, stencilCatalogURL}
	rand.Shuffle(len(urls), func(i, j int) { urls[i], urls[j] = urls[j], urls[i] })
	return fetchModelsDevMirrors(ctx, client, urls, CurrentOMPCatalog().data.ModelsDev, time.Now())
}

// Unlike OMP's file SHA, an unversioned mirror's digest has no chronological
// order. Reject recently replaced contents and confirm suspicious changes with
// another publisher. Ordinary additions and small corrections need one download.
func fetchModelsDevMirrors(ctx context.Context, client *http.Client, urls []string, current *modelsDevCatalog, now time.Time) (*modelsDevCatalog, error) {
	type observation struct {
		catalog *modelsDevCatalog
		etag    string
		err     error
	}
	observed := map[string]observation{}
	read := func(url string) observation {
		if result, ok := observed[url]; ok {
			return result
		}
		attempt, cancel := context.WithTimeout(ctx, catalogDownloadTimeout)
		catalog, etag, err := downloadModelsDevCatalog(attempt, client, url, current)
		if err == nil {
			err = attempt.Err()
		}
		cancel()
		result := observation{catalog, etag, err}
		observed[url] = result
		return result
	}
	normalize := func(catalog *modelsDevCatalog) (*modelsDevCatalog, error) {
		var hints []*modelsDevCatalog
		for _, url := range urls {
			if result, ok := observed[url]; ok && result.err == nil && !modelsDevRecentlyReplaced(current, result.catalog.Revision, now) {
				hints = append(hints, result.catalog)
			}
		}
		return completeModelsDevTypes(catalog, current, hints)
	}
	accept := func(candidate *modelsDevCatalog) (*modelsDevCatalog, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		staged := *candidate
		staged.History = nil
		staged.ETags = map[string]string{}
		if current != nil {
			for _, previous := range current.History {
				if previous.ReplacedAt.Add(modelsDevHistoryWindow).After(now) {
					staged.History = append(staged.History, previous)
				}
			}
			if candidate.Revision != current.Revision {
				staged.History = append(staged.History, modelsDevRevision{current.Revision, now.UTC()})
			} else {
				staged.ETags = maps.Clone(current.ETags)
				if staged.ETags == nil {
					staged.ETags = map[string]string{}
				}
			}
		}
		for url, result := range observed {
			if result.err == nil && !modelsDevRecentlyReplaced(current, result.catalog.Revision, now) {
				equivalent, err := normalize(result.catalog)
				if err != nil || equivalent.Revision != candidate.Revision {
					continue
				}
				if result.etag == "" {
					delete(staged.ETags, url)
				} else {
					staged.ETags[url] = result.etag
				}
			}
		}
		return &staged, nil
	}
	var failures []error
	unconfirmed := false
	for _, url := range urls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := read(url)
		if result.err != nil {
			failures = append(failures, fmt.Errorf("%s: download/parse failed: %w", url, result.err))
			continue
		}
		if modelsDevRecentlyReplaced(current, result.catalog.Revision, now) {
			failures = append(failures, fmt.Errorf("%s: downloaded recently replaced models.dev metadata", url))
			continue
		}
		candidate, err := normalize(result.catalog)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: downloaded catalog normalization failed: %w", url, err))
			continue
		}
		if current != nil && candidate.Revision == current.Revision {
			if !unconfirmed {
				return accept(candidate)
			}
			continue // A disagreement must not be reported as "up to date".
		}
		if modelsDevRecentlyReplaced(current, candidate.Revision, now) {
			failures = append(failures, fmt.Errorf("%s returned recently replaced models.dev metadata", url))
			continue
		}
		changes := modelsDevChangesToConfirm(current, candidate)
		if len(changes) == 0 {
			return accept(candidate)
		}
		for _, otherURL := range urls {
			if modelsDevPublisher(url) == modelsDevPublisher(otherURL) {
				continue
			}
			read(otherURL)
		}
		// Newly read type information must be applied to both sides before
		// comparing removals. A missing type does not contradict "embedding".
		candidate, err = normalize(result.catalog)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: downloaded catalog normalization failed: %w", url, err))
			continue
		}
		if modelsDevRecentlyReplaced(current, candidate.Revision, now) {
			failures = append(failures, fmt.Errorf("%s returned recently replaced models.dev metadata", url))
			continue
		}
		witnesses := map[string]*modelsDevCatalog{}
		for _, otherURL := range urls {
			other, ok := observed[otherURL]
			if !ok || other.err != nil || modelsDevPublisher(url) == modelsDevPublisher(otherURL) || modelsDevRecentlyReplaced(current, other.catalog.Revision, now) {
				continue
			}
			catalog, err := normalize(other.catalog)
			if err == nil && !modelsDevRecentlyReplaced(current, catalog.Revision, now) {
				witnesses[otherURL] = catalog
			}
		}
		if err := confirmModelsDevChanges(modelsDevChangesToConfirm(current, candidate), witnesses); err == nil {
			return accept(candidate)
		} else {
			failures = append(failures, fmt.Errorf("%s: downloaded catalog %w", url, err))
		}
		unconfirmed = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, result := range observed {
		if result.err == nil {
			return nil, fmt.Errorf("models.dev catalog update rejected; keeping the current catalog: %w", errors.Join(failures...))
		}
	}
	return nil, fmt.Errorf("all models.dev catalog downloads failed: %w", errors.Join(failures...))
}

func modelsDevPublisher(url string) string {
	if url == modelsDevCatalogURL || url == openCodeModelsCatalogURL {
		return "models.dev"
	}
	return url
}

func downloadModelsDevCatalog(ctx context.Context, client *http.Client, url string, current *modelsDevCatalog) (*modelsDevCatalog, string, error) {
	limit := int64(32 << 20)
	if url == stencilCatalogURL {
		limit = 8 << 20
	}
	etag := ""
	if current != nil && current.Schema == modelsDevSchema {
		etag = current.ETags[url]
	}
	data, etag, unchanged, err := fetchCatalogResponse(ctx, client, url, "models.dev", limit, etag)
	if err != nil {
		return nil, "", err
	}
	if unchanged {
		return current, etag, nil
	}
	if url == stencilCatalogURL {
		data, err = decodeStencilDownload(data)
		if err != nil {
			return nil, "", err
		}
	}
	catalog, err := decodeModelsDevCatalog(data)
	return catalog, etag, err
}

func decodeStencilDownload(compressed []byte) ([]byte, error) {
	reader, err := zstd.NewReader(bytes.NewReader(compressed), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(32<<20))
	if err != nil {
		return nil, fmt.Errorf("decompress Stencil mirror: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, (32<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("decompress Stencil mirror: %w", err)
	}
	if len(data) > 32<<20 {
		return nil, errors.New("models.dev catalog exceeds 32 MiB")
	}
	return data, nil
}

func decodeModelsDevCatalog(data []byte) (*modelsDevCatalog, error) {
	var source map[string]struct {
		ID     string                    `json:"id"`
		Models map[string]modelsDevModel `json:"models"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("decode models.dev model data: %w", err)
	}
	catalog := &modelsDevCatalog{Schema: modelsDevSchema, Providers: map[string]map[string]OMPModel{}, Types: map[string]map[string]string{}}
	for providerID, entry := range source {
		if entry.ID != providerID {
			return nil, errors.New("models.dev provider identity does not match its catalog entry")
		}
		// Keep explicit types even for excluded rows: another mirror may omit
		// this field while still publishing the same non-conversation model.
		if providerID == "opencode" {
			providerID = "opencode-zen"
		}
		rows := map[string]OMPModel{}
		for id, raw := range entry.Models {
			if raw.ID != id {
				return nil, errors.New("models.dev model identity does not match its catalog entry")
			}
			if raw.Type != "" {
				if catalog.Types[providerID] == nil {
					catalog.Types[providerID] = map[string]string{}
				}
				catalog.Types[providerID][id] = raw.Type
			}
			// Generation-only endpoints do not belong in the conversation picker.
			if raw.Type != "" && raw.Type != "chat" || !slices.Contains(raw.Modalities.Input, "text") || !slices.Contains(raw.Modalities.Output, "text") {
				continue
			}
			api := modelsDevModelAPI(providerID, raw.Provider.Package)
			model := OMPModel{Name: raw.Name, API: api, Input: raw.Modalities.Input, Reasoning: raw.Reasoning,
				ContextWindow: raw.Limit.Context, MaxTokens: raw.Limit.Output, SupportsTools: raw.Tools}
			if model.Reasoning {
				model.ReasoningOptions = cloneReasoningOptions(raw.ReasoningOptions)
				for i := range model.ReasoningOptions {
					for j, value := range model.ReasoningOptions[i].Values {
						if value == "" || value == "null" {
							model.ReasoningOptions[i].Values[j] = "default"
						}
					}
				}
				var efforts []string
				for _, option := range raw.ReasoningOptions {
					if option.Type == "effort" {
						for _, value := range option.Values {
							if validEffortValue(value) && !slices.Contains(efforts, value) {
								efforts = append(efforts, value)
							}
						}
					}
				}
				if len(efforts) > 0 {
					mode := "effort"
					switch api {
					case "anthropic-messages":
						mode = "anthropic-adaptive"
					case "google-generative-ai", "google-vertex":
						mode = "google-level"
					}
					model.Thinking = &OMPThinking{Mode: mode, Efforts: efforts}
				}
			}
			rows[id] = model
		}
		if len(rows) > 0 {
			catalog.Providers[providerID] = rows
			catalog.ModelCount += len(rows)
		}
	}
	if err := validateCatalogModels(catalog.Providers, catalog.ModelCount); err != nil {
		return nil, fmt.Errorf("invalid models.dev catalog: %w", err)
	}
	var err error
	catalog.Revision, err = catalogRevision(catalog.Providers)
	return catalog, err
}

// Missing types are unknown, not a vote against an explicit classification.
// Prefer fresh observations to saved hints; an explicit type on the candidate
// itself is never overwritten. All maps belong to the staged result.
func completeModelsDevTypes(catalog, current *modelsDevCatalog, hints []*modelsDevCatalog) (*modelsDevCatalog, error) {
	staged := *catalog
	staged.Providers = maps.Clone(catalog.Providers)
	staged.Types = map[string]map[string]string{}
	for providerID, types := range catalog.Types {
		staged.Types[providerID] = maps.Clone(types)
	}
	staged.ModelCount = 0
	for providerID, models := range catalog.Providers {
		rows := maps.Clone(models)
		for id := range rows {
			kind := catalog.Types[providerID][id]
			if kind == "" {
				for _, hint := range hints {
					if other := hint.Types[providerID][id]; other != "" {
						if kind != "" && kind != other {
							return nil, fmt.Errorf("%s/%s type: conflicting classifications %q and %q", providerID, id, kind, other)
						}
						kind = other
					}
				}
				if kind == "" && current != nil {
					kind = current.Types[providerID][id]
				}
				if kind != "" {
					if staged.Types[providerID] == nil {
						staged.Types[providerID] = map[string]string{}
					}
					staged.Types[providerID][id] = kind
				}
			}
			if kind != "" && kind != "chat" {
				delete(rows, id)
			}
		}
		if len(rows) == 0 {
			delete(staged.Providers, providerID)
		} else {
			staged.Providers[providerID] = rows
			staged.ModelCount += len(rows)
		}
	}
	if err := validateCatalogModels(staged.Providers, staged.ModelCount); err != nil {
		return nil, err
	}
	var err error
	staged.Revision, err = catalogRevision(staged.Providers)
	return &staged, err
}

// models.dev supplies metadata, never endpoints, credentials or new transports.
// Existing OMP rows keep their API; new rows use Vista's provider adapter and
// the published API package discriminator for multi-API services such as Zen.
func modelsDevModelAPI(providerID, pkg string) string {
	if pkg == "" {
		if provider, err := LookupCatalogProvider(providerID); err == nil {
			pkg = provider.Package
		}
	}
	switch pkg {
	case "@ai-sdk/openai", "@ai-sdk/azure", "@ai-sdk/xai", "@ai-sdk/amazon-bedrock/mantle":
		return "openai-responses"
	case "@ai-sdk/anthropic", "@ai-sdk/google-vertex/anthropic":
		return "anthropic-messages"
	case "@ai-sdk/google":
		return "google-generative-ai"
	case "@ai-sdk/google-vertex":
		return "google-vertex"
	case "@ai-sdk/amazon-bedrock":
		return "bedrock-converse-stream"
	default:
		return "openai-completions"
	}
}

func mergeModelsDevCatalog(omp, modelsDev map[string]map[string]OMPModel) map[string]map[string]OMPModel {
	merged := make(map[string]map[string]OMPModel, len(omp))
	for providerID, models := range omp {
		rows := maps.Clone(models)
		for id, model := range rows {
			// OMP-only models retain their complete Thinking policy. Explicit
			// controls belong to models.dev and replace it by model, not by field.
			model.ReasoningOptions = nil
			rows[id] = model
		}
		merged[providerID] = rows
	}
	base := &OMPCatalogSnapshot{models: omp}
	for providerID, models := range modelsDev {
		rows := merged[providerID]
		if rows == nil {
			rows = map[string]OMPModel{}
		}
		for id, model := range models {
			// Older installed snapshots only retained explicit effort entries in
			// models.dev's Thinking field. Recover those without consulting OMP.
			if len(model.ReasoningOptions) == 0 && model.Thinking != nil {
				model.ReasoningOptions = []ReasoningOption{{Type: "effort", Values: slices.Clone(model.Thinking.Efforts)}}
			}
			if original, ok := base.model(providerID, id); ok {
				model.API = original.API
				if model.ContextWindow == 0 {
					model.ContextWindow = original.ContextWindow
				}
				if model.MaxTokens == 0 {
					model.MaxTokens = original.MaxTokens
				}
				if model.SupportsTools == nil {
					model.SupportsTools = original.SupportsTools
				}
			}
			rows[id] = model
		}
		merged[providerID] = rows
	}
	return merged
}
