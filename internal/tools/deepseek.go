package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"vista/internal/mcpclient"
	"vista/internal/useragent"
	"vista/internal/workerproto"
)

const deepSeekFetchEndpoint = "https://api.deepseek.com/v1/tools/fetch"

// Both retrieval APIs use direct JSON requests, independently of the chat model.
func deepSeekRequest(ctx context.Context, client *http.Client, endpoint, key string, payload any) (*http.Response, error) {
	if key == "" {
		return nil, errors.New("DeepSeek API key is not configured")
	}
	if len(key) > 4096 {
		return nil, errors.New("DeepSeek API key exceeds its size limit")
	}
	if err := mcpclient.ValidateConfig(workerproto.MCPServer{ID: "deepseek", Name: "deepseek", Transport: "http", Endpoint: endpoint}); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	apiClient := *client
	apiClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("DeepSeek endpoint redirected") }
	response, err := useragent.Do(&apiClient, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return nil, fmt.Errorf("DeepSeek HTTP status %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	return response, nil
}

func searchDeepSeek(ctx context.Context, config workerproto.SearchProvider, query string) ([]SearchHit, error) {
	if utf8.RuneCountInString(query) > 128 {
		return nil, errors.New("DeepSeek search query must contain 1–128 Unicode characters; shorten the query")
	}
	response, err := deepSeekRequest(ctx, &http.Client{Timeout: 25 * time.Second}, config.Endpoint, config.APIKey, map[string]any{
		"query": query, "count": 8, "need_content": false,
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Results == nil {
		return nil, errors.New("invalid DeepSeek search response")
	}
	hits := make([]SearchHit, 0, len(result.Results))
	for _, item := range result.Results {
		content := item.Summary
		if strings.TrimSpace(content) == "" {
			content = item.Snippet
		}
		hits = append(hits, SearchHit{URL: item.URL, Title: item.Title, Content: content, Published: item.Date})
	}
	return boundedHits(hits), nil
}

func webfetchDeepSeekResponse(ctx context.Context, client *http.Client, rawURL string, settings workerproto.WebfetchConfig) (*http.Response, error) {
	response, err := deepSeekRequest(ctx, client, deepSeekFetchEndpoint, settings.DeepSeekAPIKey, map[string]string{"url": rawURL})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Content *string `json:"content"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Content == nil || strings.TrimSpace(*result.Content) == "" {
		return nil, errors.New("invalid or empty DeepSeek fetch response")
	}
	// The provider returns a JSON envelope. The artifact contains only the page
	// Markdown, so existing byte offsets continue to address the saved document.
	response.Body = io.NopCloser(strings.NewReader(*result.Content))
	response.Header = http.Header{"Content-Type": []string{"text/markdown; charset=utf-8"}}
	response.ContentLength = int64(len(*result.Content))
	return response, nil
}
