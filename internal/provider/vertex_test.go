package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestVertexNativeRoutes(t *testing.T) {
	for _, check := range []struct {
		name, protocol, modelID, basePath, route, stream string
	}{
		{"gemini", "vertex-gemini", "gemini-2.5-flash", "/v1beta1/projects/p/locations/us-central1/publishers/google", "/models/gemini-2.5-flash:streamGenerateContent", `data: {"candidates":[{"content":{"parts":[{"text":"vertex done"}]},"finishReason":"STOP"}]}` + "\n\n"},
		{"anthropic", "vertex-anthropic", "claude-haiku-4-5@20251001", "/v1/projects/p/locations/us-central1/publishers/anthropic", "/models/claude-haiku-4-5@20251001:streamRawPredict", `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"vertex done"}}` + "\n\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n"},
		{"maas", "openai-chat", "openai/gpt-oss-120b-maas", "/v1/projects/p/locations/us-central1/endpoints/openapi", "/chat/completions", `data: {"choices":[{"delta":{"content":"vertex done"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"},
	} {
		t.Run(check.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != check.basePath+check.route || r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("x-goog-api-key") != "" || r.Header.Get("x-api-key") != "" {
					t.Errorf("Vertex request: %s %s, headers=%+v", r.Method, r.URL, r.Header)
				}
				if check.name == "gemini" && r.URL.Query().Get("alt") != "sse" {
					t.Errorf("Vertex Gemini stream query: %s", r.URL)
				}
				if check.name == "anthropic" && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("Vertex Anthropic version header: %q", r.Header.Get("anthropic-version"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if check.name == "anthropic" {
					if body["model"] != nil || body["anthropic_version"] != "vertex-2023-10-16" || body["stream"] != true {
						t.Errorf("Vertex Anthropic body: %+v", body)
					}
				} else if check.name == "maas" && (body["model"] != check.modelID || body["stream"] != true) {
					t.Errorf("Vertex MaaS body: %+v", body)
				} else if check.name == "gemini" && body["contents"] == nil {
					t.Errorf("Vertex Gemini body: %+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, check.stream)
			}))
			defer endpoint.Close()
			model := modelcfg.Model{Provider: check.protocol, ProviderID: "google-vertex", ID: check.modelID, BaseURL: endpoint.URL + check.basePath, APIKey: "access-token"}
			turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || strings.TrimSpace(turn.Text) != "vertex done" {
				t.Fatalf("Vertex turn: %+v, err=%v", turn, err)
			}
		})
	}
}

func TestVertexGeminiExpressModeAPIKey(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent" || r.Header.Get("x-goog-api-key") != "express-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("Vertex Express Mode request: %s, headers=%+v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"express done"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "vertex-gemini", ID: "gemini-2.5-flash", BaseURL: endpoint.URL + "/v1/publishers/google", APIKey: "express-key", VertexAPIKey: true}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "express done" {
		t.Fatalf("Vertex Express Mode turn: %+v, err=%v", turn, err)
	}
}
