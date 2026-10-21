package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/workerproto"
)

func TestWebSearchMCPBackendsAndTavily(t *testing.T) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "search-fixture", Version: "1"}, nil)
	inputSchema := json.RawMessage(`{"type":"object"}`)
	mcpServer.AddTool(&mcp.Tool{Name: "web_search_exa", InputSchema: inputSchema}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Query      string `json:"query"`
			NumResults int    `json:"numResults"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil || input.Query != "vista" || input.NumResults != 8 {
			t.Errorf("Exa arguments: %+v %v", input, err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Title: Exa result\nURL: https://example.com/exa\nHighlights:\nExa excerpt\nPublished: 2026-01-01"}}}, nil
	})
	mcpServer.AddTool(&mcp.Tool{Name: "firecrawl_search", InputSchema: inputSchema}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil || input.Query != "vista" || input.Limit != 8 {
			t.Errorf("Firecrawl arguments: %+v %v", input, err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"success":true,"data":{"web":[{"url":"https://example.com/firecrawl","title":"Firecrawl result","description":"Firecrawl excerpt"}]}}`}}}, nil
	})
	mcpServer.AddTool(&mcp.Tool{Name: "web_search", InputSchema: inputSchema}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Objective     string   `json:"objective"`
			SearchQueries []string `json:"search_queries"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil || input.Objective != "vista" || len(input.SearchQueries) != 1 || input.SearchQueries[0] != "vista" {
			t.Errorf("Parallel arguments: %+v %v", input, err)
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{"results": []any{map[string]any{"url": "https://example.com/parallel", "title": "Parallel result", "excerpts": []string{"Parallel excerpt"}}}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantExaKey, wantAuth := "", ""
		if r.URL.Path == "/keyed/exa" {
			wantExaKey = "exa-secret"
		} else if strings.HasPrefix(r.URL.Path, "/keyed/") {
			wantAuth = "Bearer other-secret"
		}
		if r.URL.Query().Get("exaApiKey") != wantExaKey || r.Header.Get("Authorization") != wantAuth {
			t.Errorf("wrong credentials on MCP request %s", r.URL.Path)
		}
		handler.ServeHTTP(w, r)
	}))
	defer mcpHTTP.Close()
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth, wantMode := "", "keyless"
		if r.URL.Path == "/keyed" {
			wantAuth, wantMode = "Bearer tavily-secret", ""
		}
		if r.Header.Get("Authorization") != wantAuth || r.Header.Get("X-Tavily-Access-Mode") != wantMode {
			t.Error("wrong credentials on Tavily request")
		}
		var input struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Query != "vista" || input.MaxResults != 8 {
			t.Errorf("Tavily arguments: %+v %v", input, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"url":"https://example.com/tavily","title":"Tavily result","content":"Tavily excerpt"}]}`))
	}))
	defer tavily.Close()
	providers := []workerproto.SearchProvider{
		{Name: "exa", Endpoint: mcpHTTP.URL + "/keyed/exa", APIKey: "exa-secret"},
		{Name: "firecrawl", Endpoint: mcpHTTP.URL + "/keyed/firecrawl", APIKey: "other-secret"},
		{Name: "parallel", Endpoint: mcpHTTP.URL + "/keyed/parallel", APIKey: "other-secret"},
		{Name: "tavily", Endpoint: tavily.URL + "/keyed", APIKey: "tavily-secret"},
	}
	for _, config := range providers {
		name := config.Name
		result, err := WebSearch(context.Background(), providers, name, "vista")
		if err != nil || result.Provider != name || len(result.Results) != 1 || !strings.Contains(result.Results[0].URL, name) || !strings.Contains(strings.ToLower(result.Results[0].Content), name[:1]) {
			t.Errorf("%s search: result=%+v err=%v", name, result, err)
		}
		config.APIKey = ""
		config.Endpoint = strings.Replace(config.Endpoint, "/keyed", "/keyless", 1)
		result, err = WebSearch(context.Background(), []workerproto.SearchProvider{config}, "auto", "vista")
		if err != nil || result.Provider != name || len(result.Results) != 1 {
			t.Errorf("keyless %s was not available to auto: result=%+v err=%v", name, result, err)
		}
	}
}

