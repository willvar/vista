package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
)

func TestOpenAIChatStreamAndFailures(t *testing.T) {
	var requested []ChatMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var input struct {
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
			Messages []ChatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Model != "test-model" || !input.Stream {
			t.Errorf("unexpected request body: %+v", input)
		}
		requested = input.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "test-model", BaseURL: srv.URL + "/v1", APIKey: "test-key"}
	reply, err := OpenAIChat(context.Background(), model, []ChatMessage{{Role: "assistant", Content: "earlier"}}, "now")
	if err != nil || reply != "hello world" {
		t.Fatalf("reply %q, error %v", reply, err)
	}
	if len(requested) != 2 || requested[0].Content != "earlier" || requested[1].Content != "now" {
		t.Fatalf("history and prompt not forwarded: %+v", requested)
	}

}

func TestCustomOpenAIChatUsesRemoteHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "models.example.test" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request host=%q path=%q auth=%q", r.Host, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"remote HTTP works\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
	}}
	previousTransport := client.Transport
	client.Transport = transport
	defer func() { client.Transport = previousTransport; transport.CloseIdleConnections() }()
	model := modelcfg.Model{Provider: "openai-chat", ID: "test-model", BaseURL: "http://models.example.test/v1", APIKey: "test-key"}
	if reply, err := OpenAIChat(context.Background(), model, nil, "hello"); err != nil || reply != "remote HTTP works" {
		t.Fatalf("custom HTTP response: %q %v", reply, err)
	}
	model.ProviderID = "opencode"
	if _, err := OpenAIChat(context.Background(), model, nil, "hello"); err == nil || !strings.Contains(err.Error(), "invalid OpenAI Chat base URL") {
		t.Fatalf("catalog endpoint unexpectedly accepted remote HTTP: %v", err)
	}
}

func TestOpenAICompatibleChatSessionReasoningEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Effort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Effort != "high" {
			t.Errorf("reasoning effort not forwarded: %+v %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "custom", BaseURL: srv.URL, APIKey: "key", ReasoningEffort: "high", ChatReasoningOverride: true}
	if reply, err := OpenAIChat(context.Background(), model, nil, "hello"); err != nil || reply != "ok" {
		t.Fatalf("response: %q %v", reply, err)
	}
}

func TestZenChatSessionReasoningEffortReachesRequest(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Effort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Effort != "max" {
			t.Errorf("Zen Chat effort not forwarded: %+v %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	previousTransport := client.Transport
	client.Transport = srv.Client().Transport
	defer func() { client.Transport = previousTransport }()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode", ID: "deepseek-v4-flash", BaseURL: srv.URL + "/v1", APIKey: "key", ReasoningEffort: "max", ChatReasoningOverride: true}
	if reply, err := OpenAIChat(context.Background(), model, nil, "hello"); err != nil || reply != "ok" {
		t.Fatalf("Zen Chat response: %q %v", reply, err)
	}
}

func TestZenGoChatSendsSessionHeader(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-opencode-session"); got != "session-123" {
			t.Errorf("Zen Go session header = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	previousTransport := client.Transport
	client.Transport = server.Client().Transport
	defer func() { client.Transport = previousTransport }()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode-go", SessionID: "session-123", ID: "deepseek-v4-flash", BaseURL: server.URL + "/v1", APIKey: "key"}
	if reply, err := OpenAIChat(context.Background(), model, nil, "Hi"); err != nil || reply != "Hi" {
		t.Fatalf("Zen Go reply: %q %v", reply, err)
	}
}

func TestOpenAIChatToolDeltasWithoutIndexes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"call-a","type":"function","function":{"name":"first","arguments":"{\"value\":"}},{"id":"call-b","type":"function","function":{"name":"second","arguments":"{\"value\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"call-b","function":{"arguments":"2}"}},{"id":"call-a","function":{"arguments":"1}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"call-c","type":"function","function":{"name":"third","arguments":"{\"value\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"3}"}}]},"finish_reason":"tool_calls"}]}`,
		} {
			fmt.Fprint(w, "data: "+event+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "run three tools"}}, nil)
	if err != nil || len(turn.Calls) != 3 {
		t.Fatalf("unindexed tool deltas: calls=%+v err=%v", turn.Calls, err)
	}
	for i, want := range []struct{ id, name, args string }{
		{"call-a", "first", `{"value":1}`},
		{"call-b", "second", `{"value":2}`},
		{"call-c", "third", `{"value":3}`},
	} {
		if got := turn.Calls[i]; got.ID != want.id || got.Function.Name != want.name || got.Function.Arguments != want.args {
			t.Fatalf("call %d: got %+v, want %+v", i, got, want)
		}
	}
}

