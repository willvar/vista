package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestAnthropicThinkingAndSignedToolContinuation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Messages) != 3 || !strings.Contains(string(request.Messages[1].Content), `"signature":"signed-token"`) || !strings.Contains(string(request.Messages[1].Content), `"thinking":"Check."`) || !strings.Contains(string(request.Messages[1].Content), `"data":"opaque-token"`) {
				t.Errorf("signed assistant content was not replayed: %+v", request.Messages)
			} else {
				var blocks []struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(request.Messages[1].Content, &blocks); err != nil || len(blocks) != 3 || blocks[0].Type != "redacted_thinking" || blocks[1].Type != "thinking" || blocks[2].Type != "tool_use" {
					t.Errorf("Anthropic content block order changed: %+v, err=%v", blocks, err)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque-token"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"Check."}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"signed-token"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-1","name":"read_file","input":{}}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/a\"}"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":2}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	var reasoning strings.Builder
	first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || reasoning.String() != "Check." || first.Text != "" || len(first.Calls) != 1 || len(first.AnthropicBlocks) != 3 {
		t.Fatalf("thinking tool turn: %+v, reasoning=%q, err=%v", first, reasoning.String(), err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, AnthropicBlocks: first.AnthropicBlocks}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || requests != 2 {
		t.Fatalf("signed continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestDirectAnthropicThinkingRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			MaxTokens    int                      `json:"max_tokens"`
			Thinking     *modelcfg.ThinkingOption `json:"thinking"`
			OutputConfig map[string]string        `json:"output_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/messages" || r.URL.RawQuery != "beta=true" || r.Header.Get("x-api-key") != "key" || body.MaxTokens != 32000 {
			t.Errorf("direct Anthropic route, auth, or output limit: %s %+v, max=%d", r.URL, r.Header, body.MaxTokens)
		}
		switch requests {
		case 1:
			if body.Thinking == nil || body.Thinking.Type != "adaptive" || body.Thinking.BlockBinding == nil || body.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" || r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14,thinking-binding-controls-2026-08-01" {
				t.Errorf("new Claude default thinking: %+v, beta=%q", body.Thinking, r.Header.Get("anthropic-beta"))
			}
		case 2:
			if body.Thinking == nil || body.Thinking.Type != "enabled" || body.Thinking.BudgetTokens == nil || *body.Thinking.BudgetTokens != 16000 || body.Thinking.BudgetAlias != nil || body.Thinking.BlockBinding != nil || body.OutputConfig["effort"] != "high" || r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" {
				t.Errorf("manual thinking and effort: %+v, output=%+v, beta=%q", body.Thinking, body.OutputConfig, r.Header.Get("anthropic-beta"))
			}
		case 3:
			if body.Thinking == nil || body.Thinking.Type != "disabled" || body.Thinking.BlockBinding != nil || r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" {
				t.Errorf("disabled thinking: %+v, beta=%q", body.Thinking, r.Header.Get("anthropic-beta"))
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done."}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ProviderID: "anthropic", ID: "claude-opus-5-5", BaseURL: server.URL, APIKey: "key", OutputLimit: 128000}
	turn := func() {
		result, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
		if err != nil || result.Text != "Done." {
			t.Fatalf("direct Anthropic turn: %+v, err=%v", result, err)
		}
	}
	turn()
	budget := 20000
	model.ID = "claude-sonnet-4-5"
	model.Thinking = &modelcfg.ThinkingOption{Type: "enabled", BudgetAlias: &budget, Display: "summarized"}
	model.Effort = "high"
	turn()
	model.ID = "claude-opus-5-5"
	model.Thinking = &modelcfg.ThinkingOption{Type: "disabled"}
	model.Effort = ""
	turn()
	if requests != 3 {
		t.Fatalf("expected three direct Anthropic turns, got %d", requests)
	}
}

func TestAnthropicBindingHeaderFollowsThinkingField(t *testing.T) {
	for _, check := range []struct {
		protocol, providerID, id string
		disabled                 bool
	}{
		{"anthropic", "anthropic", "claude-fable-5-1", false},
		{"anthropic", "custom", "claude-fable-5-1", false},
		{"anthropic", "opencode", "claude-fable-5-1", false},
		{"vertex-anthropic", "google-vertex", "claude-fable-5-1@default", false},
		{"anthropic", "custom", "claude-fable-5-1", true},
	} {
		t.Run(fmt.Sprintf("%s/disabled=%t", check.providerID, check.disabled), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Thinking *modelcfg.ThinkingOption `json:"thinking"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				hasBinding := body.Thinking != nil && body.Thinking.BlockBinding != nil
				hasBeta := strings.Contains(r.Header.Get("anthropic-beta"), "thinking-binding-controls-2026-08-01")
				if hasBinding != !check.disabled || hasBeta != hasBinding {
					t.Errorf("binding=%t beta=%q disabled=%t", hasBinding, r.Header.Get("anthropic-beta"), check.disabled)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done."}}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ProviderID: check.providerID, ID: check.id, BaseURL: server.URL, APIKey: "key", Custom: check.providerID == "custom", Authentication: "bearer"}
			if err := ApplyReasoningEffort(&model, "high"); err != nil {
				t.Fatal(err)
			}
			model.ReasoningDisabled = check.disabled
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnthropicThinkingTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"output_tokens_details":{"thinking_tokens":1}}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done."}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8,"output_tokens_details":{"thinking_tokens":3}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ID: "claude-opus-5-5", BaseURL: server.URL, APIKey: "key"}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 10, OutputTokens: 8, ReasoningTokens: 3}) {
		t.Fatalf("Anthropic thinking usage: %+v, err=%v", turn, err)
	}
}

func TestAnthropicBearerAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.URL.RawQuery != "beta=true" || r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("x-api-key") != "" {
			t.Errorf("Anthropic bearer auth: %s, authorization=%q, x-api-key=%q", r.URL, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Authenticated."}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ProviderID: "anthropic", ID: "claude-sonnet-4-5", BaseURL: server.URL, APIKey: "private-token", AnthropicAuthToken: true}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "Authenticated." {
		t.Fatalf("Anthropic bearer turn: %+v, err=%v", turn, err)
	}
}

func TestZAIMessagesAllowsUnsignedThinkingToolContinuation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/anthropic/v1/messages" || r.Header.Get("Authorization") != "Bearer zai-key" || r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" {
			t.Errorf("ZAI Messages route or authorization: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if requests == 2 {
			var body struct {
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Messages) != 3 || !strings.Contains(string(body.Messages[1].Content), `"thinking":"Check."`) || strings.Contains(string(body.Messages[1].Content), `"signature"`) || !strings.Contains(string(body.Messages[2].Content), "tool_result") {
				t.Errorf("ZAI unsigned thinking and tool result were not replayed: %+v", body.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"Check."}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ProviderID: "zai-coding-plan", ID: "glm-5.2", BaseURL: server.URL + "/api/anthropic/v1", APIKey: "zai-key"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	var reasoning strings.Builder
	first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || reasoning.String() != "Check." || len(first.Calls) != 1 || len(first.AnthropicBlocks) != 2 {
		t.Fatalf("ZAI unsigned thinking turn: %+v, reasoning=%q, err=%v", first, reasoning.String(), err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, AnthropicBlocks: first.AnthropicBlocks}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file data"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done" || requests != 2 {
		t.Fatalf("ZAI unsigned thinking continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestAnthropicThinkingBoundsAndSinkErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`+"\n\n")
		for range 3 {
			chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": strings.Repeat("r", 90<<10)}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "solve"}}
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "reasoning exceeds") {
		t.Fatalf("oversized reasoning accepted: %v", err)
	}
	if _, err := ModelTurn(context.Background(), model, messages, nil); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("reasoning silently dropped: %v", err)
	}
	abort := errors.New("reasoning event failed")
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("reasoning sink error was not propagated: %v", err)
	}
}

func TestAnthropicRejectsUnsignedThinkingToolContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check."}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call","name":"read_file","input":{}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	_, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "read"}}, nil, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "signed thinking") {
		t.Fatalf("unsigned thinking tool turn accepted: %v", err)
	}
}

func TestAnthropicStreamToolCallAndResultHistory(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Anthropic endpoint or headers: %s %+v", r.URL, r.Header)
		}
		var request struct {
			System string `json:"system"`
			Tools  []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"input_schema"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.System != "system prompt" || len(request.Tools) != 1 || request.Tools[0].Name != "read_file" {
			t.Errorf("Anthropic request missing system/tool: %+v", request)
		}
		if requests == 2 && (len(request.Messages) != 3 || request.Messages[2].Role != "user" || !strings.Contains(string(request.Messages[2].Content), "tool_result")) {
			t.Errorf("Anthropic tool result history missing: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2,\"output_tokens\":1}}}\n\n")
		if requests == 1 {
			fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"read_file\",\"input\":{}}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":8}}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Done\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n")
		}
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "anthropic", ID: "test", BaseURL: server.URL, APIKey: "key"}
	schema := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "description": "read", "parameters": map[string]any{"type": "object"}}}}
	messages := []ChatMessage{{Role: "system", Content: "system prompt"}, {Role: "user", Content: "read a file"}}
	first, err := ModelTurn(context.Background(), model, messages, schema)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].Function.Arguments != `{"path":"/tmp/a"}` || first.FinishReason != "tool_use" || first.Usage == nil || *first.Usage != (ModelUsage{InputTokens: 15, OutputTokens: 8, CacheReadTokens: 3}) {
		t.Fatalf("tool turn: %+v %v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := ModelTurn(context.Background(), model, messages, schema)
	if err != nil || second.Text != "Done" || second.FinishReason != "end_turn" || second.Usage == nil || *second.Usage != (ModelUsage{InputTokens: 15, OutputTokens: 9, CacheReadTokens: 3}) || requests != 2 {
		t.Fatalf("final turn: %+v %v requests=%d", second, err, requests)
	}
}

func TestAnthropicRejectsIncompleteToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"read_file\",\"input\":{}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	}))
	defer server.Close()
	_, err := AnthropicTurn(context.Background(), modelcfg.Model{Provider: "anthropic", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete tool arguments were accepted: %v", err)
	}
}

func TestAnthropicRejectsInvalidUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9223372036854775807,\"cache_read_input_tokens\":9223372036854775807}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	_, err := AnthropicTurn(context.Background(), modelcfg.Model{Provider: "anthropic", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("invalid Anthropic usage was accepted: %v", err)
	}
}
