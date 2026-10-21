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

func TestCohereCustomConnectionAndToolContinuation(t *testing.T) {
	const modelID = "command-r-plus-08-2024"
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer private-cohere-key" {
			t.Errorf("Cohere request: %s, Authorization=%q", r.URL, r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string           `json:"model"`
			Stream   bool             `json:"stream"`
			Tools    []map[string]any `json:"tools"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != modelID || !body.Stream || len(body.Tools) != 1 {
			t.Errorf("Cohere body: %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event map[string]any) {
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		if requests == 1 {
			if len(body.Messages) != 1 || body.Messages[0]["role"] != "user" {
				t.Errorf("Cohere initial prompt: %+v", body.Messages)
			}
			startCall := map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":`}}
			startMessage := map[string]any{"tool_calls": startCall}
			send(map[string]any{"type": "tool-call-start", "delta": map[string]any{"message": startMessage}})
			deltaCall := map[string]any{"function": map[string]any{"arguments": `"/tmp/a"}`}}
			deltaMessage := map[string]any{"tool_calls": deltaCall}
			send(map[string]any{"type": "tool-call-delta", "delta": map[string]any{"message": deltaMessage}})
			send(map[string]any{"type": "tool-call-end"})
			send(map[string]any{"type": "message-end", "delta": map[string]any{"finish_reason": "TOOL_CALL"}})
			return
		}
		if len(body.Messages) != 3 || body.Messages[1]["role"] != "assistant" || body.Messages[2]["role"] != "tool" || body.Messages[2]["tool_call_id"] != "call_1" {
			t.Errorf("Cohere continuation prompt: %+v", body.Messages)
		}
		send(map[string]any{"type": "content-delta", "delta": map[string]any{"message": map[string]any{"content": map[string]any{"text": "Cohere reply"}}}})
		send(map[string]any{"type": "message-end", "delta": map[string]any{"finish_reason": "COMPLETE", "usage": map[string]any{"tokens": map[string]any{"input_tokens": 71, "output_tokens": 26}, "billed_units": map[string]any{"input_tokens": 5, "output_tokens": 26}}}})
	}))
	defer endpoint.Close()
	data := []byte(fmt.Sprintf(`{"default":"cohere","models":[{"key":"cohere","provider":"cohere","providerId":"custom","authentication":"bearer","id":%q,"baseUrl":%q,"apiKey":"private-cohere-key"}]}`, modelID, endpoint.URL+"/v2"))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	messages := []provider.ChatMessage{{Role: "user", Content: "read"}}
	first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" || first.Calls[0].Function.Arguments != `{"path":"/tmp/a"}` {
		t.Fatalf("Cohere tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Cohere reply" || second.FinishReason != "COMPLETE" || second.Usage == nil || second.Usage.InputTokens != 71 || second.Usage.OutputTokens != 26 || requests != 2 {
		t.Fatalf("Cohere continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}
