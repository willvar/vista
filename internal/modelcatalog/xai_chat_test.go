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

func TestXAIChatRouteAndToolContinuation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(base string) (Catalog, error) {
		data := fmt.Sprintf(`{"default":"grok","models":[{"key":"grok","provider":"catalog","providerId":"xai","id":"grok-4.3","route":"chat","baseUrl":%q,"apiKey":"xai-key","reasoningEffort":"high","promptCacheKey":"conversation-42"}]}`, base)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			return Catalog{}, err
		}
		return Load(root)
	}
	catalog, err := write("")
	if err != nil || catalog.Models[0].Provider != "openai-chat" || catalog.Models[0].BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("xAI Chat route: %+v, err=%v", catalog.Models, err)
	}
	calls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer xai-key" || r.Header.Get("x-grok-conv-id") != "conversation-42" {
			t.Errorf("xAI Chat route/headers: %s, %q, %q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-grok-conv-id"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "grok-4.3" || body["reasoning_effort"] != "high" || body["tools"] == nil {
			t.Errorf("xAI Chat request options: %+v", body)
		}
		if calls == 2 {
			encoded, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(encoded), `"role":"tool"`) || !strings.Contains(string(encoded), "file data") {
				t.Errorf("xAI Chat tool continuation: %s", encoded)
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
	catalog, err = write(endpoint.URL + "/v1")
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
	if err != nil || len(first.Calls) != 1 {
		t.Fatalf("xAI Chat first turn: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Done" || calls != 2 {
		t.Fatalf("xAI Chat continuation: %+v, calls=%d, err=%v", second, calls, err)
	}
}

func TestXAIChatRouteRejectsInvalidOptions(t *testing.T) {
	root := t.TempDir()
	for _, options := range []string{
		`"route":"unknown"`,
		`"route":"responses","promptCacheKey":"cache"`,
		`"route":"chat","promptCacheKey":" bad "`,
		`"route":"chat","reasoningEffort":"wrong"`,
	} {
		data := fmt.Sprintf(`{"default":"grok","models":[{"key":"grok","provider":"catalog","providerId":"xai","id":"grok-4.3","apiKey":"key",%s}]}`, options)
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid xAI route options accepted: %s", data)
		}
	}
}
