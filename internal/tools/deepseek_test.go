package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/workerproto"

	"vista/internal/useragent"
)

func TestDeepSeekSearchUsesModelSummariesAndPreservesUnicode(t *testing.T) {
	wantAgent, agentErr := useragent.Current()
	if agentErr != nil {
		t.Fatal(agentErr)
	}
	query := strings.Repeat("问🙂", 64)
	summary := strings.Repeat("完整摘要🙂", 1200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.UserAgent() != wantAgent {
			t.Errorf("sent UA %q, want %q", r.UserAgent(), wantAgent)
		}
		var body map[string]any
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer search-key" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("incorrect request or authentication")
		}
		if body["query"] != query || body["count"] != float64(8) || body["need_content"] != false || len(body) != 3 {
			t.Errorf("search request: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"url": "https://example.com/summary", "title": "Summary", "summary": summary, "snippet": "UI snippet", "content": "Page content is unused", "date": "2026-10-18T00:00:00Z"},
			{"url": "https://example.com/snippet", "summary": "  ", "snippet": "Fallback excerpt", "date": nil},
			{"url": "file:///private", "summary": "invalid URL"},
		}})
	}))
	defer server.Close()
	providers := []workerproto.SearchProvider{{Name: "deepseek", Endpoint: server.URL, APIKey: "search-key"}}
	result, err := WebSearch(context.Background(), providers, "deepseek", query)
	if err != nil || result.Provider != "deepseek" || len(result.Results) != 2 {
		t.Fatalf("search: %+v, %v", result, err)
	}
	if result.Results[0].Content != summary || result.Results[1].Content != "Fallback excerpt" || result.Results[0].Published != "2026-10-18T00:00:00Z" {
		t.Fatal("summary, fallback, or publication date was lost")
	}
}

func TestDeepSeekSearchSkipsMissingKeyAndRejectsLongQueries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("key leaked to fallback")
		}
		io.WriteString(w, `{"results":[{"url":"https://example.com/fallback"}]}`)
	}))
	defer server.Close()
	providers := []workerproto.SearchProvider{{Name: "deepseek", Endpoint: server.URL}, {Name: "tavily", Endpoint: server.URL}}
	result, err := WebSearch(context.Background(), providers, "auto", "query")
	if err != nil || result.Provider != "tavily" || calls.Load() != 1 {
		t.Fatalf("missing key fallback: %+v %v", result, err)
	}
	if _, err := WebSearch(context.Background(), providers, "deepseek", "query"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing key: %v", err)
	}
	providers[0].APIKey = "search-key"
	if _, err := WebSearch(context.Background(), providers, "deepseek", strings.Repeat("问", 129)); err == nil || !strings.Contains(err.Error(), "128 Unicode") {
		t.Fatalf("long query: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid query or missing key was sent to DeepSeek")
	}
}

