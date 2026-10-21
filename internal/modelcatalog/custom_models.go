package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"vista/internal/provider"
)

// DiscoverCustomModels uses the explicitly selected API format and the same
// authentication and headers as inference. Inventory hints never change routing.
func DiscoverCustomModels(parent context.Context, source Configured) ([]Configured, error) {
	if !provider.IsCustomProtocol(source.Provider) || !ValidCustomProviderBaseURL(source.BaseURL) {
		return nil, errors.New("choose a conversation API format and enter an HTTP or HTTPS service address")
	}
	if err := provider.ValidateCustomAuthentication(source.Authentication, source.APIKey); err != nil {
		return nil, err
	}
	if err := provider.ValidateCustomHeaders(source.Headers); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	base := provider.CustomBaseURL(source.BaseURL, source.Provider)
	if source.Provider == "cohere" && strings.HasSuffix(base, "/v2") {
		// Cohere Chat uses v2, but its model inventory remains a v1 endpoint.
		base = strings.TrimSuffix(base, "/v2") + "/v1"
	}
	endpoint := base + "/models"
	if source.Provider == "cohere" {
		endpoint += "?endpoint=chat"
	}
	headers := http.Header{"Accept": []string{"application/json"}}
	if source.Provider == "anthropic" {
		headers.Set("anthropic-version", "2023-06-01")
	}
	provider.ApplyCustomRequestHeaders(headers, source.Authentication, source.APIKey, source.Headers)
	return discoverCustomPages(ctx, source, endpoint, headers)
}

func discoverCustomPages(ctx context.Context, source Configured, endpoint string, headers http.Header) ([]Configured, error) {
	var models []Configured
	seenIDs, seenPages := map[string]bool{}, map[string]bool{}
	for {
		if seenPages[endpoint] {
			return nil, errors.New("model list pagination repeated a page")
		}
		seenPages[endpoint] = true
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, errors.New("could not request model list")
		}
		request.Header = headers.Clone()
		response, err := customModelDiscoveryClient.Do(request)
		if err != nil {
			return nil, errors.New("model list is unavailable")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, &provider.ModelHTTPError{Provider: "model list", StatusCode: response.StatusCode}
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		response.Body.Close()
		if err != nil || len(data) > 4<<20 {
			return nil, errors.New("model list response exceeds 4 MiB or could not be read")
		}
		type entry struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Endpoints []string `json:"endpoints"`
			Methods   []string `json:"supportedGenerationMethods"`
		}
		var page struct {
			Data                []entry `json:"data"`
			Models              []entry `json:"models"`
			HasMore             bool    `json:"has_more"`
			LastID              string  `json:"last_id"`
			NextPageToken       string  `json:"nextPageToken"`
			CohereNextPageToken string  `json:"next_page_token"`
		}
		if json.Unmarshal(data, &page) != nil || page.Data == nil && page.Models == nil {
			return nil, errors.New("model list has an unsupported format")
		}
		entries := page.Data
		if page.Models != nil {
			entries = page.Models
		}
		for _, row := range entries {
			id := strings.TrimSpace(row.ID)
			if id == "" && page.Models != nil {
				id = strings.TrimSpace(row.Name)
				if source.Provider == "gemini" {
					id = strings.TrimPrefix(id, "models/")
				}
			}
			if id == "" || len(id) > 200 || seenIDs[id] || len(row.Methods) > 0 && !slices.Contains(row.Methods, "generateContent") && !slices.Contains(row.Methods, "streamGenerateContent") || source.Provider == "cohere" && len(row.Endpoints) > 0 && !slices.Contains(row.Endpoints, "chat") {
				continue
			}
			seenIDs[id] = true
			model := source
			model.ID = id
			models = append(models, model)
		}
		if len(seenIDs) > 4096 || len(seenPages) > 64 {
			return nil, errors.New("model list exceeds discovery limits")
		}
		if !page.HasMore && page.NextPageToken == "" && page.CohereNextPageToken == "" {
			break
		}
		next, _ := url.Parse(endpoint)
		query := next.Query()
		if page.CohereNextPageToken != "" {
			query.Set("page_token", page.CohereNextPageToken)
		} else if page.NextPageToken != "" {
			query.Set("pageToken", page.NextPageToken)
		} else {
			if page.LastID == "" {
				return nil, errors.New("model list has no pagination cursor")
			}
			query.Set("after_id", page.LastID)
		}
		next.RawQuery = query.Encode()
		endpoint = next.String()
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
