package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/smithy-go/eventstream"
	modelcfg "vista/internal/model"
)

// Selection cases adapted from OpenCode's cache-policy.test.ts at
// 6dd40633a15ec82c5fa2f2c4ea17e22c2ba67084; see licenses/opencode/LICENSE.
// Assertions inspect requests received by HTTP servers, after final encoding.
func TestAutomaticProviderCacheBodies(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, provider, id string
		hints, points                int
	}{
		{"Messages", "anthropic", "anthropic", "claude-sonnet-4-5", 4, 0},
		{"Converse", "bedrock", "amazon-bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", 0, 4},
		{"Chat", "openai-chat", "openai", "gpt-4.1", 0, 0},
		{"Responses", "openai-responses", "openai", "gpt-5.4", 0, 0},
		{"Gemini", "gemini", "google", "gemini-2.5-flash", 0, 0},
		{"DeepSeek", "openai-chat", "deepseek", "deepseek-chat", 0, 0},
		{"Alibaba Qwen", "openai-chat", "alibaba", "qwen-plus", 3, 0},
		{"OpenRouter Claude", "openai-chat", "openrouter", "anthropic/claude-sonnet-4.5", 4, 0},
		{"OpenRouter Qwen", "openai-chat", "openrouter", "qwen/qwen3-coder", 3, 0},
		{"OpenRouter Gemini", "openai-chat", "openrouter", "google/gemini-2.5-pro", 0, 0},
		{"Gateway Claude", "vercel-gateway", "vercel", "anthropic/claude-sonnet-4.5", 4, 0},
		{"Gateway Qwen", "vercel-gateway", "vercel", "alibaba/qwen3-coder", 3, 0},
		{"Gateway Gemini", "vercel-gateway", "vercel", "google/gemini-2.5-pro", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				decoder := json.NewDecoder(r.Body)
				decoder.UseNumber()
				if err := decoder.Decode(&received); err != nil {
					t.Error(err)
				}
				writeCacheFixtureResponse(t, w, tc.protocol)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: tc.protocol, ProviderID: tc.provider, ID: tc.id, BaseURL: server.URL, APIKey: "fixture", AWSRegion: "us-east-1", SessionID: "session"}
			if tc.protocol == "bedrock" {
				model.APIKey = ""
				model.AWSAccessID = "fixture"
				model.AWSSecret = "fixture"
			}
			messages := []ChatMessage{{Role: "system", Content: "base instructions"}, {Role: "system", Content: "project instructions"}, {Role: "user", Content: "read"}}
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
			before, _ := json.Marshal([]any{messages, schemas})
			if _, err := ModelTurn(context.Background(), model, messages, schemas); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal([]any{messages, schemas})
			if string(before) != string(after) {
				t.Fatal("cache policy mutated canonical input")
			}
			hints, points := countCacheMarkers(received)
			if hints != tc.hints || points != tc.points {
				t.Fatalf("markers hints=%d points=%d, want %d/%d; body=%v", hints, points, tc.hints, tc.points, received)
			}
			if tc.provider == "openai" {
				if received["prompt_cache_key"] != "session" {
					t.Fatal("missing session cache key")
				}
			} else if received["prompt_cache_key"] != nil {
				t.Fatal("sent an unsupported OpenAI cache key")
			}
			if tc.protocol == "anthropic" {
				systems := received["system"].([]any)
				if len(systems) != 2 || systems[0].(map[string]any)["cache_control"] == nil || systems[1].(map[string]any)["cache_control"] == nil {
					t.Fatal("lost independent system breakpoints")
				}
			}
			if tc.protocol == "gemini" && received["cachedContent"] != nil {
				t.Fatal("implicit caching created a resource reference")
			}
			if tc.name == "Gateway Qwen" {
				first := received["prompt"].([]any)[0].(map[string]any)
				options := first["providerOptions"].(map[string]any)
				if options["alibaba"] == nil || options["anthropic"] != nil {
					t.Fatal("Qwen received the wrong gateway provider namespace")
				}
			}
			if tc.name == "Alibaba Qwen" || tc.name == "OpenRouter Qwen" {
				first := received["messages"].([]any)[0].(map[string]any)
				block := first["content"].([]any)[0].(map[string]any)
				if first["role"] != "system" || block["type"] != "text" || block["text"] != "base instructions" || block["cache_control"] == nil {
					t.Fatal("Qwen system marker changed instruction content or authority")
				}
			}
		})
	}
}

func countCacheMarkers(value any) (hints, points int) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "cache_control" || key == "cacheControl" {
				hints++
				continue
			}
			if key == "cachePoint" {
				points++
				continue
			}
			a, b := countCacheMarkers(child)
			hints += a
			points += b
		}
	case []any:
		for _, child := range v {
			a, b := countCacheMarkers(child)
			hints += a
			points += b
		}
	}
	return
}

func writeCacheFixtureResponse(t *testing.T, w http.ResponseWriter, protocol string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	switch protocol {
	case "anthropic", "vertex-anthropic":
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	case "openai-responses":
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	case "gemini":
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")
	case "vercel-gateway":
		fmt.Fprint(w, "data: {\"type\":\"finish\",\"finishReason\":{\"unified\":\"stop\"}}\n\n")
	case "bedrock":
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		message := eventstream.Message{Headers: eventstream.Headers{
			{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue("messageStop")},
			{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
			{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
		}, Payload: []byte(`{"stopReason":"end_turn"}`)}
		if err := eventstream.NewEncoder().Encode(w, message); err != nil {
			t.Error(err)
		}
	default:
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}
}

func TestAutomaticCacheToolTailPreservesNativeReplay(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&received); err != nil {
			t.Error(err)
		}
		writeCacheFixtureResponse(t, w, "anthropic")
	}))
	defer server.Close()
	messages := []ChatMessage{{Role: "system", Content: "stable"}, {Role: "user", Content: "read"}, {Role: "assistant", AnthropicBlocks: []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"opaque thought","signature":"signed"}`),
		json.RawMessage(`{"type":"tool_use","id":"call","name":"read","input":{"n":9007199254740993}}`),
	}}, {Role: "tool", ToolCallID: "call", Content: "result"}}
	before, _ := json.Marshal(messages)
	if _, err := AnthropicTurn(context.Background(), modelcfg.Model{Provider: "anthropic", APIKey: "fixture", ID: "claude-sonnet-4-5", BaseURL: server.URL}, messages, nil); err != nil {
		t.Fatal(err)
	}
	items := received["messages"].([]any)
	blocks := items[1].(map[string]any)["content"].([]any)
	thought := blocks[0].(map[string]any)
	if thought["signature"] != "signed" || thought["cache_control"] != nil {
		t.Fatal("changed signed reasoning")
	}
	args := blocks[1].(map[string]any)["input"].(map[string]any)
	if fmt.Sprint(args["n"]) != "9007199254740993" {
		t.Fatal("cache compilation rounded a replay argument")
	}
	last := items[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if last["type"] != "tool_result" || last["cache_control"] == nil {
		t.Fatal("tool-only tail is not cacheable")
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("mutated native replay history")
	}
	encoded, _ := json.Marshal(received)
	if strings.Contains(string(encoded), "providerOptions") {
		t.Fatal("foreign cache representation in Messages")
	}
}
