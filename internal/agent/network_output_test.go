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
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestNetworkInitialOutputReachesModelWithoutFixedTruncation(t *testing.T) {
	const runID, callID = "run", "network-output"
	const executionID = runID + ":tool:main/" + callID
	content := strings.Repeat("证据🙂\n<\"\\\x00", 8000) + "FINAL EVIDENCE"
	for _, name := range []string{"websearch", "webfetch"} {
		for _, limit := range []int{90000, 0} {
			for _, slow := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/limit=%d/slow=%v", name, limit, slow), func(t *testing.T) {
					_, record := outputToolStore(t)
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					releaseSource := make(chan struct{})
					want := content
					if limit > 0 {
						want = textPrefix(content, limit)
					}
					source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if slow {
							select {
							case <-releaseSource:
							case <-r.Context().Done():
								return
							}
						}
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
					var requests, pendingReplies, noticesOnWire atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var request struct {
							Messages []provider.ChatMessage `json:"messages"`
						}
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Error(err)
							return
						}
						if requests.Add(1) == 1 {
							continuationEvent(w, mcpToolEvent(callID, name, arguments))
							return
						}
						last := request.Messages[len(request.Messages)-1]
						var waiting struct {
							Status      string `json:"status"`
							ExecutionID string `json:"executionId"`
						}
						if last.Role == "tool" && last.ToolCallID == callID && json.Unmarshal([]byte(last.Content), &waiting) == nil && waiting.Status == "running" {
							if waiting.ExecutionID != executionID || pendingReplies.Add(1) != 1 {
								t.Errorf("wrong or repeated pending network result: %+v", waiting)
								http.Error(w, "invalid tool result", http.StatusBadRequest)
								return
							}
							if slow {
								close(releaseSource)
							}
							continuationAnswer(w, "Waiting for the network result")
							return
						}
						if runtimeNoticeOnWire(last.Role, last.Content) {
							_, raw, _ := strings.Cut(last.Content, "\n")
							var notice workerproto.RuntimeEvent
							if err := json.Unmarshal([]byte(raw), &notice); err != nil || notice.ID != executionID || notice.SourceID != callID || notice.Kind != "tool" || notice.Status != "succeeded" || notice.ContentOutput != nil {
								t.Errorf("network completion was not restored before inference: %v", err)
								http.Error(w, "invalid completion", http.StatusBadRequest)
								return
							}
							noticesOnWire.Add(1)
							last.Role, last.ToolCallID, last.Content = "tool", callID, notice.Content
						}
						if last.Role != "tool" || last.ToolCallID != callID || !exceedsToolBudget(last.Content) {
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
						SessionID: "session", RunID: runID, Prompt: "Check the evidence",
						Model:              modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"},
						SearchPreviewBytes: limit,
						SearchProviders:    []workerproto.SearchProvider{{Name: "deepseek", Endpoint: source.URL, APIKey: "fixture"}},
						Webfetch:           workerproto.WebfetchConfig{PreviewBytes: limit},
					}
					var completed, runtimeEvents atomic.Int32
					answer, err := Run(ctx, command, func(event workerproto.Result) error {
						if event.Kind == "runtime_event" {
							runtimeEvents.Add(1)
							if event.RuntimeEvent.ContentOutput == nil || len(event.RuntimeEvent.Content) > maxRuntimeReportBytes {
								t.Error("network completion lost its full model-facing result reference")
							}
						}
						if event.Kind == "tool" && event.Status != "started" {
							completed.Add(1)
							encoded, _ := json.Marshal(event)
							if event.Status != "succeeded" || event.Tool.OutputRef == nil || len(encoded) > 32<<10 {
								t.Error("network result lost its saved output or exceeded the event transport budget")
							}
						}
						return record(event)
					})
					pending, events := pendingReplies.Load(), runtimeEvents.Load()
					if err != nil || answer != "verified" || requests.Load() != 2+pending || completed.Load() != 1 || events != noticesOnWire.Load() || events > 1 || pending > events || (slow && pending != 1) {
						t.Fatalf("network output flow: answer=%q requests=%d tools=%d pending=%d events=%d error=%v", answer, requests.Load(), completed.Load(), pending, events, err)
					}
				})
			}
		}
	}
}
