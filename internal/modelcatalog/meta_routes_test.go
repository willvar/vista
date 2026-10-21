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

func TestMetaChatAndMessagesRoutes(t *testing.T) {
	for _, check := range []struct{ route, protocol, path string }{
		{"chat", "openai-chat", "/v1/chat/completions"},
		{"messages", "anthropic", "/v1/messages"},
	} {
		t.Run(check.route, func(t *testing.T) {
			root := t.TempDir()
			write := func(base string) (Catalog, error) {
				data := fmt.Sprintf(`{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1","route":%q,"baseUrl":%q,"apiKey":"meta-key"}]}`, check.route, base)
				if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != check.protocol || catalog.Models[0].BaseURL != "https://api.meta.ai/v1" {
				t.Fatalf("Meta default route: %+v, err=%v", catalog.Models, err)
			}
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer meta-key" || r.Header.Get("x-api-key") != "" {
					t.Errorf("Meta route/auth: %s, auth=%q, x-api-key=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "muse-spark-1.1" || body["tools"] == nil {
					t.Errorf("Meta model or tools missing: %+v", body)
				}
				if check.route == "messages" {
					thinking, ok := body["thinking"].(map[string]any)
					if !ok || thinking["type"] != "adaptive" || thinking["display"] != "omitted" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Errorf("Meta Messages native defaults missing: %+v, version=%q", body, r.Header.Get("anthropic-version"))
					}
				}
				if calls == 2 {
					encoded, _ := json.Marshal(body["messages"])
					if !strings.Contains(string(encoded), "file data") || check.route == "messages" && (!strings.Contains(string(encoded), "tool_result") || !strings.Contains(string(encoded), `"thinking":"Check."`) || strings.Contains(string(encoded), `"signature"`)) || check.route == "chat" && !strings.Contains(string(encoded), `"role":"tool"`) {
						t.Errorf("Meta tool result missing: %s", encoded)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if check.route == "messages" {
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
				} else {
					if calls == 1 {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
					} else {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\n")
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer endpoint.Close()
			catalog, err = write(endpoint.URL + "/v1")
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol {
				t.Fatalf("Meta custom route: %+v, err=%v", model, err)
			}
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
			messages := []provider.ChatMessage{{Role: "user", Content: "read file"}}
			var reasoning strings.Builder
			first, err := provider.ModelTurnStreamWithReasoning(context.Background(), model, messages, schemas, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
			if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" || check.route == "messages" && reasoning.String() != "Check." {
				t.Fatalf("Meta first turn: %+v, err=%v", first, err)
			}
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls, AnthropicBlocks: first.AnthropicBlocks}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
			second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || second.Text != "Done" || calls != 2 {
				t.Fatalf("Meta continuation: %+v, calls=%d, err=%v", second, calls, err)
			}
		})
	}
}

func TestMetaRejectsUnknownRoute(t *testing.T) {
	root := t.TempDir()
	data := `{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1","route":"unknown","apiKey":"key"}]}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("unknown Meta route was accepted")
	}
}

func TestMetaMessagesThinkingAndEffortOptions(t *testing.T) {
	for _, check := range []struct {
		thinking    string
		wantType    string
		wantDisplay string
		wantBudget  any
	}{
		{`{"type":"enabled","budgetTokens":4096,"display":"omitted"}`, "enabled", "omitted", float64(2048)},
		{`{"type":"disabled"}`, "disabled", "", nil},
	} {
		t.Run(check.wantType, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				thinking, ok := body["thinking"].(map[string]any)
				output, outputOK := body["output_config"].(map[string]any)
				if !ok || thinking["type"] != check.wantType || check.wantDisplay != "" && thinking["display"] != check.wantDisplay || check.wantDisplay == "" && thinking["display"] != nil || thinking["budget_tokens"] != check.wantBudget || !outputOK || output["effort"] != "high" {
					t.Errorf("Meta Messages options: %+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer endpoint.Close()
			root := t.TempDir()
			data := fmt.Sprintf(`{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1","route":"messages","baseUrl":%q,"apiKey":"meta-key","thinking":%s,"effort":"high"}]}`, endpoint.URL+"/v1", check.thinking)
			if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
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
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "Done" {
				t.Fatalf("Meta Messages turn: %+v, err=%v", turn, err)
			}
		})
	}
}

func TestMetaMessagesRejectInvalidThinkingOptions(t *testing.T) {
	root := t.TempDir()
	for _, options := range []string{
		`"route":"chat","thinking":{"type":"adaptive"}`,
		`"route":"responses","effort":"high"`,
		`"route":"chat","reasoningEffort":"high"`,
		`"route":"messages","reasoningEffort":"high"`,
		`"route":"responses","reasoningEffort":"invalid"`,
		`"route":"messages","thinking":{"type":"enabled"}`,
		`"route":"messages","thinking":{"type":"disabled","display":"omitted"}`,
		`"route":"messages","thinking":{"type":"adaptive","display":"wrong"}`,
		`"route":"messages","thinking":{"type":"enabled","budget_tokens":4096,"budgetTokens":4096}`,
		`"route":"messages","effort":"none"`,
	} {
		data := fmt.Sprintf(`{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1","apiKey":"key",%s}]}`, options)
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid Meta Messages options accepted: %s", data)
		}
	}
}

func TestMetaResponsesReasoningEffortConfiguration(t *testing.T) {
	root := t.TempDir()
	data := `{"default":"meta","models":[{"key":"meta","provider":"catalog","providerId":"meta","id":"muse-spark-1.1","reasoningEffort":"high","apiKey":"key"}]}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-responses" || model.ReasoningEffort != "high" {
		t.Fatalf("Meta Responses reasoning effort: %+v, err=%v", model, err)
	}
}
