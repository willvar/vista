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

func TestOverflowEvidenceDoesNotGuessFromStatusOrNormalContent(t *testing.T) {
	for _, text := range []string{
		"400 (no body)", "413 status code (no body)", "too many tokens", "token limit exceeded",
		"Request exceeds the maximum size", "Rate limit: prompt too long", "Throttling error: too many tokens",
		`{"error":{"code":"authentication_error","message":"prompt is too long"}}`,
		`{"error":{"param":"messages[0].role","message":"Invalid role: prompt is too long"}}`,
		`{"error":{"param":"max_output_tokens","message":"exceeds the model's maximum context length"}}`,
		`{"choices":[{"message":{"content":"prompt is too long"}}]}`,
		`{"usage":{"input_tokens":99999999},"finish_reason":"length"}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
	} {
		if errorEnvelopeOverflow([]byte(text)) {
			t.Errorf("misclassified %s", text)
		}
	}
	for _, text := range []string{
		"This endpoint's maximum context length is 128000 tokens. However, you requested about 150000 tokens.",
		"Input length (265330) exceeds model's maximum context length (262144).",
		"prompt token count of 12000 exceeds the limit of 8000",
		"prompt (20000 tokens) + max tokens (4000) exceeds the context (16000)",
		"prompt (20000 tokens) leaves no room to answer in the context (16000)",
		"Range of input length should be [1, 32000]",
		"prompt 超长",
	} {
		if !errorEnvelopeOverflow([]byte(text)) {
			t.Errorf("missed explicit evidence: %s", text)
		}
	}
	inner := `{"error":{"message":"prompt is too long"}}`
	outer, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "metadata": map[string]any{"raw": inner}}})
	if !errorEnvelopeOverflow(outer) {
		t.Fatal("lost OpenRouter nested upstream error")
	}
}

func TestLargeGatewayErrorStillClassifiesBeyondOldFourKiBBoundary(t *testing.T) {
	body := `{"error":{"message":"` + strings.Repeat("gateway attempt; ", 400) + `prompt is too long"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); fmt.Fprint(w, body) }))
	defer server.Close()
	_, err := ModelTurn(context.Background(), modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "fixture"}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if !IsContextOverflow(err) {
		t.Fatalf("lost long gateway error: %v", err)
	}
}

// Source-shaped official and gateway fixtures, not live traffic measurements.
// Provenance and deliberate exclusions are documented in docs/context-compaction.md.
func TestContextOverflowAdapters(t *testing.T) {
	cases := []struct {
		name, protocol string
		status         int
		sse            bool
		body           string
		confirmed      bool
	}{
		{"openai_http_code", "openai-chat", 400, false, `{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"Your input exceeds the context window of this model."}}`, true},
		{"newapi_status_mapped_500", "openai-chat", 500, false, `{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"Your input exceeds the context window of this model."}}`, true},
		{"openai_message_without_code", "openai-responses", 400, false, `{"error":{"type":"invalid_request_error","message":"Your input exceeds the context window of this model."}}`, true},
		{"claude_http_prompt_length", "anthropic", 400, false, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`, true},
		{"gemini_http_input_tokens", "gemini", 400, false, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)."}}`, true},
		{"newapi_claude_to_chat", "openai-chat", 400, false, `{"error":{"code":"invalid_request_error","type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`, true},
		{"litellm_wrapped_error", "openai-chat", 400, false, `{"error":{"code":"400","type":"invalid_request_error","message":"litellm.ContextWindowExceededError: AnthropicError - prompt is too long: 213462 tokens > 200000 maximum"}}`, true},
		{"openai_sse_failed_code", "openai-responses", 200, true, `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`, true},
		{"openai_sse_error_code", "openai-responses", 200, true, `{"type":"error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}`, true},
		{"chat_sse_error_code", "openai-chat", 200, true, `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`, true},
		{"claude_sse_error", "anthropic", 200, true, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`, true},
		{"gemini_sse_error", "gemini", 200, true, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)."}}`, true},
		{"claude_context_stop", "anthropic", 200, true, `{"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"}}
{"type":"message_stop"}`, true},
		{"newapi_status_mapped_200_json", "openai-chat", 200, false, `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`, true},
		{"newapi_nonjson_body_hidden", "openai-chat", 400, false, `{"error":{"type":"bad_response_status_code","code":"bad_response_status_code","message":"bad response status code 400"}}`, false},
		{"herdex_precontent_code_in_message", "openai-responses", 503, false, `{"error":{"type":"server_error","message":"all pool attempts failed; last: fixture-account: context_length_exceeded"}}`, true},
		{"herdex_precontent_message_erased", "openai-responses", 503, false, `{"error":{"type":"server_error","message":"all pool attempts failed; last: fixture-account: invalid_request_error"}}`, false},
		{"output_parameter_error", "openai-chat", 400, false, `{"error":{"type":"invalid_request_error","param":"max_completion_tokens","message":"max_completion_tokens must be less than or equal to 4096"}}`, false},
		{"rate_limit", "gemini", 429, false, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limit exceeded for tokens per minute"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.sse {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				if tc.sse {
					for _, event := range strings.Split(tc.body, "\n") {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				} else {
					fmt.Fprint(w, tc.body)
				}
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: tc.protocol, ID: "mock", BaseURL: server.URL, APIKey: "local-fixture"}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "small local fixture"}}, nil)
			var typed *ModelHTTPError
			detected := errors.As(err, &typed) && typed.Code == "context_length_exceeded"
			t.Logf("current-overflow=%v error=%v", detected, err)
			if detected != tc.confirmed {
				t.Fatalf("unexpected baseline behavior: got %v, want %v", detected, tc.confirmed)
			}
		})
	}
}
