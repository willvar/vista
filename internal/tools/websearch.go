package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"vista/internal/mcpclient"
	"vista/internal/useragent"
	"vista/internal/workerproto"
)

type SearchHit struct {
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	Published string `json:"published,omitempty"`
}

type SearchResults struct {
	Provider  string      `json:"provider"`
	Results   []SearchHit `json:"results"`
	Truncated bool        `json:"truncated,omitempty"`
}

func WebSearch(ctx context.Context, providers []workerproto.SearchProvider, requested, query string) (SearchResults, error) {
	query = strings.TrimSpace(query)
	if len(query) == 0 || len(query) > 512 || !utf8.ValidString(query) {
		return SearchResults{}, errors.New("websearch query must be 1–512 UTF-8 bytes")
	}
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "exa" && requested != "firecrawl" && requested != "parallel" && requested != "tavily" && requested != "deepseek" {
		return SearchResults{}, errors.New("unknown websearch provider")
	}
	var failures []error
	for _, config := range providers {
		if requested != "auto" && requested != config.Name {
			continue
		}
		if err := ctx.Err(); err != nil {
			return SearchResults{}, err
		}
		if config.Name == "deepseek" && config.APIKey == "" {
			if requested != "auto" {
				return SearchResults{}, errors.New("DeepSeek search API key is not configured")
			}
			continue
		}
		var result SearchResults
		var err error
		result.Results, err = searchOne(ctx, config, query)
		if ctx.Err() != nil {
			return SearchResults{}, ctx.Err()
		}
		if err == nil && len(result.Results) == 0 {
			err = errors.New("no usable search results")
		}
		if err == nil {
			result.Provider = config.Name
			return result, nil
		}
		err = fmt.Errorf("%s websearch: %w", config.Name, sanitizedSearchError(err, config.APIKey))
		if requested != "auto" {
			return SearchResults{}, err
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return SearchResults{}, errors.New("no configured websearch provider")
	}
	return SearchResults{}, fmt.Errorf("all websearch providers failed: %w", errors.Join(failures...))
}

func searchOne(ctx context.Context, config workerproto.SearchProvider, query string) ([]SearchHit, error) {
	if len(config.APIKey) > 4096 || len(config.Endpoint) > 2048 || config.Endpoint == "" {
		return nil, errors.New("websearch configuration exceeds limits or has no endpoint")
	}
	if err := mcpclient.ValidateConfig(workerproto.MCPServer{ID: "search", Name: "search", Transport: "http", Endpoint: config.Endpoint}); err != nil {
		return nil, err
	}
	if config.Name == "tavily" {
		return searchTavily(ctx, config, query)
	}
	if config.Name == "deepseek" {
		return searchDeepSeek(ctx, config, query)
	}
	if config.Name != "exa" && config.Name != "firecrawl" && config.Name != "parallel" {
		return nil, errors.New("unknown websearch provider")
	}
	connection, _ := mcpclient.SearchConfig(config.Name, config.Endpoint, config.APIKey)
	server, err := mcpclient.OpenSearch(ctx, connection.Endpoint, connection.Token)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	var name string
	var args any
	switch config.Name {
	case "exa":
		name, args = "web_search_exa", map[string]any{"query": query, "numResults": 8}
	case "firecrawl":
		name, args = "firecrawl_search", map[string]any{"query": query, "limit": 8}
	case "parallel":
		name, args = "web_search", map[string]any{"objective": query, "search_queries": []string{query}}
	}
	encoded, _ := json.Marshal(args)
	result, err := server.CallTool(ctx, name, encoded)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, errors.New("websearch MCP tool returned an error: " + result.Content)
	}
	switch config.Name {
	case "exa":
		hits := parseExaResults(result.Content)
		if len(hits) == 0 {
			return nil, errors.New("Exa returned no parseable search results")
		}
		return hits, nil
	case "firecrawl":
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				Web []struct {
					URL         string `json:"url"`
					Title       string `json:"title"`
					Description string `json:"description"`
				} `json:"web"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(result.Content), &response); err != nil || !response.Success {
			return nil, errors.New("invalid Firecrawl search response")
		}
		hits := make([]SearchHit, 0, len(response.Data.Web))
		for _, item := range response.Data.Web {
			hits = append(hits, SearchHit{URL: item.URL, Title: item.Title, Content: item.Description})
		}
		return boundedHits(hits), nil
	case "parallel":
		var response struct {
			Results []struct {
				URL         string   `json:"url"`
				Title       string   `json:"title"`
				PublishDate string   `json:"publish_date"`
				Excerpts    []string `json:"excerpts"`
			} `json:"results"`
		}
		encoded := []byte(result.Content)
		var err error
		if result.Structured != nil {
			encoded, err = json.Marshal(result.Structured)
		}
		if err != nil || json.Unmarshal(encoded, &response) != nil {
			return nil, errors.New("invalid Parallel search response")
		}
		hits := make([]SearchHit, 0, len(response.Results))
		for _, item := range response.Results {
			hits = append(hits, SearchHit{URL: item.URL, Title: item.Title, Content: strings.Join(item.Excerpts, "\n\n"), Published: item.PublishDate})
		}
		return boundedHits(hits), nil
	}
	return nil, errors.New("unreachable websearch provider")
}

func searchTavily(ctx context.Context, config workerproto.SearchProvider, query string) ([]SearchHit, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "search_depth": "basic", "chunks_per_source": 3, "max_results": 8})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if config.APIKey == "" {
		request.Header.Set("X-Tavily-Access-Mode", "keyless")
	} else {
		request.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("websearch endpoint redirected") }}
	response, err := useragent.Do(client, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	var decoded struct {
		Results []SearchHit `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, errors.New("invalid Tavily search response")
	}
	return boundedHits(decoded.Results), nil
}

func parseExaResults(content string) []SearchHit {
	var hits []SearchHit
	for _, block := range strings.Split(content, "\n\n---\n\n") {
		var hit SearchHit
		inContent := false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "URL:"):
				hit.URL = strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			case strings.HasPrefix(line, "Title:"):
				hit.Title = strings.TrimSpace(strings.TrimPrefix(line, "Title:"))
			case strings.HasPrefix(line, "Published:"):
				hit.Published = strings.TrimSpace(strings.TrimPrefix(line, "Published:"))
			case strings.HasPrefix(line, "Highlights:") || strings.HasPrefix(line, "Text:"):
				hit.Content = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				inContent = true
			default:
				if inContent {
					hit.Content += " " + strings.TrimSpace(line)
				}
			}
		}
		if hit.URL != "" {
			hits = append(hits, hit)
		}
	}
	return boundedHits(hits)
}

func sanitizedSearchError(err error, key string) error {
	message := err.Error()
	if key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
		message = strings.ReplaceAll(message, url.QueryEscape(key), "[redacted]")
	}
	return errors.New(limitedText(message, 512))
}

func boundedHits(hits []SearchHit) []SearchHit {
	out := make([]SearchHit, 0, min(len(hits), 8))
	for _, hit := range hits {
		if len(out) == 8 {
			break
		}
		// Grounded searches may return long redirect URLs. Keep complete URLs
		// within the same limit used by webfetch, never a broken prefix.
		parsed, err := url.Parse(hit.URL)
		if err != nil || len(hit.URL) > 2048 || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		hit.Title = limitedText(hit.Title, 100)
		hit.Published = limitedText(hit.Published, 64)
		out = append(out, hit)
	}
	return out
}

func limitedText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
