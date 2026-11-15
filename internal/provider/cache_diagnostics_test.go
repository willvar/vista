package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/smithy-go/eventstream"
	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
)

func TestCacheDiagnosticsOnlyCaptureEnabledRequestsAndRestartAfterGaps(t *testing.T) {
	dir := configureTestUserAgent(t)
	state := new(RequestState)
	scope := modeltrace.Scope{SessionID: uuid.NewString(), RunID: uuid.NewString()}
	model := modelcfg.Model{Provider: "openai-responses", ID: "fixture"}
	adapter := protocolAdapter{turn: func(ctx context.Context, model modelcfg.Model, _ []ChatMessage, _ []map[string]any, _, _ func(string) error) (Turn, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/responses", strings.NewReader(`{"input":[{"role":"user","content":"stable fixture"}]}`))
		if err != nil {
			return Turn{}, err
		}
		original := request.Body
		captureCacheRequest(request, model)
		if wrapped := request.Body != original; wrapped != scope.Enabled {
			t.Errorf("debug=%t: request body capture=%t", scope.Enabled, wrapped)
		}
		_, err = io.Copy(io.Discard, request.Body)
		if closeErr := request.Body.Close(); err == nil {
			err = closeErr
		}
		return Turn{}, err
	}}
	for i, want := range []string{"", "new-series", "unchanged", "", "new-series"} {
		scope.Enabled, scope.Turn = want != "", int32(i+1)
		ctx := WithRequestState(modeltrace.WithScope(context.Background(), scope), state)
		if _, err := retryModelTurn(ctx, model, nil, nil, adapter, nil, nil); err != nil {
			t.Fatal(err)
		}
		if !scope.Enabled {
			if len(state.Cache.baselines) != 0 {
				t.Fatal("disabled diagnostics retained a baseline across an unobserved request")
			}
			continue
		}
		list, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		var observation *modeltrace.CacheObservation
		for _, summary := range list {
			if summary.Turn == scope.Turn {
				record, err := modeltrace.Read(filepath.Join(dir, "state"), scope.SessionID, scope.RunID, summary.ID)
				if err != nil {
					t.Fatal(err)
				}
				observation = record.Cache
			}
		}
		if observation == nil || observation.PrefixChange != want {
			t.Fatalf("turn %d: cache observation=%+v, want %s", scope.Turn, observation, want)
		}
	}
}

func TestCacheDiagnosticsCompareActualBodiesAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "anthropic", "gemini", "bedrock", "mistral", "vercel-gateway"} {
		t.Run(protocol, func(t *testing.T) {
			dir := configureTestUserAgent(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				cached := 80
				if requests == 2 {
					cached = 0
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case "anthropic":
					fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":%d,\"output_tokens\":0,\"cache_read_input_tokens\":%d,\"cache_creation_input_tokens\":0}}}\n\n", 100-cached, cached)
					writeCacheFixtureResponse(t, w, protocol)
				case "gemini":
					fmt.Fprintf(w, "data: {\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":0,\"cachedContentTokenCount\":%d},\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n", cached)
				case "bedrock":
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					for _, event := range []struct{ name, body string }{{"messageStop", `{"stopReason":"end_turn"}`}, {"metadata", fmt.Sprintf(`{"usage":{"inputTokens":%d,"outputTokens":0,"cacheReadInputTokens":%d,"cacheWriteInputTokens":0}}`, 100-cached, cached)}} {
						message := eventstream.Message{Headers: eventstream.Headers{{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(event.name)}, {Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)}, {Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")}}, Payload: []byte(event.body)}
						if err := eventstream.NewEncoder().Encode(w, message); err != nil {
							t.Error(err)
						}
					}
				case "vercel-gateway":
					fmt.Fprintf(w, "data: {\"type\":\"finish\",\"finishReason\":{\"unified\":\"stop\"},\"usage\":{\"inputTokens\":{\"total\":100,\"cacheRead\":%d,\"cacheWrite\":0},\"outputTokens\":{\"total\":0}}}\n\n", cached)
				default:
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":0,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\ndata: [DONE]\n\n", cached)
				}
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: protocol, ID: "fixture", APIKey: "fixture-key", BaseURL: server.URL, SessionID: "cache-conversation", AWSRegion: "us-east-1"}
			if protocol == "bedrock" {
				model.ID = "anthropic.claude-sonnet-4-5"
				model.APIKey = ""
				model.AWSAccessID = "fixture"
				model.AWSSecret = "fixture"
			}
			state := new(RequestState)
			scope := modeltrace.Scope{Enabled: true, SessionID: uuid.NewString(), RunID: uuid.NewString()}
			ctx := WithRequestState(modeltrace.WithScope(context.Background(), scope), state)
			messages := []ChatMessage{{Role: "system", Content: "private stable rules"}, {Role: "user", Content: "private fixture"}}
			for i := range 2 {
				state.Reset()
				if _, err := ModelTurn(ctx, model, messages, nil); err != nil {
					t.Fatal(err)
				}
				messages = append(messages, ChatMessage{Role: "user", Content: "next"})
				list, err := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
				if err != nil || len(list) != i+1 {
					t.Fatalf("trace list=%v err=%v", list, err)
				}
			}
			list, _ := modeltrace.List(filepath.Join(dir, "state"), scope.SessionID, scope.RunID)
			var compared bool
			for _, summary := range list {
				record, err := modeltrace.Read(filepath.Join(dir, "state"), scope.SessionID, scope.RunID, summary.ID)
				if err != nil || record.Cache == nil || record.Cache.InputTokens == nil || *record.Cache.InputTokens != 100 || record.Cache.CachedTokens == nil || record.Cache.RequestBytes <= 0 {
					t.Fatalf("missing diagnostic evidence: %+v %v", record.Cache, err)
				}
				if *record.Cache.CachedTokens == 0 {
					compared = true
					if record.Cache.PrefixChange != "append-only" || !record.Cache.CachedDrop {
						t.Fatalf("lost cross-turn prefix baseline: %+v", record.Cache)
					}
				}
				encoded, _ := json.Marshal(record.Cache)
				if strings.Contains(string(encoded), "private") {
					t.Fatal("diagnostic metadata stored prompt content")
				}
			}
			if !compared || requests != 2 {
				t.Fatal("comparison missing or made extra cache requests")
			}
		})
	}
}

