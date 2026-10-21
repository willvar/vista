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

func TestVeniceCatalogTextAndToolContinuation(t *testing.T) {
	const modelID = "aion-labs-aion-3-0"
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	t.Setenv("VENICE_API_KEY", "private-venice-key")
	data := []byte(`{"default":"venice","models":[{"key":"venice","provider":"catalog","providerId":"venice","id":"aion-labs-aion-3-0"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-chat" || model.BaseURL != "https://api.venice.ai/api/v1" || model.APIKey != "private-venice-key" || model.Tools == nil || !*model.Tools || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "VENICE_API_KEY" {
		t.Fatalf("Venice catalog: %+v, env=%q, err=%v", model, catalog.CredentialEnvironment(), err)
	}
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-venice-key" || r.Header.Get("api-key") != "" {
			t.Errorf("Venice request: %s, Authorization=%q", r.URL, r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
			Stream   bool             `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != modelID || !body.Stream || len(body.Tools) != 1 {
			t.Errorf("Venice body: %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if len(body.Messages) != 3 || body.Messages[1]["role"] != "assistant" || body.Messages[2]["role"] != "tool" {
			t.Errorf("Venice tool history: %+v", body.Messages)
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Venice reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	data = []byte(fmt.Sprintf(`{"default":"venice","models":[{"key":"venice","provider":"catalog","providerId":"venice","id":%q,"baseUrl":%q,"apiKey":"private-venice-key"}]}`, modelID, endpoint.URL+"/api/v1"))
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
		t.Fatalf("Venice tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Venice reply" || requests != 2 {
		t.Fatalf("Venice tool continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}