func TestWebSearchPreservesLargeProviderSummaries(t *testing.T) {
	content := strings.Repeat("证据🙂evidence ", 30000) + "FINAL EVIDENCE"
	for _, name := range []string{"exa", "firecrawl", "parallel", "tavily", "deepseek"} {
		for _, asJSON := range []bool{true, false} {
			if !asJSON && (name == "tavily" || name == "deepseek") {
				continue // These providers use JSON HTTP rather than MCP.
			}
			t.Run(fmt.Sprintf("%s/JSON=%t", name, asJSON), func(t *testing.T) {
				var handler http.Handler
				if name == "tavily" || name == "deepseek" {
					field := "content"
					if name == "deepseek" {
						field = "summary"
					}
					handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]string{"url": "https://example.com/evidence", field: content}}})
					})
				} else {
					var toolName string
					var result mcp.CallToolResult
					switch name {
					case "exa":
						toolName = "web_search_exa"
						result.Content = []mcp.Content{&mcp.TextContent{Text: "URL: https://example.com/evidence\nText: " + content}}
					case "firecrawl":
						toolName = "firecrawl_search"
						encoded, _ := json.Marshal(map[string]any{"success": true, "data": map[string]any{"web": []any{map[string]string{"url": "https://example.com/evidence", "description": content}}}})
						result.Content = []mcp.Content{&mcp.TextContent{Text: string(encoded)}}
					case "parallel":
						toolName = "web_search"
						result.StructuredContent = map[string]any{"results": []any{map[string]any{"url": "https://example.com/evidence", "excerpts": []string{content}}}}
					}
					server := mcp.NewServer(&mcp.Implementation{Name: "search-fixture", Version: "1"}, nil)
					server.AddTool(&mcp.Tool{Name: toolName, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return &result, nil
					})
					handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: asJSON, Stateless: true})
				}
				upstream := httptest.NewServer(handler)
				defer upstream.Close()
				results, err := WebSearch(context.Background(), []workerproto.SearchProvider{{Name: name, Endpoint: upstream.URL, APIKey: "fixture"}}, name, "evidence")
				if err != nil || len(results.Results) != 1 || results.Results[0].Content != content || results.Truncated {
					t.Fatalf("large provider summary was rejected or shortened: %v", err)
				}
			})
		}
	}
}

func TestWebSearchAutoFallsBackOnProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		toolText string
		isError  bool
	}{
		{name: "rate limit", status: 429},
		{name: "authentication", status: 401},
		{name: "anonymous access denied", status: 403},
		{name: "provider outage", status: 503},
		{name: "invalid JSON", body: "invalid response"},
		{name: "unparseable Exa response", toolText: "A changed response format"},
		{name: "MCP tool error", toolText: "Search unavailable", isError: true},
		{name: "unusable results", toolText: "Title: Local result\nURL: file:///tmp/result"},
		{name: "connection failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := searchMCPFixture(t, "web_search_exa", &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: tc.toolText}}, IsError: tc.isError,
			})
			if tc.status != 0 || tc.body != "" {
				upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					_, _ = w.Write([]byte(tc.body))
				}))
				t.Cleanup(upstream.Close)
			} else if tc.name == "connection failure" {
				upstream.Close()
			}
			var fallbackCalls atomic.Int32
			tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackCalls.Add(1)
				_, _ = w.Write([]byte(`{"results":[{"url":"https://example.com/result","title":"Result","content":"Found"}]}`))
			}))
			defer tavily.Close()
			providers := []workerproto.SearchProvider{{Name: "exa", Endpoint: upstream.URL}, {Name: "tavily", Endpoint: tavily.URL}}
			for _, requested := range []string{"auto", ""} {
				result, err := WebSearch(context.Background(), providers, requested, "vista")
				if err != nil || result.Provider != "tavily" || len(result.Results) != 1 {
					t.Fatalf("fallback: result=%+v err=%v", result, err)
				}
			}
			if fallbackCalls.Load() != 2 {
				t.Fatalf("fallback calls=%d", fallbackCalls.Load())
			}
			if _, err := WebSearch(context.Background(), providers, "exa", "vista"); err == nil || !strings.Contains(err.Error(), "exa websearch:") {
				t.Fatalf("explicit provider error missing: %v", err)
			}
			if fallbackCalls.Load() != 2 {
				t.Fatal("explicit provider selection used another backend")
			}
		})
	}
}

func searchMCPFixture(t *testing.T, name string, result *mcp.CallToolResult) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "search-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}))
	t.Cleanup(httpServer.Close)
	return httpServer
}

