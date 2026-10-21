package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestMistralImageRequestShapeAndLimits(t *testing.T) {
	image := testImage(t)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image.Data)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Mistral image route: %s", r.URL.Path)
		}
		var request struct {
			Messages []struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" || len(request.Messages[0].Content) != 4 || request.Messages[0].Content[0]["type"] != "text" || request.Messages[0].Content[0]["text"] != "describe" || request.Messages[0].Content[1]["type"] != "image_url" || request.Messages[0].Content[1]["image_url"] != dataURL {
			t.Errorf("Mistral image parts: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "mistral", ID: "mock", BaseURL: server.URL + "/v1", APIKey: "key"}
	message := ChatMessage{Role: "user", Content: "describe", Images: []Image{image, image, image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || calls != 1 {
		t.Fatalf("Mistral image turn: %+v, calls=%d, err=%v", turn, calls, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "1 MiB") || calls != 1 {
		t.Fatalf("Mistral accepted oversized image request: %v, calls=%d", err, calls)
	}

}

func TestMistralStreamsStructuredThinkingSeparately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"Check "},{"text":"inputs."}]},{"type":"text","text":"Done."}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "mistral", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "solve"}}
	var text, reasoning strings.Builder
	turn, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil,
		func(fragment string) error { _, err := text.WriteString(fragment); return err },
		func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Done." || text.String() != "Done." || reasoning.String() != "Check inputs." || len(turn.MistralParts) != 2 || turn.MistralParts[0] != (MistralPart{Type: "thinking", Text: "Check inputs."}) || turn.MistralParts[1] != (MistralPart{Type: "text", Text: "Done."}) {
		t.Fatalf("Mistral thinking: turn=%+v text=%q reasoning=%q err=%v", turn, text.String(), reasoning.String(), err)
	}
	if _, err := MistralTurn(context.Background(), model, messages, nil); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("Mistral thinking was silently dropped: %v", err)
	}
	abort := errors.New("reasoning event failed")
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("Mistral reasoning sink error was not propagated: %v", err)
	}
}

func TestMistralBoundsCumulativeThinking(t *testing.T) {
	var stream strings.Builder
	for range 3 {
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": []any{map[string]any{"type": "thinking", "thinking": []any{map[string]any{"text": strings.Repeat("r", 90<<10)}}}}}}}})
		fmt.Fprintf(&stream, "data: %s\n\n", chunk)
	}
	stream.WriteString("data: [DONE]\n\n")
	_, err := parseMistralStreamWithReasoning(strings.NewReader(stream.String()), nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "reasoning exceeds") {
		t.Fatalf("oversized Mistral thinking accepted: %v", err)
	}
}

func TestMistralNativeToolAndStructuredTextRoundTrip(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer mistral-key" {
			t.Errorf("wrong Mistral route or key: %s", r.URL)
		}
		var request struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role       string `json:"role"`
				Name       string `json:"name"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Strict bool   `json:"strict"`
					Name   string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "codestral-latest" || !request.Stream || len(request.Tools) != 1 || request.Tools[0].Type != "function" || request.Tools[0].Function.Name != "read_file" || request.Tools[0].Function.Strict {
			t.Errorf("wrong Mistral native request: %+v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-abc\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"/tmp/a\\\"}\"}}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if len(request.Messages) != 4 || request.Messages[2].Role != "assistant" || request.Messages[3].Role != "tool" || request.Messages[3].Name != "read_file" || len(request.Messages[2].ToolCalls) != 1 || request.Messages[2].ToolCalls[0].ID != request.Messages[3].ToolCallID || request.Messages[3].ToolCallID != "lg1gjqg4a" {
			t.Errorf("Mistral tool continuation: %+v", request.Messages)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"Read \"}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":23,\"completion_tokens\":11,\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "mistral", ID: "codestral-latest", BaseURL: server.URL + "/v1", APIKey: "mistral-key"}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object"}}}}
	messages := []ChatMessage{{Role: "system", Content: "Be useful"}, {Role: "user", Content: "read"}}
	first, err := MistralTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].Function.Arguments != `{"path":"/tmp/a"}` {
		t.Fatalf("Mistral native tool turn: %+v %v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file data"})
	var fragments []string
	second, err := mistralTurn(context.Background(), model, messages, schemas, func(part string) error { fragments = append(fragments, part); return nil })
	if err != nil || second.Text != "Read complete" || second.FinishReason != "stop" || second.Usage == nil || *second.Usage != (ModelUsage{InputTokens: 23, OutputTokens: 11, CacheReadTokens: 4, ReasoningTokens: 2}) || strings.Join(fragments, "") != second.Text || calls != 2 {
		t.Fatalf("Mistral native text turn: %+v, fragments=%q, calls=%d, err=%v", second, fragments, calls, err)
	}
}

func TestMistralToolIDsMatchPinnedNormalizer(t *testing.T) {
	for source, want := range map[string]string{
		"call-abc":    "lg1gjqg4a",
		"generated-0": "k71ghr9nt",
		"😀":           "9k07i8epq",
		"Abc123XYZ":   "Abc123XYZ",
	} {
		got := mistralToolIDNormalizer([]string{source})(source)
		if got != want {
			t.Errorf("Mistral ID %q = %q, want %q", source, got, want)
		}
	}
	call := func(id string) ToolCall {
		var result ToolCall
		result.ID, result.Type = id, "function"
		result.Function.Name, result.Function.Arguments = "read_file", `{}`
		return result
	}
	messages := []ChatMessage{
		{Role: "assistant", ToolCalls: []ToolCall{call("call-abc")}},
		{Role: "tool", ToolCallID: "call-abc", Content: "first"},
		{Role: "assistant", ToolCalls: []ToolCall{call("lg1gjqg4a")}},
		{Role: "tool", ToolCallID: "lg1gjqg4a", Content: "second"},
	}
	converted, err := mistralMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	first := converted[0]["tool_calls"].([]map[string]any)[0]["id"]
	second := converted[2]["tool_calls"].([]map[string]any)[0]["id"]
	if first != "qp1iww77n" || converted[1]["tool_call_id"] != first || second != "lg1gjqg4a" || converted[3]["tool_call_id"] != second {
		t.Fatalf("Mistral ID collision or continuation: %+v", converted)
	}
}

func TestMistralRejectsUnfinishedAndInvalidOutput(t *testing.T) {
	for _, test := range []struct{ name, stream, reason string }{
		{"unfinished", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "without a complete response"},
		{"reasoning requires sink", "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"text\":\"private\"}]}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "requires a sink"},
		{"bad args", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"abcdefghi\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", "invalid tool arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, test.stream)
			}))
			defer server.Close()
			_, err := MistralTurn(context.Background(), modelcfg.Model{Provider: "mistral", ID: "test", BaseURL: server.URL, APIKey: "key"}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("expected %q, got %v", test.reason, err)
			}
		})
	}
}