func TestOpenAIChatStreamsReasoningSeparately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Check \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"inputs.\",\"content\":\"Answer.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	var text, reasoning strings.Builder
	turn, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil,
		func(fragment string) error { _, err := text.WriteString(fragment); return err },
		func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Answer." || turn.FinishReason != "stop" || text.String() != "Answer." || reasoning.String() != "Check inputs." {
		t.Fatalf("Chat reasoning: %+v, text=%q, reasoning=%q, err=%v", turn, text.String(), reasoning.String(), err)
	}
	if _, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("reasoning was silently dropped: %v", err)
	}
	abort := errors.New("persistence failed")
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil, nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("reasoning sink error was not propagated: %v", err)
	}
}

func TestOpenAIChatStreamsRefusalAsText(t *testing.T) {
	var late atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"I ","refusal":"cannot help."},"finish_reason":"stop"}]}`+"\n\n")
		if late.Load() {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"refusal":"late"}}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	var streamed strings.Builder
	turn, err := ModelTurnStream(context.Background(), model, []ChatMessage{{Role: "user", Content: "unsafe"}}, nil, func(s string) error { _, err := streamed.WriteString(s); return err })
	if err != nil || turn.Text != "I cannot help." || streamed.String() != turn.Text {
		t.Fatalf("Chat refusal: turn=%+v streamed=%q err=%v", turn, streamed.String(), err)
	}
	late.Store(true)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "unsafe"}}, nil); err == nil || !strings.Contains(err.Error(), "after finish reason") {
		t.Fatalf("late refusal was accepted: %v", err)
	}
}

func TestOpenRouterReplaysReasoningDetailsWithToolResult(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var body struct {
				Messages []struct {
					ReasoningDetails []map[string]any `json:"reasoning_details"`
					Reasoning        string           `json:"reasoning"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			} else if len(body.Messages) != 3 || len(body.Messages[1].ReasoningDetails) != 2 || body.Messages[1].ReasoningDetails[0]["text"] != "Check inputs." || body.Messages[1].ReasoningDetails[0]["index"] != float64(0) || body.Messages[1].ReasoningDetails[1]["data"] != "opaque" || body.Messages[1].Reasoning != "Check inputs." {
				t.Errorf("reasoning details were not replayed: %+v", body.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning":"duplicate","reasoning_details":[{"type":"reasoning.text","id":"reason-1","index":0,"text":"Check "}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","id":"reason-1","index":0,"text":"inputs."},{"type":"reasoning.encrypted","data":"opaque"}],"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "openrouter", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	var reasoning strings.Builder
	first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
	if err != nil || reasoning.String() != "Check inputs." || len(first.Calls) != 1 || len(first.ReasoningDetails) != 2 {
		t.Fatalf("OpenRouter details: turn=%+v reasoning=%q err=%v", first, reasoning.String(), err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ReasoningDetails: first.ReasoningDetails, ReasoningText: first.ReasoningText}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := ModelTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || requests != 2 {
		t.Fatalf("OpenRouter continuation: turn=%+v requests=%d err=%v", second, requests, err)
	}
}

