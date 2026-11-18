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
		if row, ok := c.data.Providers[p][id]; ok && row.Kind != "" {
			return row.Kind
		}
	}
	return ""
}

func outputKind(row OutputMetadata) string {
	if row.Kind != "" {
		return row.Kind
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

func SpecializedModels(providerID, kind string) []OutputMetadata {
	c := CurrentOMPCatalog()
	if c == nil {
		return nil
	}
	var rows []OutputMetadata
	seen := map[string]bool{}
	add := func(row OutputMetadata) {
		if outputKind(row) == kind && !seen[row.ID] {
			seen[row.ID] = true
			rows = append(rows, row)
		}
	}
	for _, p := range catalogProviderCandidates(providerID) {
		if c.data.ModelsDev != nil {
			for _, row := range c.data.ModelsDev.Specialized[p] {
				add(row)
			}
		}
		for id, m := range c.data.Providers[p] {
			add(OutputMetadata{ID: id, Name: m.Name, Kind: m.Kind, Input: m.Input, Output: m.Output})
		}
	}
	slices.SortFunc(rows, func(a, b OutputMetadata) int { return strings.Compare(a.ID, b.ID) })
	return rows
}

// Compatible generic lists may use llm/model as a resource label. Only a
// known specialized category is evidence that a model is not for conversation.
func ConversationCategory(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "chat", "llm":
		return "chat"
	case "image", "video", "audio", "speech", "transcription", "realtime", "decision", "embedding", "rerank", "reranking", "extraction", "image_generation", "video_generation":
		return kind
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
			row := OutputMetadata{ID: id, Name: m.Name, Kind: m.Kind, Input: m.Input, Output: m.Output}
			if outputKind(row) == kind {
				return row, true
			}
		}
	}
	return OutputMetadata{}, false
}
