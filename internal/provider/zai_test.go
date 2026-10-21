package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	modelcfg "vista/internal/model"
)

func TestToolStreamFollowsModelAndToolSupport(t *testing.T) {
	requests := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer zai-key" {
			t.Errorf("unexpected ZAI route or authentication: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	for _, check := range []struct {
		providerID, modelID string
		tools               bool
		wantToolStream      bool
	}{
		{"zai", "glm-4.5", true, false},
		{"zai", "glm-4.6", true, true},
		{"zai-coding-plan", "glm-4.7-flash", true, true},
		{"zhipuai-coding-plan", "glm-5.3", true, true},
		{"zhipuai", "glm-5.2", true, true},
		{"zhipuai-coding-plan", "glm-5", true, true},
		{"zai", "glm-5", false, false},
		{"openai", "glm-5", true, false},
	} {
		t.Run(check.providerID+"/"+check.modelID, func(t *testing.T) {
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: check.providerID, ID: check.modelID, BaseURL: server.URL, APIKey: "zai-key", Thinking: &modelcfg.ThinkingOption{Type: "enabled"}, ReasoningEffort: "high"}
			var offered []map[string]any
			if check.tools {
				offered = schemas
			}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, offered)
			if err != nil || turn.Text != "ok" {
				t.Fatalf("ZAI Chat response: %+v, %v", turn, err)
			}
			body := <-requests
			if got := body["tool_stream"] == true; got != check.wantToolStream {
				t.Fatalf("tool_stream = %v, want %v, body=%+v", got, check.wantToolStream, body)
			}
			if got := body["tools"] != nil; got != check.tools {
				t.Fatalf("tools = %v, want %v, body=%+v", got, check.tools, body)
			}
			if check.providerID == "openai" {
				if body["thinking"] != nil || body["reasoning_effort"] != nil {
					t.Fatalf("ZAI-only options leaked to OpenAI: %+v", body)
				}
			} else {
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "enabled" || body["reasoning_effort"] != "high" {
					t.Fatalf("ZAI Chat options missing: %+v", body)
				}
			}
		})
	}
}

func TestZAIChatNativeRequestOptions(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	no := false
	yes := true
	model := modelcfg.Model{
		Provider: "openai-chat", ProviderID: "zai", ID: "glm-5", BaseURL: server.URL, APIKey: "key",
		ToolStream: &no, ZAIDoSample: &no, ChatResponseFormat: &modelcfg.ChatResponseFormat{Type: "json_object"},
		ZAIRequestID: "request-one", ZAIUserID: "user-one",
	}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, schemas)
	if err != nil || turn.Text != "ok" {
		t.Fatalf("ZAI Chat native options turn: %+v, err=%v", turn, err)
	}
	body := <-requests
	format, ok := body["response_format"].(map[string]any)
	if body["tool_stream"] != false || body["do_sample"] != false || !ok || format["type"] != "json_object" || body["request_id"] != "request-one" || body["user_id"] != "user-one" {
		t.Fatalf("ZAI Chat native options missing: %+v", body)
	}
	model.ToolStream = &yes
	model.ZAIDoSample = nil
	turn, err = OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "ok" {
		t.Fatalf("ZAI explicit tool stream turn: %+v, err=%v", turn, err)
	}
	body = <-requests
	if body["tool_stream"] != true || body["tools"] != nil || body["do_sample"] != nil {
		t.Fatalf("ZAI explicit toolStream option lost: %+v", body)
	}
}