func TestChatReasoningDetailsRejectMalformedAndOversized(t *testing.T) {
	for _, sample := range []struct{ raw, want string }{
		{`[{"type":"unknown","text":"hidden"}]`, "unsupported"},
		{`[{"type":"reasoning.encrypted","data":123}]`, "invalid"},
		{`[{"type":"reasoning.text","text":"x"}]`, ""},
	} {
		var details chatReasoningDetails
		_, err := details.add(json.RawMessage(sample.raw))
		if sample.want == "" && err != nil || sample.want != "" && (err == nil || !strings.Contains(err.Error(), sample.want)) {
			t.Fatalf("detail %s: %v", sample.raw, err)
		}
	}
	var details chatReasoningDetails
	for range 3 {
		chunk, _ := json.Marshal([]map[string]string{{"type": "reasoning.text", "text": strings.Repeat("r", 25<<10)}})
		_, err := details.add(chunk)
		if err != nil {
			if !strings.Contains(err.Error(), "64 KiB") {
				t.Fatalf("wrong limit error: %v", err)
			}
			return
		}
	}
	t.Fatal("oversized reasoning details accepted")
}

func TestKimiReasoningDetailsMergeAndDropStreamIndex(t *testing.T) {
	var details chatReasoningDetails
	for _, raw := range []string{
		`[{"type":"summary","index":2,"summary":"First "}]`,
		`[{"type":"summary","index":2,"summary":"second."},{"type":"encrypted","index":3,"encrypted":"opaque"}]`,
	} {
		if _, err := details.add(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := details.replay()
	if err != nil || len(replay) != 2 || strings.Contains(string(replay[0]), `"index"`) || strings.Contains(string(replay[1]), `"index"`) || !strings.Contains(string(replay[0]), `"First second."`) {
		t.Fatalf("Kimi details replay: %s, err=%v", replay, err)
	}
	wire, err := chatMessagesWithReasoningDetails([]ChatMessage{{Role: "assistant", ReasoningDetails: replay, ReasoningText: "Full thinking."}}, "kimi-for-coding")
	if err != nil || wire[0]["reasoning_content"] != "Full thinking." {
		t.Fatalf("Kimi reasoning_content missing: %+v, err=%v", wire, err)
	}
}

func TestMoonshotChatReplaysPlainReasoningWithToolResult(t *testing.T) {
	for _, providerID := range []string{"moonshotai", "moonshotai-cn", "kimi-for-coding"} {
		t.Run(providerID, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body struct {
					ReasoningEffort string `json:"reasoning_effort"`
					Messages        []struct {
						Role             string  `json:"role"`
						Content          *string `json:"content"`
						ReasoningContent string  `json:"reasoning_content"`
						ToolCallID       string  `json:"tool_call_id"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.ReasoningEffort != "high" {
					t.Errorf("Moonshot Chat reasoning effort missing: %+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests == 1 {
					fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"Check "}}]}`+"\n\n")
					fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"file.","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
					return
				}
				if len(body.Messages) != 3 || body.Messages[1].Role != "assistant" || body.Messages[1].Content != nil || body.Messages[1].ReasoningContent != "Check file." || body.Messages[2].ToolCallID != "call_1" {
					t.Errorf("Moonshot reasoning or tool result lost: %+v", body.Messages)
				}
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: providerID, ID: "kimi-k2.7-code", BaseURL: server.URL, APIKey: "key", ReasoningEffort: "high"}
			messages := []ChatMessage{{Role: "user", Content: "read"}}
			var reasoning strings.Builder
			first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
			if err != nil || len(first.Calls) != 1 || first.ReasoningText != "Check file." || reasoning.String() != "Check file." || len(first.ReasoningDetails) != 0 {
				t.Fatalf("Moonshot first turn: %+v, reasoning=%q, err=%v", first, reasoning.String(), err)
			}
			messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ReasoningText: first.ReasoningText}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "contents"})
			second, err := ModelTurn(context.Background(), model, messages, nil)
			if err != nil || second.Text != "Done." || requests != 2 {
				t.Fatalf("Moonshot continuation: %+v, requests=%d, err=%v", second, requests, err)
			}
		})
	}
}

