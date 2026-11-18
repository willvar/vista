package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"vista/internal/codexauth"
	modelcfg "vista/internal/model"
	"vista/internal/network"
)

// ProcessingInventoryKey identifies the authenticated catalog query. Codex
// returns its whole account catalog; a gateway query is scoped to one model.
func ProcessingInventoryKey(m modelcfg.Model) string {
	if m.Custom {
		return ""
	}
	switch m.ProviderID {
	case "openai-codex":
		if m.CodexCredentialPath != "" && m.ConnectionID != "" && (m.BaseURL == codexBaseURL || m.BaseURL == "") {
			return "codex:" + m.ConnectionID
		}
	case "openrouter":
		if m.APIKey != "" && strings.TrimRight(m.BaseURL, "/") == "https://openrouter.ai/api/v1" {
			return "openrouter:" + m.Key
		}
	case "cerebras", "groq", "mistral":
		if nativeTierCatalogURL(m) != "" {
			return m.ProviderID + ":" + m.UsageConnection()
		}

	}
	return ""
}

var processingInventoryClient = network.NewClient(http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("processing inventory redirected") }}, network.ConnectionScope)

func DiscoverProcessingTiers(ctx context.Context, m modelcfg.Model) (map[string][]string, error) {
	if ProcessingInventoryKey(m) == "" {
		return nil, nil
	}
	ctx, m, err := m.BindNetwork(ctx)
	if err != nil {
		return nil, err
	}
	switch m.ProviderID {
	case "openai-codex":
		auth, err := codexauth.NewClient().Token(ctx, m.CodexCredentialPath, "")
		if err != nil {
			return nil, err
		}
		m.APIKey = auth.AccessToken
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, codexBaseURL+"/codex/models?client_version="+url.QueryEscape(codexClientVersion), nil)
		if err != nil {
			return nil, err
		}
		setCodexHeaders(request, m)
		request.Header.Set("chatgpt-account-id", auth.AccountID)
		var catalog struct {
			Models []struct {
				Slug  string            `json:"slug"`
				Tiers []json.RawMessage `json:"service_tiers"`
			} `json:"models"`
		}
		if err = readProcessingInventory(request, m.APIKey, &catalog); err != nil {
			return nil, err
		}
		result := make(map[string][]string, len(catalog.Models))
		for _, row := range catalog.Models {
			if row.Slug != "" && len(row.Slug) <= 512 {
				result[row.Slug] = parseAdvertisedTiers(row.Tiers)
			}
		}
		return result, nil
	case "openrouter":
		id := requestModelID(m)
		author, slug, ok := strings.Cut(id, "/")
		if !ok || author == "" || slug == "" || strings.ContainsAny(author+slug, "/\\:?#") {
			return nil, nil
		}
		if m.OpenRouter != nil && (len(m.OpenRouter.Models) > 0 || len(m.OpenRouter.APIKeys) > 0) {
			return nil, nil
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models/"+url.PathEscape(author)+"/"+url.PathEscape(slug)+"/endpoints", nil)
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Data struct {
				Endpoints []routingTierEndpoint `json:"endpoints"`
			} `json:"data"`
		}
		if err = readProcessingInventory(request, m.APIKey, &catalog); err != nil {
			return nil, err
		}
		var routing *modelcfg.OpenRouterRouting
		if m.OpenRouter != nil {
			routing = m.OpenRouter.Provider
		}
		return map[string][]string{id: routingTiers(catalog.Data.Endpoints, routing)}, nil
	}
	if endpoint := nativeTierCatalogURL(m); endpoint != "" {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Data []struct {
				ID    string            `json:"id"`
				Tiers []json.RawMessage `json:"service_tiers"`
			} `json:"data"`
		}
		if err = readProcessingInventory(request, m.APIKey, &catalog); err != nil {
			return nil, err
		}
		result := make(map[string][]string)
		for _, row := range catalog.Data {
			if row.ID != "" && len(row.ID) <= 512 {
				if tiers := parseAdvertisedTiers(row.Tiers); len(tiers) > 0 {
					result[row.ID] = tiers
				}
			}
		}
		return result, nil
	}

	return nil, nil
}

// Native model lists can prove gated tiers only when they explicitly advertise
// them. An ordinary models response or a dedicated URL alone proves nothing.
func nativeTierCatalogURL(m modelcfg.Model) string {
	if m.APIKey == "" || m.Custom || m.UsageConnection() == "" {
		return ""
	}
	base := strings.TrimRight(m.BaseURL, "/")
	switch m.ProviderID {
	case "cerebras":
		if m.Provider == "openai-chat" && base == "https://api.cerebras.ai/v1" {
			return base + "/models"
		}
	case "groq":
		if m.Provider == "openai-chat" && base == "https://api.groq.com/openai/v1" {
			return base + "/models"
		}
	case "mistral":
		if m.Provider == "mistral" && (base == "" || base == "https://api.mistral.ai/v1") {
			return "https://api.mistral.ai/v1/models"
		}
	}
	return ""
}

func readProcessingInventory(request *http.Request, key string, value any) error {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := network.Do(processingInventoryClient, request)
	if err != nil {
		return errors.New("could not read provider processing tiers")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("provider processing inventory returned HTTP %d", response.StatusCode)
	}
	const limit = 2 << 20
	blob, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(blob) > limit || json.Unmarshal(blob, value) != nil {
		return errors.New("invalid provider processing inventory")
	}
	return nil
}

func parseAdvertisedTiers(rows []json.RawMessage) []string {
	var result []string
	for _, row := range rows {
		var id string
		if json.Unmarshal(row, &id) != nil {
			var obj struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(row, &obj) != nil {
				continue
			}
			id = obj.ID
		}
		if slices.Contains([]string{"priority", "fast", "flex", "ultrafast", "performance"}, id) && !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	return result
}

type routingTierEndpoint struct {
	Tag          string `json:"tag"`
	Tier         string `json:"service_tier"`
	Quantization string `json:"quantization"`
}

func routingTiers(endpoints []routingTierEndpoint, routing *modelcfg.OpenRouterRouting) []string {
	if routing != nil && (routing.MaxPrice != nil || routing.ZDR != nil && *routing.ZDR || routing.RequireParameters != nil && *routing.RequireParameters || routing.DataCollection == "deny") {
		// The catalog does not prove eligibility under these additional filters.
		return nil
	}
	var result []string
	for _, e := range endpoints {
		if e.Tag == "" || !slices.Contains([]string{"priority", "flex", "ultrafast"}, e.Tier) {
			continue
		}
		if routing != nil {
			canonical := func(tag string) string {
				if root, ok := strings.CutSuffix(tag, "/priority"); ok {
					return root + "/fast"
				}
				return tag
			}
			covers := func(tag string) bool {
				tag = canonical(tag)
				normalized := canonical(e.Tag)
				return normalized == tag || strings.HasPrefix(normalized, tag+"/")
			}

			if (len(routing.Only) > 0 && !slices.ContainsFunc(routing.Only, covers)) || slices.ContainsFunc(routing.Ignore, covers) || (len(routing.Quantizations) > 0 && !slices.Contains(routing.Quantizations, e.Quantization)) {
				continue
			}
		}
		if !slices.Contains(result, e.Tier) {
			result = append(result, e.Tier)
		}
	}
	return result
}
