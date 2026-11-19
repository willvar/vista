package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// OutputMetadata describes outputs, not the permissions or capabilities of a
// specific account. Chat admission and generation routing are separate.
type OutputMetadata struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind,omitempty"`
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Preserve the digest of older cached catalogs that had no specialized rows.
func (c *modelsDevCatalog) revision() (string, error) {
	revision, err := catalogRevision(c.Providers)
	if err != nil || len(c.Specialized) == 0 {
		return revision, err
	}
	data, err := json.Marshal(c.Specialized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(revision), data...))
	return hex.EncodeToString(digest[:]), nil
}

func DeclaredModelKind(providerID, id string) string {
	c := CurrentOMPCatalog()
	if c == nil {
		return ""
	}
	for _, p := range catalogProviderCandidates(providerID) {
		if c.data.ModelsDev != nil {
			if kind := c.data.ModelsDev.Types[p][id]; kind != "" {
				return kind
			}
			if row, ok := c.data.ModelsDev.Specialized[p][id]; ok {
				return outputKind(row)
			}
		}
		if row, ok := c.data.Providers[p][id]; ok {
			if kind := outputKind(ompOutput(id, row)); kind != "" {
				return kind
			}
		}
	}
	return ""
}

// OutputKind uses declared categories or unambiguous non-chat outputs.
func OutputKind(row OutputMetadata) string { return outputKind(row) }

func outputKind(row OutputMetadata) string {
	if kind := ConversationCategory(row.Kind); kind != "" {
		return kind
	}
	if slices.Contains(row.Output, "decisions") {
		return "decision"
	}
	if slices.Contains(row.Output, "embeddings") {
		return "embedding"
	}
	if slices.Contains(row.Output, "rerank") {
		return "reranking"
	}
	if !slices.Contains(row.Output, "text") {
		for _, kind := range []string{"image", "video", "audio"} {
			if slices.Contains(row.Output, kind) {
				return kind
			}
		}
	}
	return ""
}

// SpecializedModels returns catalog candidates and any explicitly supported
// adapter IDs. Name lookup does not decide membership or routing capabilities.
func SpecializedModels(providerID, kind string, adapterIDs ...string) []OutputMetadata {
	return CurrentOMPCatalog().specializedModels(providerID, kind, adapterIDs...)
}

func (c *OMPCatalogSnapshot) specializedModels(providerID, kind string, adapterIDs ...string) []OutputMetadata {
	var data ompCatalogFile
	if c != nil {
		data = c.data
	}
	providers := catalogProviderCandidates(providerID)
	var rows []OutputMetadata
	seen := map[string]bool{}
	add := func(row OutputMetadata) {
		if outputKind(row) == kind && !seen[row.ID] {
			row.Kind = kind
			seen[row.ID] = true
			rows = append(rows, row)
		}
	}
	for _, p := range providers {
		if data.ModelsDev != nil {
			for _, row := range data.ModelsDev.Specialized[p] {
				add(row)
			}
		}
		for id, m := range data.Providers[p] {
			if data.ModelsDev != nil {
				if declared := ConversationCategory(data.ModelsDev.Types[p][id]); declared != "" && declared != kind {
					continue
				}
			}
			add(ompOutput(id, m))
		}
		// Older mirrors retained type declarations even when they didn't
		// serialize the full non-chat rows or display names.
		if data.ModelsDev != nil {
			for id, declared := range data.ModelsDev.Types[p] {
				if _, richer := data.ModelsDev.Specialized[p][id]; !richer {
					add(OutputMetadata{ID: id, Kind: declared})
				}
			}
		}
	}
	slices.SortFunc(rows, func(a, b OutputMetadata) int { return strings.Compare(a.ID, b.ID) })
	for _, id := range adapterIDs {
		add(OutputMetadata{ID: id, Kind: kind})
	}
	for i := range rows {
		rows[i].Name = data.outputDisplayName(providers, rows[i].ID)
	}
	return rows
}

// Select only the name field. A legacy type-only entry can still use its OMP
// name even if OMP does not identify the model as a specialized output.
func (data ompCatalogFile) outputDisplayName(providers []string, id string) string {
	if dev := data.ModelsDev; dev != nil {
		for _, p := range providers {
			if name := strings.TrimSpace(dev.Specialized[p][id].Name); name != "" && name != id {
				return name
			}
			if name := strings.TrimSpace(dev.Providers[p][id].Name); name != "" && name != id {
				return name
			}
		}
	}
	for _, p := range providers {
		if name := strings.TrimSpace(data.Providers[p][id].Name); name != "" && name != id {
			return name
		}
	}
	return id
}

// Compatible generic lists may use llm/model as a resource label. Only a
// known specialized category is evidence that a model is not for conversation.
func ConversationCategory(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "chat", "llm":
		return "chat"
	case "image_generation":
		return "image"
	case "video_generation":
		return "video"
	case "image", "video", "audio", "speech", "transcription", "realtime", "decision", "embedding", "rerank", "reranking", "extraction":
		return strings.ToLower(strings.TrimSpace(kind))
	case "decisions":
		return "decision"
	case "embeddings":
		return "embedding"
	default:
		return ""
	}
}

// SpecializedModel looks up an exact route without materializing the roster.
func SpecializedModel(providerID, id, kind string) (OutputMetadata, bool) {
	c := CurrentOMPCatalog()
	if c == nil {
		return OutputMetadata{}, false
	}
	for _, p := range catalogProviderCandidates(providerID) {
		if c.data.ModelsDev != nil {
			if row, ok := c.data.ModelsDev.Specialized[p][id]; ok {
				return row, outputKind(row) == kind
			}
		}
		if m, ok := c.data.Providers[p][id]; ok {
			if c.data.ModelsDev != nil {
				if declared := ConversationCategory(c.data.ModelsDev.Types[p][id]); declared != "" && declared != kind {
					continue
				}
			}
			row := ompOutput(id, m)
			if outputKind(row) == kind {
				return row, true
			}
		}
		if c.data.ModelsDev != nil && ConversationCategory(c.data.ModelsDev.Types[p][id]) == kind {
			return OutputMetadata{ID: id, Kind: kind}, true
		}
	}
	return OutputMetadata{}, false
}

// Old persisted OMP catalogs omitted kind/output but retained explicit wire
// APIs. Recover those declarations without interpreting model ID spelling.
func ompOutput(id string, m OMPModel) OutputMetadata {
	row := OutputMetadata{ID: id, Name: m.Name, Kind: m.Kind, Input: m.Input, Output: m.Output}
	if row.Kind == "" {
		switch m.API {
		case "openai-images", "openrouter-images":
			row.Kind = "image"
		case "openrouter-video":
			row.Kind = "video"
		case "openrouter-decisions":
			row.Kind = "decision"
		}
	}
	return row
}