func TestMoonshotChatRejectsOversizedPlainReasoningReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": strings.Repeat("r", 65<<10)}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "moonshotai", ID: "kimi-k2.7-code", BaseURL: server.URL, APIKey: "key"}
	_, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "read"}}, nil, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "replay text exceeds 64 KiB") {
		t.Fatalf("oversized Moonshot reasoning was accepted for a tool continuation: %v", err)
	}
}

func TestChatReplaysObservedReasoningFieldAcrossToolTurn(t *testing.T) {
	for _, check := range []struct{ providerID, wireField, replayField string }{
		{"deepseek", "reasoning_content", "reasoning_content"},
		{"custom-host", "reasoning_text", "reasoning_text"},
		{"openrouter", "reasoning", "reasoning"},
		{"alibaba", "reasoning", "reasoning_content"},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				if requests == 1 {
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
						check.wireField: "Think.",
						"tool_calls":    []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": "{}"}}},
					}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				} else if len(body.Messages) != 3 || body.Messages[1][check.replayField] != "Think." {
					t.Errorf("reasoning field %q missing: %+v", check.replayField, body.Messages)
				}
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: check.providerID, ID: "mock", BaseURL: server.URL, APIKey: "key"}
			messages := []ChatMessage{{Role: "user", Content: "read"}}
			first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(string) error { return nil })
			if err != nil || first.ReasoningText != "Think." || first.ReasoningField != check.wireField || len(first.Calls) != 1 {
				t.Fatalf("first reasoning turn: %+v, err=%v", first, err)
			}
			messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ReasoningText: first.ReasoningText, ReasoningField: first.ReasoningField}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "contents"})
			second, err := ModelTurn(context.Background(), model, messages, nil)
			if err != nil || second.Text != "Done." || requests != 2 {
				t.Fatalf("reasoning continuation: %+v, requests=%d, err=%v", second, requests, err)
			}
		})
	}
}

func TestOpenAIChatBoundsCumulativeReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": strings.Repeat("r", 90<<10)}}}})
		if err != nil {
			t.Error(err)
			return
		}
		for range 3 {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil, nil, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "reasoning exceeds") {
		t.Fatalf("oversized reasoning was accepted: %v", err)
	}
}

func TestOpenAIChatPreservesUsageAndFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.StreamOptions.IncludeUsage {
			t.Errorf("OpenAI usage request: %+v, err=%v", request, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":37,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":20},\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ProviderID: "openai", ID: "test", BaseURL: server.URL, APIKey: "key"}
	turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "done" || turn.FinishReason != "stop" || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 37, OutputTokens: 12, CacheReadTokens: 20, ReasoningTokens: 4}) {
		t.Fatalf("OpenAI Chat result metadata: %+v, err=%v", turn, err)
	}
}

func TestChatRequestsStreamUsageAcrossSharedRoutes(t *testing.T) {
	for _, providerID := range []string{"deepseek", "groq", "togetherai", "moonshotai"} {
		t.Run(providerID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					StreamOptions struct {
						IncludeUsage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.StreamOptions.IncludeUsage {
					t.Errorf("shared Chat route omitted usage request: %+v, err=%v", body, err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: providerID, ID: "test", BaseURL: server.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" {
				t.Fatalf("shared Chat route: %+v, err=%v", turn, err)
			}
		})
	}
}

func TestChatToolStrictModeMatchesSharedProtocol(t *testing.T) {
	schema := map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}
	for _, check := range []struct {
		name, providerID, baseURL string
		wantStrict                bool
	}{
		{"OpenAI", "openai", "", true},
		{"DeepSeek", "deepseek", "", true},
		{"Moonshot", "moonshotai", "", false},
		{"TogetherAI", "togetherai", "", false},
		{"Cloudflare", "cloudflare-ai-gateway", "", false},
		{"NVIDIA endpoint", "custom", "https://integrate.api.nvidia.com/v1", false},
	} {
		t.Run(check.name, func(t *testing.T) {
			model := modelcfg.Model{ProviderID: check.providerID, BaseURL: check.baseURL}
			result := chatToolSchemas(model, []map[string]any{schema})
			function, ok := result[0]["function"].(map[string]any)
			if !ok {
				t.Fatalf("missing Chat function schema: %+v", result)
			}
			value, present := function["strict"]
			if present != check.wantStrict || present && value != false {
				t.Fatalf("Chat strict mode for %s: %+v", check.name, function)
			}
			original := schema["function"].(map[string]any)
			if _, mutated := original["strict"]; mutated {
				t.Fatal("shared Agent tool schema was mutated")
			}
		})
	}
}