func TestWebSearchParallelTextResults(t *testing.T) {
	server := searchMCPFixture(t, "web_search", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: `{"results":[{"url":"https://example.com/parallel","title":"Parallel result","excerpts":["An excerpt"]}]}`,
	}}})
	result, err := WebSearch(context.Background(), []workerproto.SearchProvider{{Name: "parallel", Endpoint: server.URL}}, "auto", "vista")
	if err != nil || result.Provider != "parallel" || len(result.Results) != 1 || result.Results[0].Content != "An excerpt" {
		t.Fatalf("text-only MCP result lost: %+v, %v", result, err)
	}
}

func TestWebSearchAutoPreservesAllFailures(t *testing.T) {
	key := "secret+search/key"
	exa := searchMCPFixture(t, "web_search_exa", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
		Text: "Search unavailable: " + key + " " + url.QueryEscape(key),
	}}})
	firecrawl := searchMCPFixture(t, "firecrawl_search", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: `{"success":false}`,
	}}})
	parallel := searchMCPFixture(t, "web_search", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: `{"results":[]}`,
	}}})
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer tavily.Close()
	providers := []workerproto.SearchProvider{
		{Name: "exa", Endpoint: exa.URL, APIKey: key},
		{Name: "firecrawl", Endpoint: firecrawl.URL},
		{Name: "parallel", Endpoint: parallel.URL},
		{Name: "tavily", Endpoint: tavily.URL},
	}
	_, err := WebSearch(context.Background(), providers, "auto", "vista")
	if err == nil {
		t.Fatal("all-provider failure reported success")
	}
	previous := -1
	for _, detail := range []string{"all websearch providers failed", "exa websearch: websearch MCP tool returned an error: Search unavailable: [redacted] [redacted]", "firecrawl websearch: invalid Firecrawl search response", "parallel websearch: no usable search results", "tavily websearch: HTTP status 503"} {
		index := strings.Index(err.Error(), detail)
		if index <= previous {
			t.Fatalf("missing or out-of-order error %q: %v", detail, err)
		}
		previous = index
	}
	if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), url.QueryEscape(key)) {
		t.Fatal("search credential was not redacted")
	}
}

func TestWebSearchAutoContinuesPastEmptyResults(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"url":"https://example.com/result"}]}`))
	}))
	defer upstream.Close()
	providers := []workerproto.SearchProvider{{Name: "tavily", Endpoint: upstream.URL}, {Name: "tavily", Endpoint: upstream.URL}}
	result, err := WebSearch(context.Background(), providers, "auto", "vista")
	if err != nil || len(result.Results) != 1 || calls.Load() != 2 {
		t.Fatalf("empty response stopped fallback: %+v, %v, calls=%d", result, err, calls.Load())
	}
}

func TestWebSearchAutoStopsOnContextCancellation(t *testing.T) {
	for _, mode := range []string{"before request", "during request", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if mode == "before request" {
				cancel()
			} else if mode == "deadline" {
				var expire context.CancelFunc
				ctx, expire = context.WithTimeout(ctx, 50*time.Millisecond)
				defer expire()
				want = context.DeadlineExceeded
			}
			var attempts, fallbacks atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				if mode == "during request" {
					cancel()
				}
				<-ctx.Done()
			}))
			defer upstream.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbacks.Add(1)
				w.WriteHeader(503)
			}))
			defer fallback.Close()
			providers := []workerproto.SearchProvider{{Name: "exa", Endpoint: upstream.URL}, {Name: "tavily", Endpoint: fallback.URL}}
			_, err := WebSearch(ctx, providers, "auto", "vista")
			if !errors.Is(err, want) || fallbacks.Load() != 0 || mode == "before request" && attempts.Load() != 0 {
				t.Fatalf("cancellation triggered another backend: err=%v attempts=%d fallbacks=%d", err, attempts.Load(), fallbacks.Load())
			}
		})
	}
}

func TestLiveKeylessWebSearch(t *testing.T) {
	if os.Getenv("VISTA_LIVE_WEBSEARCH") != "1" {
		t.Skip("set VISTA_LIVE_WEBSEARCH=1 for live vendor checks")
	}
	providers := []workerproto.SearchProvider{
		{Name: "exa", Endpoint: "https://mcp.exa.ai/mcp"},
		{Name: "parallel", Endpoint: "https://search.parallel.ai/mcp"},
		{Name: "tavily", Endpoint: "https://api.tavily.com/search"},
	}
	for _, name := range []string{"exa", "parallel", "tavily"} {
		result, err := WebSearch(context.Background(), providers, name, "Go programming language")
		if err != nil || len(result.Results) == 0 {
			t.Errorf("live %s: result=%+v err=%v", name, result, err)
		}
	}
}
