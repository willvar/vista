package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestAgentWebSearchFallsBackWithinOneToolCall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var failedRequests, searches atomic.Int32
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failedRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		searches.Add(1)
		fmt.Fprint(w, `{"results":[{"url":"https://example.com/source","title":"Verified source"}]}`)
	}))
	defer tavily.Close()
	var modelRequests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.ChatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if modelRequests.Add(1) == 1 {
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"Checking sources.","tool_calls":[{"index":0,"id":"search","type":"function","function":{"name":"websearch","arguments":"{\"query\":\"vista\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		last := body.Messages[len(body.Messages)-1]
		var result tools.SearchResults
		if last.Role != "tool" || json.Unmarshal([]byte(last.Content), &result) != nil || result.Provider != "tavily" || len(result.Results) != 1 {
			t.Errorf("fallback result not delivered to model: %+v", last)
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"Answer with verified source."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	command := workerproto.Command{
		RunID: "search-fallback", Prompt: "Search for sources",
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: model.URL, APIKey: "fixture"},
		SearchProviders: []workerproto.SearchProvider{
			{Name: "exa", Endpoint: unavailable.URL}, {Name: "tavily", Endpoint: tavily.URL},
		},
	}
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		events = append(events, event)
		return nil
	})
	if err != nil || answer != "Answer with verified source." || modelRequests.Load() != 2 || failedRequests.Load() != 1 || searches.Load() != 1 {
		t.Fatalf("fallback execution: answer=%q error=%v model=%d failed=%d searches=%d", answer, err, modelRequests.Load(), failedRequests.Load(), searches.Load())
	}
	completedTools := 0
	text := map[int32]string{}
	for _, event := range events {
		if event.Kind == "model_delta" {
			text[event.Turn] += event.Content
		}
		if event.Kind == "tool" && event.Status != "started" {
			completedTools++
			if event.Status != "succeeded" {
				t.Fatalf("recoverable provider failure escaped as tool failure: %+v", event)
			}
		}
	}
	if completedTools != 1 || text[1] != "Checking sources." || text[2] != answer {
		t.Fatalf("progress/final output or tool boundary changed: tools=%d text=%+v", completedTools, text)
	}
}

func TestWebSearchSummarySettingPreservesFullResultsForRangeReads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int
		content string
		count   int
	}{
		{"character limit", 3, "中文🙂evidence beyond the preview", 2},
		{"default length", 1000, strings.Repeat("证据🙂café\n", 1400), 8},
		{"large character limit", 20000, strings.Repeat("证据🙂café\n", 4000), 8},
		{"line limit", 5000, strings.Repeat("x\n", 3000), 8},
		{"escaped content", 20000, strings.Repeat("<\x00🙂\\\"", 5000), 8},
		{"unlimited short", 0, "中文🙂 complete summary", 2},
		{"unlimited large", 0, strings.Repeat("证据🙂café\n", 1400), 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, emit := outputToolStore(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				hits := make([]map[string]string, tc.count)
				for i := range hits {
					hits[i] = map[string]string{"url": fmt.Sprintf("https://example.com/%d", i), "summary": tc.content}
				}
				json.NewEncoder(w).Encode(map[string]any{"results": hits})
			}))
			defer server.Close()
			parent := workerproto.Command{SessionID: "session", RunID: "run", SearchSummaryLength: tc.limit, SearchProviders: []workerproto.SearchProvider{{Name: "deepseek", Endpoint: server.URL, APIKey: "search-key"}}}
			call := provider.ToolCall{ID: "search"}
			call.Function.Name, call.Function.Arguments = "websearch", `{"query":"sources"}`
			runtime := &toolRuntime{parent: parent, emit: emit}
			output, err := runtime.execute(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			var preview struct {
				tools.SearchResults
				ArtifactID string `json:"artifactId"`
				Hint       string `json:"hint"`
			}
			if json.Unmarshal([]byte(output), &preview) != nil || len(preview.Results) != tc.count {
				t.Fatal("search dropped results")
			}
			want := tc.content
			if tc.limit > 0 {
				want = string([]rune(want)[:tc.limit])
			}
			for _, hit := range preview.Results {
				if hit.Content != want || !utf8.ValidString(hit.Content) {
					t.Fatal("preview did not honor the Unicode character limit")
				}
			}
			if tc.limit > 0 && (!preview.Truncated || preview.ArtifactID == "" || !strings.Contains(preview.Hint, "read_tool_output")) {
				t.Fatal("truncated summaries lack a readable full result")
			}
			bounded, err := boundToolOutput(context.Background(), "run", call.ID, call.Function.Name, output, resultOutputRef(output), emit)
			if err != nil || bounded.model != output {
				t.Fatalf("tool pipeline overrode the user's character setting: %v", err)
			}
			if tc.name == "unlimited short" {
				if bounded.model != output || preview.Truncated {
					t.Fatal("unlimited changed a short summary")
				}
				return
			}
			if bounded.reference == nil || bounded.reference.ArtifactID == "" {
				t.Fatal("full result has no artifact")
			}
			artifact, err := db.GetArtifact(context.Background(), "session", bounded.reference.ArtifactID)
			if err != nil || !artifact.Complete {
				t.Fatalf("artifact: %v", err)
			}
			full, err := os.ReadFile(artifact.OutputPath)
			var complete tools.SearchResults
			if err != nil || json.Unmarshal(full, &complete) != nil || len(complete.Results) != tc.count {
				t.Fatalf("saved result: %v", err)
			}
			for _, hit := range complete.Results {
				if hit.Content != tc.content {
					t.Fatal("saved summary was truncated")
				}
			}
			server.Close()
			var reconstructed strings.Builder
			for offset := int64(0); offset < int64(len(full)); {
				page, err := readToolOutput(context.Background(), fmt.Sprintf(`{"artifactId":%q,"offset":%d,"limit":32768}`, artifact.ID, offset), parent)
				var result tools.OutputPage
				if err != nil || json.Unmarshal([]byte(page), &result) != nil || result.NextOffset <= offset {
					t.Fatalf("read saved result: %v", err)
				}
				reconstructed.WriteString(result.Content)
				offset = result.NextOffset
			}
			if reconstructed.String() != string(full) || requests.Load() != 1 {
				t.Fatal("continuation lost content or repeated the search")
			}
		})
	}
}