func TestChatStoreFlagFollowsCompatibleHosts(t *testing.T) {
	for _, check := range []struct {
		providerID string
		wantStore  bool
	}{
		{"openai", true},
		{"custom-compatible", true},
		{"deepseek", false},
		{"zai", false},
		{"moonshotai", false},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				value, present := body["store"]
				if present != check.wantStore || present && value != false {
					t.Errorf("Chat store flag for %s: %+v", check.providerID, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: check.providerID, ID: "test", BaseURL: server.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" {
				t.Fatalf("Chat store route: %+v, err=%v", turn, err)
			}
		})
	}
}

func TestChatReadsChoiceUsageAndVendorCacheFields(t *testing.T) {
	for _, check := range []struct {
		name, event string
		want        ModelUsage
		invalid     bool
	}{
		{"Moonshot choice usage", `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop","usage":{"prompt_tokens":30,"completion_tokens":8,"prompt_cache_hit_tokens":5}}]}`, ModelUsage{InputTokens: 30, OutputTokens: 8, CacheReadTokens: 5}, false},
		{"DeepSeek cache hit", `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":6,"prompt_cache_hit_tokens":4}}`, ModelUsage{InputTokens: 25, OutputTokens: 6, CacheReadTokens: 4}, false},
		{"ZAI top-level cache", `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":9,"cached_tokens":7}}`, ModelUsage{InputTokens: 40, OutputTokens: 9, CacheReadTokens: 7}, false},
		{"invalid choice usage", `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop","usage":{"prompt_tokens":30,"completion_tokens":8,"cached_tokens":-1}}]}`, ModelUsage{}, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", check.event)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if check.invalid {
				if err == nil || !strings.Contains(err.Error(), "invalid token usage") {
					t.Fatalf("invalid usage accepted: %+v, err=%v", turn, err)
				}
				return
			}
			if err != nil || turn.Usage == nil || *turn.Usage != check.want {
				t.Fatalf("Chat usage: %+v, err=%v", turn, err)
			}
		})
	}
}

func TestChatAcceptsPinnedFinishReasonAliases(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		if count == 2 {
			var body struct {
				Tools    []map[string]any `json:"tools"`
				Messages []struct {
					Role       string `json:"role"`
					ToolCallID string `json:"tool_call_id"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tools == nil || len(body.Tools) != 0 || len(body.Messages) != 3 || body.Messages[2].Role != "tool" || body.Messages[2].ToolCallID != "call_1" {
				t.Errorf("Chat alias tool continuation: %+v, err=%v", body, err)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"function_call"}]}`+"\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"end"}]}`+"\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	first, err := OpenAIChatTurn(context.Background(), model, messages, []map[string]any{{"type": "function"}})
	if err != nil || first.FinishReason != "function_call" || len(first.Calls) != 1 {
		t.Fatalf("Chat function_call alias: %+v, err=%v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file"})
	second, err := OpenAIChatTurn(context.Background(), model, messages, nil)
	if err != nil || second.Text != "Done." || second.FinishReason != "end" || requests.Load() != 2 {
		t.Fatalf("Chat end alias: %+v, requests=%d, err=%v", second, requests.Load(), err)
	}
}

func TestChatFinishFailuresDoNotReturnPartialSuccess(t *testing.T) {
	for _, check := range []struct{ reason, want string }{
		{"length", "length limit"},
		{"content_filter", "content filter"},
		{"error", "generation error"},
		{"network_error", "network error"},
	} {
		t.Run(check.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", check.reason)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "question"}}, nil)
			if err == nil || !strings.Contains(err.Error(), check.want) || turn.Text != "" {
				t.Fatalf("Chat %s finish was treated as success: %+v, err=%v", check.reason, turn, err)
			}
		})
	}
}

func TestOpenAIChatRejectsIncompleteToolAndOversizeStreams(t *testing.T) {
	for name, body := range map[string]string{
		"incomplete":       "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"tool":             "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\"}]}}]}\n\ndata: [DONE]\n\n",
		"oversize":         "data: " + strings.Repeat("x", maxEventBytes) + "\n\n",
		"late content":     "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[{\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\"late\"}]}}]}\n\ndata: [DONE]\n\n",
		"multiple choices": "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}},{\"delta\":{\"content\":\"two\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			model := modelcfg.Model{ID: "test-model", BaseURL: srv.URL, APIKey: "test-key"}
			if result, err := OpenAIChat(context.Background(), model, nil, "now"); err == nil || result != "" {
				t.Fatalf("unexpected partial success: %q, %v", result, err)
			}
		})
	}
}

