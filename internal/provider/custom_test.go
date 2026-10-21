package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func customReply(w http.ResponseWriter, protocol string) {
	w.Header().Set("Content-Type", "text/event-stream")
	switch protocol {
	case "openai-chat":
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	case "openai-responses":
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg1\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
	case "anthropic":
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	case "cohere":
		_, _ = w.Write([]byte("data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"ok\"}}}}\n\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\"}}\n\n"))
	case "gemini":
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n"))
	}
}

func TestCustomAPIFormatAndAuthenticationAreIndependent(t *testing.T) {
	paths := map[string]string{"openai-chat": "/v1/chat/completions", "openai-responses": "/v1/responses", "anthropic": "/v1/messages", "gemini": "/v1beta/models/test-model:streamGenerateContent", "cohere": "/v2/chat"}
	for _, protocol := range CustomProtocols() {
		for _, authentication := range []string{"none", "bearer", "x-api-key", "x-goog-api-key"} {
			t.Run(protocol+"/"+authentication, func(t *testing.T) {
				requests := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodPost || r.URL.Path != paths[protocol] || r.Header.Get("X-Custom") != "custom-value" {
						t.Errorf("wrong method, path, or headers: %s %s", r.Method, r.URL.Path)
					}
					for _, name := range []string{"Authorization", "x-api-key", "x-goog-api-key"} {
						want := ""
						if name == authentication {
							want = "test-key"
						}
						if name == "Authorization" && authentication == "bearer" {
							want = "Bearer test-key"
						}
						if got := r.Header.Get(name); got != want {
							t.Errorf("%s authentication header %s: got %q want %q", protocol, name, got, want)
						}
					}
					customReply(w, protocol)
				}))
				defer upstream.Close()
				key := "test-key"
				if authentication == "none" {
					key = ""
				}
				model := modelcfg.Model{Custom: true, ProviderID: "custom", Provider: protocol, Authentication: authentication, ID: "test-model", BaseURL: upstream.URL, APIKey: key, Headers: map[string]string{"X-Custom": "custom-value"}}
				for _, purpose := range []RequestPurpose{PurposeConversation, PurposeTitle, PurposeCompaction} {
					var streamed strings.Builder
					turn, err := ModelTurnStream(context.Background(), ForPurpose(model, purpose), []ChatMessage{{Role: "user", Content: "Hi"}}, nil, func(s string) error { streamed.WriteString(s); return nil })
					if err != nil || turn.Text != "ok" || streamed.String() != "ok" {
						t.Fatalf("%s turn=%+v stream=%q err=%v", purpose, turn, streamed.String(), err)
					}
				}
				if requests != 3 {
					t.Fatalf("unexpected requests: %d", requests)
				}
			})
		}
	}
}

