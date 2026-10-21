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

func TestCloudflareGatewayRoutesKeepCredentialsOnGateway(t *testing.T) {
	for _, check := range []struct{ protocol, model, wire, path, stream string }{
		{"openai-chat", "alibaba/qwen3-max", "alibaba/qwen3-max", "/ai/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"openai-responses", "openai/gpt-4.1-mini", "openai/gpt-4.1-mini", "/ai/v1/responses", "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"anthropic", "anthropic/claude-opus-4.5", "anthropic/claude-opus-4-5", "/ai/v1/messages", "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(check.protocol, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer gateway-secret" || r.Header.Get("cf-aig-gateway-id") != "gateway-one" || r.Header.Get("x-api-key") != "" {
					t.Errorf("Cloudflare route or auth: %s, %+v", r.URL, r.Header)
				}
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != check.wire {
					t.Errorf("Cloudflare wire model %q, %v", request.Model, err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, check.stream)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ProviderID: "cloudflare-ai-gateway", ID: check.model, BaseURL: server.URL + "/ai/v1", APIKey: "gateway-secret", GatewayID: "gateway-one"}
			turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" || requests != 1 {
				t.Fatalf("Cloudflare turn: %+v, requests=%d, err=%v", turn, requests, err)
			}
			model.BaseURL = ""
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err == nil || !strings.Contains(err.Error(), "configured base URL") {
				t.Fatalf("Cloudflare key could reach upstream default: %v", err)
			}
		})
	}
}
