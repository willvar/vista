package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/provider"
)

func TestNativeCompatibleChatHosts(t *testing.T) {
	for _, check := range []struct {
		providerID, modelID, base, path string
	}{
		{"deepseek", "deepseek-v4-pro", "https://api.deepseek.com/v1", "/v1/chat/completions"},
		{"fireworks-ai", "deepseek-v4-pro", "https://api.fireworks.ai/inference/v1", "/inference/v1/chat/completions"},
		{"baseten", "deepseek-ai/DeepSeek-V4-Pro-0813", "https://inference.baseten.co/v1", "/v1/chat/completions"},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "models.json")
			write := func(base string) (Catalog, error) {
				data := fmt.Sprintf(`{"default":"native","models":[{"key":"native","provider":"catalog","providerId":%q,"id":%q,"baseUrl":%q,"apiKey":"test-key"}]}`, check.providerID, check.modelID, base)
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != "openai-chat" || catalog.Models[0].BaseURL != check.base {
				t.Fatalf("native Chat route: %+v, err=%v", catalog.Models, err)
			}
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("native Chat route/auth: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != check.modelID || body["tools"] == nil {
					t.Errorf("native Chat model or tools: %+v", body)
				}
				if calls == 2 {
					encoded, _ := json.Marshal(body["messages"])
					if !strings.Contains(string(encoded), `"role":"tool"`) || !strings.Contains(string(encoded), "file data") {
						t.Errorf("native Chat tool continuation: %s", encoded)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls == 1 {
					fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
				}
			}))
			defer endpoint.Close()
			basePath := strings.TrimSuffix(check.path, "/chat/completions")
			catalog, err = write(endpoint.URL + basePath)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil {
				t.Fatal(err)
			}
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
			messages := []provider.ChatMessage{{Role: "user", Content: "read file"}}
			first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" {
				t.Fatalf("native Chat tool call: %+v, err=%v", first, err)
			}
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
			second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || second.Text != "Done" || calls != 2 {
				t.Fatalf("native Chat tool result: %+v, calls=%d, err=%v", second, calls, err)
			}
		})
	}
}