func TestCustomModelNamesDoNotChangeAPIFormat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model name changed route: %s", r.URL.Path)
		}
		customReply(w, "openai-chat")
	}))
	defer upstream.Close()
	for _, id := range []string{"claude-sonnet-4-5", "gemini-3-pro", "gpt-6.1-sol"} {
		model := modelcfg.Model{Custom: true, ProviderID: "custom", Provider: "openai-chat", Authentication: "none", ID: id, BaseURL: upstream.URL}
		if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "Hi"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCustomFailuresNeverSwitchProtocol(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 415, 422, 429, 501} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/responses" {
					t.Errorf("switched protocol: %s", r.URL.Path)
				}
				http.Error(w, `{"error":{"code":"unsupported_endpoint"}}`, status)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Custom: true, ProviderID: "custom", Provider: "openai-responses", Authentication: "none", ID: "test", BaseURL: upstream.URL}
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "Hi"}}, nil); err == nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestCustomResponseToolContinuationRetainsProtocolAndReasoning(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/responses" {
			t.Errorf("continuation changed route: %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "low" {
			t.Errorf("reasoning lost: %+v", body["reasoning"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"r1\",\"encrypted_content\":\"opaque\",\"summary\":[]},{\"type\":\"function_call\",\"id\":\"f1\",\"call_id\":\"c1\",\"name\":\"read\",\"arguments\":\"{}\"}]}}\n\n"))
		} else {
			data, _ := json.Marshal(body["input"])
			if !strings.Contains(string(data), "opaque") || !strings.Contains(string(data), "function_call_output") {
				t.Error("lost opaque state or tool result")
			}
			customReply(w, "openai-responses")
		}
	}))
	defer upstream.Close()
	model := modelcfg.Model{Custom: true, ProviderID: "custom", Authentication: "none", Provider: "openai-responses", ID: "gpt-6.1-sol", BaseURL: upstream.URL}
	if err := ApplyReasoningEffort(&model, "low"); err != nil {
		t.Fatal(err)
	}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read", "description": "read", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}}
	turn, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(turn.Calls) != 1 {
		t.Fatalf("first turn=%+v err=%v", turn, err)
	}
	messages = append(messages, turn.AssistantMessage(""), ChatMessage{Role: "tool", ToolCallID: "c1", Content: "contents"})
	turn, err = ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || turn.Text != "ok" || calls != 2 {
		t.Fatalf("continuation=%+v err=%v calls=%d", turn, err, calls)
	}
}

func TestCustomHeadersOverrideSelectedAuthentication(t *testing.T) {
	for _, protocol := range CustomProtocols() {
		t.Run(protocol, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Custom token" || r.Header.Get("X-Api-Key") != "header-key" || r.Header.Get("X-Goog-Api-Key") != "" {
					t.Error("selected authentication or overrides were replaced by API defaults")
				}
				customReply(w, protocol)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Custom: true, ProviderID: "custom", Provider: protocol, Authentication: "bearer", ID: "alias", BaseURL: upstream.URL, APIKey: "default-key", Headers: map[string]string{"authorization": "Custom token", "x-api-key": "header-key"}}
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "Hi"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomValidationAndURLPrefixes(t *testing.T) {
	for protocol, path := range map[string]string{"openai-chat": "/proxy/chat/completions", "openai-responses": "/proxy/responses", "anthropic": "/proxy/messages", "gemini": "/proxy/models/alias:streamGenerateContent", "cohere": "/proxy/chat"} {
		t.Run(protocol, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path {
					t.Errorf("inference changed explicit prefix: %s", r.URL.Path)
				}
				customReply(w, protocol)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Custom: true, ProviderID: "custom", Provider: protocol, Authentication: "none", ID: "alias", BaseURL: upstream.URL + "/proxy/"}
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "Hi"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, test := range []struct{ base, protocol, want string }{
		{"http://localhost:8000", "openai-chat", "http://localhost:8000/v1"},
		{"https://example.test/api/v1", "anthropic", "https://example.test/api/v1"},
		{"http://192.168.1.2:8000/v1", "gemini", "http://192.168.1.2:8000/v1"},
		{"https://example.test/custom", "openai-chat", "https://example.test/custom"},
		{"https://example.test", "cohere", "https://example.test/v2"},
		{"https://example.test", "gemini", "https://example.test/v1beta"},
	} {
		if got := CustomBaseURL(test.base, test.protocol); got != test.want {
			t.Errorf("%s => %s", test.base, got)
		}
	}
	for _, headers := range []map[string]string{{"Authorization": "one", "authorization": "two"}, {"X-Test": "a\r\nb"}, {"Host": "override"}} {
		if ValidateCustomHeaders(headers) == nil {
			t.Error("accepted invalid headers")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ModelTurn(ctx, modelcfg.Model{Custom: true, Provider: "openai-chat", Authentication: "none", ID: "alias", BaseURL: "http://localhost:1"}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, authentication := range []string{"", "apiKey", "automatic"} {
		if ValidateCustomAuthentication(authentication, "key") == nil {
			t.Error("accepted implicit authentication")
		}
	}
}