func TestDeepSeekSearchFailuresUseExistingFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"auth", 401, "reflected search+key"}, {"funds", 402, "insufficient balance"}, {"forbidden", 403, "policy"},
		{"rate limit", 429, "limited"}, {"timeout", 504, "timeout"}, {"invalid JSON", 200, "not JSON"},
		{"missing results", 200, `{}`}, {"null results", 200, `{"results":null}`}, {"empty results", 200, `{"results":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fallbacks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/fallback" {
					fallbacks.Add(1)
					io.WriteString(w, `{"results":[{"url":"https://example.com/fallback"}]}`)
					return
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			providers := []workerproto.SearchProvider{{Name: "deepseek", Endpoint: server.URL, APIKey: "search+key"}, {Name: "tavily", Endpoint: server.URL + "/fallback"}}
			result, err := WebSearch(context.Background(), providers, "auto", "query")
			if err != nil || result.Provider != "tavily" || fallbacks.Load() != 1 {
				t.Fatalf("fallback: %+v %v", result, err)
			}
			if _, err := WebSearch(context.Background(), providers, "deepseek", "query"); err == nil || strings.Contains(err.Error(), "search+key") {
				t.Fatalf("explicit failure: %v", err)
			}
			if fallbacks.Load() != 1 {
				t.Fatal("explicit selection fell back")
			}
		})
	}
}

func TestDeepSeekFetchSavesOnlyCompleteMarkdown(t *testing.T) {
	full := "# Article\n\n" + strings.Repeat("完整正文🙂\n", 18000) + "FINAL EVIDENCE"
	client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
		var body map[string]string
		if req.URL.String() != deepSeekFetchEndpoint || req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer fetch-key" || json.NewDecoder(req.Body).Decode(&body) != nil || body["url"] != "https://93.184.216.34/article" || len(body) != 1 {
			t.Fatal("incorrect fetch request or credentials")
		}
		encoded, _ := json.Marshal(map[string]string{"title": "Article", "content": full})
		return webfetchResponseFixture(req, 200, "application/json", string(encoded)), nil
	})}
	var output strings.Builder
	page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "text", &output, client, workerproto.WebfetchConfig{Order: []string{"deepseek", "vista", "jina"}, DeepSeekAPIKey: "fetch-key", JinaAPIKey: "other-key"})
	if err != nil || page.Reader != "deepseek" || page.Format != "markdown" || page.URL != "https://93.184.216.34/article" || output.String() != full {
		t.Fatalf("fetch: page=%+v bytes=%d err=%v", page, output.Len(), err)
	}
}

func TestDeepSeekFetchFailuresFallBackWithoutPollutingOutput(t *testing.T) {
	for _, body := range []string{"invalid JSON", `{}`, `{"content":null}`, `{"content":" "}`, `{"content":"unfinished`} {
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Hostname() == "api.deepseek.com" {
				return webfetchResponseFixture(req, 200, "application/json", body), nil
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("fetch key leaked to origin")
			}
			return webfetchResponseFixture(req, 200, "text/plain", "origin evidence"), nil
		})}
		var output strings.Builder
		page, err := webfetchWithClient(context.Background(), "https://93.184.216.34/article", "text", &output, client, workerproto.WebfetchConfig{Order: []string{"deepseek", "vista", "jina"}, DeepSeekAPIKey: "fetch-key"})
		if err != nil || page.Reader != "vista" || output.String() != "origin evidence" {
			t.Fatalf("fallback: %+v %q %v", page, output.String(), err)
		}
	}
}

func TestDeepSeekFetchSkippedWithoutKeyOrForPrivateTargets(t *testing.T) {
	for _, tc := range []struct{ target, key string }{
		{"https://93.184.216.34/article", ""}, {"http://127.0.0.1/private", "fetch-key"},
		{"https://93.184.216.34/article?token=secret", "fetch-key"},
	} {
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Hostname() == "api.deepseek.com" {
				t.Fatal("unconfigured reader or private target was sent")
			}
			return webfetchResponseFixture(req, 200, "text/plain", "local evidence"), nil
		})}
		var output strings.Builder
		page, err := webfetchWithClient(context.Background(), tc.target, "text", &output, client, workerproto.WebfetchConfig{Order: []string{"deepseek", "vista", "jina"}, DeepSeekAPIKey: tc.key})
		if err != nil || page.Reader != "vista" || output.String() != "local evidence" {
			t.Fatalf("skip: %+v %v", page, err)
		}
	}
}

func TestDeepSeekFetchCancellationAndRedactedErrors(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := &http.Client{Transport: webfetchTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if cancelRequest {
				cancel()
				return nil, ctx.Err()
			}
			return webfetchResponseFixture(req, 403, "text/plain", "reflected fetch+key fetch%2Bkey"), nil
		})}
		_, err := webfetchWithClient(ctx, "https://93.184.216.34/article", "markdown", io.Discard, client, workerproto.WebfetchConfig{Order: []string{"deepseek", "vista", "jina"}, DeepSeekAPIKey: "fetch+key"})
		if cancelRequest {
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("cancel: %v, calls=%d", err, calls)
			}
		} else if err == nil || strings.Contains(err.Error(), "fetch+key") || strings.Contains(err.Error(), "fetch%2Bkey") || !strings.Contains(err.Error(), "DeepSeek HTTP status 403") {
			t.Fatalf("failure lost status or leaked credential: %v", err)
		}
	}
}
