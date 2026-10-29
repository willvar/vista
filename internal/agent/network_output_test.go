package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestNetworkInitialOutputReachesModelWithoutFixedTruncation(t *testing.T) {
	content := strings.Repeat("证据🙂\n<\"\\\x00", 8000) + "FINAL EVIDENCE"
	for _, name := range []string{"websearch", "webfetch"} {
		for _, limit := range []int{90000, 0} {
			t.Run(fmt.Sprintf("%s/limit=%d", name, limit), func(t *testing.T) {
				_, record := outputToolStore(t)
				want := content
				if limit > 0 {
					want = textPrefix(content, limit)
				}
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if name == "websearch" {
						json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]string{"url": "https://example.com/evidence", "summary": content}}})
					} else {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						fmt.Fprint(w, content)
					}
				}))
				defer source.Close()
				arguments := `{"query":"evidence"}`
				if name == "webfetch" {
					routeReaderFixture(t, source.URL)
					arguments = `{"url":"https://93.184.216.34/fixture"}`
				}
				var requests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Messages []provider.ChatMessage `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if requests.Add(1) == 1 {
						continuationEvent(w, mcpToolEvent("network-output", name, arguments))
						return
					}
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || last.ToolCallID != "network-output" || !exceedsToolBudget(last.Content) {
						t.Error("expected a complete network result beyond the ordinary tool budget")
					}
					if name == "websearch" {
						var result tools.SearchResults
						if json.Unmarshal([]byte(last.Content), &result) != nil || len(result.Results) != 1 || !strings.HasPrefix(content, result.Results[0].Content) || (limit == 0 && result.Results[0].Content != content) || result.Truncated != (limit > 0) {
							t.Error("model received a search result shortened beyond the user's setting")
						}
					} else {
						var result webfetchResult
						if json.Unmarshal([]byte(last.Content), &result) != nil || result.Content != want || result.NextOffset != len(want) || result.Truncated != (limit > 0) || !result.Complete {
							t.Error("model received page content shortened beyond the user's setting")
						}
					}
					continuationAnswer(w, "verified")
				}))
				defer upstream.Close()
				command := workerproto.Command{
					SessionID: "session", RunID: "run", Prompt: "Check the evidence",
					Model:              modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"},
					SearchPreviewBytes: limit,
					SearchProviders:    []workerproto.SearchProvider{{Name: "deepseek", Endpoint: source.URL, APIKey: "fixture"}},
					Webfetch:           workerproto.WebfetchConfig{PreviewBytes: limit},
				}
				completed := 0
				answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
					if event.Kind == "tool" && event.Status != "started" {
						completed++
						encoded, _ := json.Marshal(event)
						if event.Status != "succeeded" || event.Tool.OutputRef == nil || len(encoded) > 32<<10 {
							t.Error("network result lost its saved output or exceeded the event transport budget")
						}
					}
					return record(event)
				})
				if err != nil || answer != "verified" || requests.Load() != 2 || completed != 1 {
					t.Fatalf("network output flow: answer=%q requests=%d tools=%d error=%v", answer, requests.Load(), completed, err)
				}
			})
		}
	}
}
