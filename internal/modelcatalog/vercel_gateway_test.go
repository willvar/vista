package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/provider"
)

func TestVercelGatewayPinnedV3RouteAndToolContinuation(t *testing.T) {
	const modelID = "alibaba/qwen-3-14b"
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	t.Setenv("AI_GATEWAY_API_KEY", "private-gateway-key")
	data := []byte(`{"default":"gateway","models":[{"key":"gateway","provider":"catalog","providerId":"vercel","id":"alibaba/qwen-3-14b"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "vercel-gateway" || model.BaseURL != "https://ai-gateway.vercel.sh/v3/ai" || model.APIKey != "private-gateway-key" || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "AI_GATEWAY_API_KEY" {
		t.Fatalf("Vercel Gateway catalog: %+v, env=%q, err=%v", model, catalog.CredentialEnvironment(), err)
	}
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v3/ai/language-model" || r.Header.Get("Authorization") != "Bearer private-gateway-key" || r.Header.Get("ai-language-model-specification-version") != "3" || r.Header.Get("ai-language-model-id") != modelID || r.Header.Get("ai-language-model-streaming") != "true" {
			t.Errorf("Vercel Gateway request: %s, headers=%v", r.URL, r.Header)
		}
		var body struct {
			Prompt []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"prompt"`
			Tools []struct {
				Type        string         `json:"type"`
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "" || len(body.Tools) != 1 || body.Tools[0].Name != "read_file" || body.Tools[0].Type != "function" || body.Tools[0].InputSchema["type"] != "object" {
			t.Errorf("Vercel Gateway body: %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			if len(body.Prompt) != 1 || body.Prompt[0].Role != "user" {
				t.Errorf("Vercel Gateway initial prompt: %+v", body.Prompt)
			}
			_, _ = fmt.Fprint(w, "data: {\"type\":\"tool-input-start\",\"id\":\"call_1\",\"toolName\":\"read_file\"}\n\ndata: {\"type\":\"tool-input-delta\",\"id\":\"call_1\",\"delta\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}\n\ndata: {\"type\":\"tool-input-end\",\"id\":\"call_1\"}\n\ndata: {\"type\":\"tool-call\",\"toolCallId\":\"call_1\",\"toolName\":\"read_file\",\"input\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}\n\ndata: {\"type\":\"finish\",\"finishReason\":{\"unified\":\"tool-calls\"},\"usage\":{}}\n\n")
			return
		}
		if len(body.Prompt) != 3 || body.Prompt[1].Role != "assistant" || body.Prompt[2].Role != "tool" {
			t.Errorf("Vercel Gateway continuation prompt: %+v", body.Prompt)
		}
		var assistant []map[string]any
		var tool []map[string]any
		_ = json.Unmarshal(body.Prompt[1].Content, &assistant)
		_ = json.Unmarshal(body.Prompt[2].Content, &tool)
		if len(assistant) != 1 || assistant[0]["type"] != "tool-call" || assistant[0]["toolCallId"] != "call_1" || len(tool) != 1 || tool[0]["type"] != "tool-result" || tool[0]["toolName"] != "read_file" {
			t.Errorf("Vercel Gateway tool history: assistant=%+v, tool=%+v", assistant, tool)
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"text-start\",\"id\":\"text_1\"}\n\ndata: {\"type\":\"text-delta\",\"id\":\"text_1\",\"delta\":\"Gateway reply\"}\n\ndata: {\"type\":\"text-end\",\"id\":\"text_1\"}\n\ndata: {\"type\":\"finish\",\"finishReason\":{\"unified\":\"stop\"},\"usage\":{}}\n\n")
	}))
	defer endpoint.Close()
	data = []byte(fmt.Sprintf(`{"default":"gateway","models":[{"key":"gateway","provider":"catalog","providerId":"vercel","id":%q,"baseUrl":%q,"apiKey":"private-gateway-key"}]}`, modelID, endpoint.URL+"/v3/ai"))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = catalog.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	messages := []provider.ChatMessage{{Role: "user", Content: "read"}}
	first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" {
		t.Fatalf("Vercel Gateway tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Gateway reply" || requests != 2 {
		t.Fatalf("Vercel Gateway continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}
