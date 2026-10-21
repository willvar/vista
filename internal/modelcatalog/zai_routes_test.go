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

func TestZAICodingPlanRouteSelectionAndToolContinuation(t *testing.T) {
	for _, check := range []struct {
		providerID, modelID, route, protocol, base, path, options string
	}{
		{"zai-coding-plan", "glm-5.2", "chat", "openai-chat", "https://api.z.ai/api/coding/paas/v4", "/api/coding/paas/v4/chat/completions", `,"thinking":{"type":"enabled","clear_thinking":false},"reasoningEffort":"high"`},
		{"zai-coding-plan", "glm-5.2", "messages", "anthropic", "https://api.z.ai/api/anthropic/v1", "/api/anthropic/v1/messages", `,"thinking":{"type":"adaptive"},"effort":"high"`},
		{"zai-coding-plan", "glm-5.2", "responses", "openai-responses", "https://api.z.ai/api/v1", "/api/v1/responses", `,"reasoningEffort":"medium"`},
		{"zhipuai-coding-plan", "glm-5.3", "messages", "anthropic", "https://api.z.ai/api/anthropic/v1", "/api/anthropic/v1/messages", `,"thinking":{"type":"adaptive"},"effort":"high"`},
		{"zhipuai-coding-plan", "glm-5.3", "responses", "openai-responses", "https://api.z.ai/api/v1", "/api/v1/responses", `,"reasoningEffort":"medium"`},
	} {
		t.Run(check.providerID+"/"+check.route, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "models.json")
			write := func(base string) (Catalog, error) {
				data := fmt.Sprintf(`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":%q,"id":%q,"route":%q,"baseUrl":%q,"apiKey":"private-key"%s}]}`, check.providerID, check.modelID, check.route, base, check.options)
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != check.protocol || catalog.Models[0].BaseURL != check.base {
				t.Fatalf("default route: %+v, err=%v", catalog.Models, err)
			}
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer private-key" || r.Header.Get("x-api-key") != "" {
					t.Errorf("ZAI request route/auth: %s %s, auth=%q, x-api-key=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
				}
				if check.route == "messages" && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("ZAI Messages version header: %q", r.Header.Get("anthropic-version"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != check.modelID || body["tools"] == nil {
					t.Errorf("ZAI model or tools missing: %+v", body)
				}
				switch check.route {
				case "chat":
					thinking, ok := body["thinking"].(map[string]any)
					if !ok || thinking["type"] != "enabled" || thinking["clear_thinking"] != false || body["reasoning_effort"] != "high" {
						t.Errorf("ZAI Chat native options: %+v", body)
					}
				case "messages":
					thinking, ok := body["thinking"].(map[string]any)
					output, outputOK := body["output_config"].(map[string]any)
					if !ok || thinking["type"] != "adaptive" || thinking["clear_thinking"] != nil || !outputOK || output["effort"] != "high" {
						t.Errorf("ZAI Messages native thinking: %+v", body)
					}
				case "responses":
					reasoning, ok := body["reasoning"].(map[string]any)
					if !ok || reasoning["effort"] != "medium" {
						t.Errorf("ZAI Responses native effort: %+v", body)
					}
				}
				if calls == 2 {
					field := "messages"
					marker := "tool"
					if check.route == "messages" {
						marker = "tool_result"
					} else if check.route == "responses" {
						field, marker = "input", "function_call_output"
					}
					encoded, _ := json.Marshal(body[field])
					if !strings.Contains(string(encoded), marker) || !strings.Contains(string(encoded), "file data") {
						t.Errorf("ZAI %s tool result absent: %s", check.route, encoded)
					}
					if check.route == "messages" && (!strings.Contains(string(encoded), `"thinking":"Check."`) || strings.Contains(string(encoded), `"signature"`)) {
						t.Errorf("ZAI unsigned thinking continuation: %s", encoded)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch check.route {
				case "chat":
					if calls == 1 {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
					} else {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\n")
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				case "messages":
					if calls == 1 {
						fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"Check."}}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
					} else {
						fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
					}
					fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
				case "responses":
					if calls == 1 {
						fmt.Fprint(w, `data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
					} else {
						fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Done"}`+"\n\n")
						fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
					}
				}
			}))
			defer endpoint.Close()
			basePath := check.path[:strings.LastIndex(check.path, "/")]
			if check.route == "chat" {
				basePath = strings.TrimSuffix(check.path, "/chat/completions")
			}
			catalog, err = write(endpoint.URL + basePath)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol {
				t.Fatalf("custom route: %+v, err=%v", model, err)
			}
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
			messages := []provider.ChatMessage{{Role: "user", Content: "read file"}}
			var reasoning strings.Builder
			first, err := provider.ModelTurnStreamWithReasoning(context.Background(), model, messages, schemas, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
			if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" || check.route == "messages" && reasoning.String() != "Check." {
				t.Fatalf("first turn: %+v, err=%v", first, err)
			}
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls, AnthropicBlocks: first.AnthropicBlocks, ResponseOutput: first.ResponseOutput}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
			second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || second.Text != "Done" || calls != 2 {
				t.Fatalf("tool continuation: %+v, calls=%d, err=%v", second, calls, err)
			}
		})
	}
}

