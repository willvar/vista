package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestProviderStreamErrorsPreserveExplanationAndRedactCredentials(t *testing.T) {
	for _, check := range []struct {
		protocol, event, code string
	}{
		{"openai-chat", `{"error":{"code":"rate_limit_error","message":"Access denied for fixture-stream-key."}}`, "rate_limit_error"},
		{"openai-responses", `{"type":"response.failed","response":{"error":{"code":"rate_limit_error","message":"Access denied for fixture-stream-key."}}}`, "rate_limit_error"},
		{"openai-responses", `{"type":"error","error":{"code":"authentication_error","message":"Access denied for fixture-stream-key."}}`, "authentication_error"},
		{"anthropic", `{"type":"error","error":{"type":"authentication_error","message":"Access denied for fixture-stream-key."}}`, "authentication_error"},
		{"gemini", `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Access denied for fixture-stream-key."}}`, "RESOURCE_EXHAUSTED"},
		{"mistral", `{"error":{"code":"rate_limit_error","message":"Access denied for fixture-stream-key."}}`, "rate_limit_error"},
	} {
		t.Run(check.protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", check.event)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ID: "mock", BaseURL: server.URL, APIKey: "fixture-stream-key"}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || !strings.Contains(err.Error(), check.code) || !strings.Contains(err.Error(), "Access denied for [redacted].") || strings.Contains(err.Error(), model.APIKey) {
				t.Fatalf("stream error details missing or credentials leaked: %v", err)
			}
		})
	}
}

func TestOpenAIChatUnknownStreamErrorCodeIsVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"error":{"code":"FreeTierError","message":"Access denied for fixture-stream-key."}}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "fixture-stream-key"}
	_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "FreeTierError") || !strings.Contains(err.Error(), "Access denied for [redacted].") || strings.Contains(err.Error(), model.APIKey) {
		t.Fatalf("unknown Chat error was hidden or credentials leaked: %v", err)
	}
}

func TestUnknownStreamLabelsDoNotEchoUntrustedValues(t *testing.T) {
	for _, check := range []struct{ protocol, events string }{
		{"openai-chat", `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"secret-stream-label"}]}` + "\n\ndata: [DONE]\n\n"},
		{"openai-responses", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"secret-stream-label"}}}` + "\n\n"},
		{"anthropic", `data: {"type":"secret-stream-label"}` + "\n\n"},
		{"gemini", `data: {"candidates":[{"finishReason":"secret-stream-label"}]}` + "\n\n"},
		{"mistral", `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"secret-stream-label"}]}` + "\n\ndata: [DONE]\n\n"},
		{"cohere", `data: {"type":"secret-stream-label"}` + "\n\n"},
		{"vercel-gateway", `data: {"type":"secret-stream-label"}` + "\n\n"},
	} {
		t.Run(check.protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, check.events)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ID: "mock", BaseURL: server.URL, APIKey: "key"}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || strings.Contains(err.Error(), "secret-stream-label") {
				t.Fatalf("unknown stream label was accepted or echoed: %v", err)
			}
		})
	}
}

func TestModelTurnStreamReportsProvisionalTextAndSinkErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"openai-chat", "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"openai-responses", "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"visible\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"anthropic", "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"visible\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"gemini", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible\"}]},\"finishReason\":\"STOP\"}]}\n\n"},
		{"mistral", "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: test.name, ID: "mock", BaseURL: server.URL, APIKey: "test"}
			var fragments []string
			turn, err := ModelTurnStream(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil, func(part string) error {
				fragments = append(fragments, part)
				return nil
			})
			if err != nil || turn.Text != "visible" || len(fragments) != 1 || fragments[0] != "visible" {
				t.Fatalf("streamed turn: %+v, fragments=%q, err=%v", turn, fragments, err)
			}
			abort := errors.New("persistence failed")
			if _, err := ModelTurnStream(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil, func(string) error { return abort }); !errors.Is(err, abort) {
				t.Fatalf("sink error was not propagated: %v", err)
			}
		})
	}
}