func TestOpenAIChatTurnAssemblesToolArgumentsBeforeUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, fragment := range []string{`{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}`, `{"index":0,"function":{"arguments":"\"/tmp/note\"}"}}`} {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []json.RawMessage{json.RawMessage(fragment)}}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	model := modelcfg.Model{ID: "test-model", BaseURL: srv.URL, APIKey: "test-key"}
	turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "read"}}, []map[string]any{{"type": "function"}})
	if err != nil || len(turn.Calls) != 1 || turn.Calls[0].Function.Arguments != `{"path":"/tmp/note"}` {
		t.Fatalf("unexpected tool turn: %+v, %v", turn, err)
	}
}

func TestChatToolCallsAreNotLimitedByExecutionConcurrency(t *testing.T) {
	for _, count := range []int{5, 9, 32} {
		t.Run(fmt.Sprintf("calls-%d", count), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				calls := make([]map[string]any, count)
				for index := range calls {
					calls[index] = map[string]any{"index": index, "id": fmt.Sprintf("call_%d", index), "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}
				}
				event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", event)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "read"}}, []map[string]any{{"type": "function"}})
			if err != nil || len(turn.Calls) != count {
				t.Fatalf("Chat lost %d calls: %+v, err=%v", count, turn, err)
			}
		})
	}
}

func TestChatToolCallFragmentsHaveAnAggregateByteLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for index := 0; index < 32; index++ {
			// Each call and each SSE event are individually within their byte limits.
			arguments := `{"text":"` + strings.Repeat("x", 8192) + `"}`
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": fmt.Sprintf("call_%d", index), "type": "function", "function": map[string]any{"name": "read_file", "arguments": arguments}}}}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	_, err := OpenAIChatTurn(context.Background(), modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "read"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "tool data exceeds 256 KiB") {
		t.Fatalf("oversized aggregate tool data accepted: %v", err)
	}
}