func TestZAICodingPlanRejectsUnsupportedRouteSelection(t *testing.T) {
	root := t.TempDir()
	for _, data := range []string{
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"unknown","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai","id":"glm-5","route":"messages","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"anthropic","id":"glm-5.2","route":"messages","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"chat","thinking":{"type":"adaptive"},"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"messages","thinking":{"type":"enabled","clear_thinking":false},"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"responses","thinking":{"type":"enabled"},"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"messages","reasoningEffort":"high","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"chat","effort":"high","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zhipuai-coding-plan","id":"glm-5.3","route":"responses","effort":"high","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"messages","effort":"invalid","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"responses","reasoningEffort":"wrong","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"messages","toolStream":true,"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai-coding-plan","id":"glm-5.2","route":"responses","doSample":false,"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai","id":"glm-5","responseFormat":{"type":"xml"},"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":"zai","id":"glm-5","requestID":" leading","apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"openai-chat","providerId":"zai","id":"glm-5","toolStream":true,"apiKey":"key"}]}`,
		`{"default":"glm","models":[{"key":"glm","provider":"anthropic","providerId":"zai-coding-plan","id":"glm-5.2","thinking":{"type":"enabled"},"apiKey":"key"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("unsupported route accepted: %s", data)
		}
	}
}

func TestZAIChatOptionsAcrossCatalogProviderIDs(t *testing.T) {
	for _, check := range []struct{ providerID, modelID string }{
		{"zai", "glm-5"},
		{"zai-coding-plan", "glm-5.2"},
		{"zhipuai-coding-plan", "glm-5.3"},
	} {
		t.Run(check.providerID, func(t *testing.T) {
			root := t.TempDir()
			data := fmt.Sprintf(`{"default":"glm","models":[{"key":"glm","provider":"catalog","providerId":%q,"id":%q,"thinking":{"type":"enabled"},"reasoningEffort":"high","toolStream":false,"doSample":false,"responseFormat":{"type":"json_object"},"requestID":"request-one","userID":"user-one","apiKey":"key"}]}`, check.providerID, check.modelID)
			if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != "openai-chat" || model.Thinking == nil || model.Thinking.Type != "enabled" || model.ReasoningEffort != "high" || model.ToolStream == nil || *model.ToolStream || model.ZAIDoSample == nil || *model.ZAIDoSample || model.ChatResponseFormat == nil || model.ChatResponseFormat.Type != "json_object" || model.ZAIRequestID != "request-one" || model.ZAIUserID != "user-one" {
				t.Fatalf("ZAI Chat options: %+v, err=%v", model, err)
			}
		})
	}
}