func TestCacheBaselineIsolationAndUnknownTelemetry(t *testing.T) {
	state := new(RequestState)
	ctx := WithRequestState(context.Background(), state)
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "same"}}}
	usage := &ModelUsage{InputTokens: 100, CacheReadTokens: 80, CacheReadKnown: testBool(true)}
	observeCache(ctx, body, 100, usage, "route-A")
	other := observeCache(ctx, body, 100, &ModelUsage{InputTokens: 100, CacheReadKnown: testBool(true)}, "route-B")
	if other.CachedDrop || other.PrefixChange != "new-series" {
		t.Fatal("different route compared another baseline")
	}
	state.Reset()
	unknown := observeCache(ctx, body, 100, &ModelUsage{InputTokens: 100}, "route-A")
	if unknown.CachedTokens != nil || unknown.CachedDrop {
		t.Fatal("unknown cache count became a measured miss")
	}
	measured := observeCache(ctx, body, 100, &ModelUsage{InputTokens: 100, CacheReadKnown: testBool(true)}, "route-A")
	if !measured.CachedDrop || measured.PrefixChange != "unchanged" {
		t.Fatal("ordinary reset or missing telemetry destroyed the route's baseline")
	}
}

func TestCacheComparisonRetainsChronologicalInstructionPositions(t *testing.T) {
	for _, field := range []string{"messages", "prompt"} {
		body := func(moved bool) map[string]any {
			instruction := map[string]any{"role": "system", "content": "updated policy"}
			next := map[string]any{"role": "user", "content": "next input"}
			items := []any{map[string]any{"role": "system", "content": "baseline"}, map[string]any{"role": "user", "content": "first input"}}
			if moved {
				items = append(items, next, instruction)
			} else {
				items = append(items, instruction, next)
			}
			return map[string]any{field: items}
		}
		before, after := fingerprintPrompt(body(false)), fingerprintPrompt(body(true))
		if reason, index := compareCachePrefixes(&before, &after); reason != "history" || index == nil || *index != 1 {
			t.Fatalf("%s ignored chronological instruction movement: %s %v", field, reason, index)
		}
	}
}

func TestCacheOwnerSeparatesProviderCredentialAndAccountChanges(t *testing.T) {
	base := modelcfg.Model{ID: "same-model", ConnectionID: "same-connection", SessionID: "same-session"}
	request := httptest.NewRequest("POST", "https://gateway.invalid/model", nil)
	owner := requestOwner(context.Background(), base, request)
	for name, change := range map[string]func(*modelcfg.Model){
		"AWS access key":     func(m *modelcfg.Model) { m.AWSAccessID = "different-key" },
		"AWS session":        func(m *modelcfg.Model) { m.AWSSession = "different-session" },
		"SAP resource group": func(m *modelcfg.Model) { m.ResourceGroup = "different-group" },
		"Vertex project":     func(m *modelcfg.Model) { m.ProjectID = "different-project" },
		"GitLab account headers": func(m *modelcfg.Model) {
			m.GitLabHeaders = map[string]string{"X-Gitlab-Instance-Id": "different-instance"}
		},
		"credential source": func(m *modelcfg.Model) { m.CredentialRef = "different-source" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			if requestOwner(context.Background(), changed, request) == owner {
				t.Fatal("different credential/account reused the old cache baseline")
			}
		})
	}
}