func TestGroqReasoningFormatFollowsPinnedRoute(t *testing.T) {
	var formats []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ReasoningFormat string `json:"reasoning_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		formats = append(formats, request.ReasoningFormat)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	for _, id := range []string{"llama-3.3-70b-versatile", "openai/gpt-oss-120b"} {
		model := modelcfg.Model{Provider: "openai-chat", ProviderID: "groq", ID: id, BaseURL: srv.URL + "/v1", APIKey: "test-key"}
		if _, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(formats) != 2 || formats[0] != "parsed" || formats[1] != "" {
		t.Fatalf("Groq reasoning formats: %q", formats)
	}
}

func TestOpenRouterRequestsUsageWithoutChangingChatStream(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Usage struct {
				Include bool `json:"include"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Usage.Include {
			t.Errorf("OpenRouter usage request: %+v, %v", request, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	for _, id := range []string{"openrouter", "standardcompute"} {
		model := modelcfg.Model{Provider: "openai-chat", ProviderID: id, ID: "test-model", BaseURL: srv.URL, APIKey: "key"}
		turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
		if err != nil || turn.Text != "ok" {
			t.Fatalf("OpenRouter package %s: %+v %v", id, turn, err)
		}
	}
	if requests != 2 {
		t.Fatalf("OpenRouter package requests: %d", requests)
	}
}

func TestCustomChatStreamCitationsAreBoundedSources(t *testing.T) {
	for _, check := range []struct {
		name      string
		citations []string
		want      int
		failure   bool
	}{
		{"deduplicated", []string{"https://example.com/a", "https://example.com/a", "http://example.org/b"}, 2, false},
		{"unsafe scheme", []string{"javascript:alert(1)"}, 0, true},
		{"too many", func() []string {
			items := make([]string, 17)
			for i := range items {
				items[i] = fmt.Sprintf("https://example.com/%d", i)
			}
			return items
		}(), 16, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"citations": check.citations, "choices": []any{map[string]any{"delta": map[string]string{"content": "ok"}, "finish_reason": "stop"}}})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				chunk, _ = json.Marshal(map[string]any{"citations": check.citations, "choices": []any{}})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: "custom", ID: "sonar", BaseURL: srv.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "question"}}, nil)
			if check.failure {
				if err == nil || !strings.Contains(err.Error(), "citations") {
					t.Fatalf("bad citations accepted: %+v %v", turn, err)
				}
				return
			}
			if err != nil || len(turn.Sources) != check.want {
				t.Fatalf("Perplexity sources: %+v %v", turn.Sources, err)
			}
			if check.name == "deduplicated" && (turn.Sources[0] != "https://example.com/a" || turn.Sources[1] != "http://example.org/b") {
				t.Fatalf("Perplexity sources were not deduplicated: %+v", turn.Sources)
			}
		})
	}
}

func TestOpenRouterStreamAnnotationsBecomeBoundedSources(t *testing.T) {
	for _, providerID := range []string{"openrouter", "standardcompute"} {
		t.Run(providerID, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Cited answer","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/a","title":"One"}},{"type":"other"}]},"message":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/a"}},{"type":"url_citation","url_citation":{"url":"http://example.org/b"}}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: providerID, ID: "mock", BaseURL: srv.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "question"}}, nil)
			if err != nil || turn.Text != "Cited answer" || len(turn.Sources) != 2 || turn.Sources[0] != "https://example.com/a" || turn.Sources[1] != "http://example.org/b" {
				t.Fatalf("OpenRouter annotations: %+v, err=%v", turn, err)
			}
		})
	}
	for _, check := range []struct {
		name        string
		annotations []map[string]any
		want        int
		failure     bool
	}{
		{"unsafe URL", []map[string]any{{"type": "url_citation", "url_citation": map[string]any{"url": "javascript:alert(1)"}}}, 0, true},
		{"missing URL", []map[string]any{{"type": "url_citation"}}, 0, true},
		{"aggregate bytes", func() []map[string]any {
			items := make([]map[string]any, 5)
			for i := range items {
				items[i] = map[string]any{"type": "url_citation", "url_citation": map[string]any{"url": fmt.Sprintf("https://example.com/%d/", i) + strings.Repeat("a", 1900)}}
			}
			return items
		}(), 0, true},
		{"too many", func() []map[string]any {
			items := make([]map[string]any, 17)
			for i := range items {
				items[i] = map[string]any{"type": "url_citation", "url_citation": map[string]any{"url": fmt.Sprintf("https://example.com/%d", i)}}
			}
			return items
		}(), 16, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "answer", "annotations": check.annotations}, "finish_reason": "stop"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer srv.Close()
			model := modelcfg.Model{Provider: "openai-chat", ProviderID: "openrouter", ID: "mock", BaseURL: srv.URL, APIKey: "key"}
			turn, err := OpenAIChatTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "question"}}, nil)
			if check.name == "too many" {
				if err != nil || len(turn.Sources) != 16 {
					t.Fatalf("citation overflow failed instead of truncating: sources=%d err=%v", len(turn.Sources), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "citations") {
				t.Fatalf("bad OpenRouter annotation accepted: %v", err)
			}
		})
	}
}
