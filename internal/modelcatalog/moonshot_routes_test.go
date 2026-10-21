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

func TestCatalogNativeAlternateRoutesAndToolContinuation(t *testing.T) {
	for _, check := range []struct {
		providerID, modelID, host, route, protocol, basePath, path string
	}{
		{"moonshotai", "kimi-k2.7-code", "https://api.moonshot.ai", "messages", "anthropic", "/anthropic/v1", "/anthropic/v1/messages"},
		{"moonshotai", "kimi-k2.7-code", "https://api.moonshot.ai", "responses", "openai-responses", "/v1", "/v1/responses"},
		{"moonshotai-cn", "kimi-k2.7-code", "https://api.moonshot.cn", "messages", "anthropic", "/anthropic/v1", "/anthropic/v1/messages"},
		{"moonshotai-cn", "kimi-k2.7-code", "https://api.moonshot.cn", "responses", "openai-responses", "/v1", "/v1/responses"},
	} {
		t.Run(check.providerID+"/"+check.route, func(t *testing.T) {
			root := t.TempDir()
			write := func(base string) (Catalog, error) {
				options := ""
				if strings.HasPrefix(check.providerID, "moonshot") {
					if check.route == "responses" {
						options = `,"reasoningEffort":"high","safetyIdentifier":"user-42"`
					} else {
						options = `,"effort":"max"`
					}
				}
				data := fmt.Sprintf(`{"default":"model","models":[{"key":"model","provider":"catalog","providerId":%q,"id":%q,"route":%q,"baseUrl":%q,"apiKey":"test-key"%s}]}`, check.providerID, check.modelID, check.route, base, options)
				if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != check.protocol || catalog.Models[0].BaseURL != check.host+check.basePath {
				t.Fatalf("default native route: %+v, err=%v", catalog.Models, err)
			}
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("x-api-key") != "" {
					t.Errorf("native route/auth: %s, auth=%q, x-api-key=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != check.modelID || body["tools"] == nil {
					t.Errorf("native model or tools missing: %+v", body)
				}
				if check.route == "responses" && strings.HasPrefix(check.providerID, "moonshot") {
					reasoning, ok := body["reasoning"].(map[string]any)
					if !ok || reasoning["effort"] != "high" || body["safety_identifier"] != "user-42" {
						t.Errorf("Moonshot Responses effort missing: %+v", body)
					}
				}
				if check.route == "messages" && strings.HasPrefix(check.providerID, "moonshot") {
					output, ok := body["output_config"].(map[string]any)
					if !ok || output["effort"] != "max" || body["effort"] != nil {
						t.Errorf("Moonshot Messages effort missing: %+v", body)
					}
				}
				if calls == 2 {
					field, marker := "messages", "tool_result"
					if check.route == "responses" {
						field, marker = "input", "function_call_output"
					}
					encoded, _ := json.Marshal(body[field])
					if !strings.Contains(string(encoded), marker) || !strings.Contains(string(encoded), "file data") {
						t.Errorf("native tool result missing: %s", encoded)
					}
					if check.route == "messages" && (!strings.Contains(string(encoded), `"thinking":"Check."`) || strings.Contains(string(encoded), `"signature"`)) {
						t.Errorf("unsigned thinking not replayed: %s", encoded)
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
				} else if calls == 1 {
					fmt.Fprint(w, `data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
				} else {
					fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Done"}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
				}
			}))
			defer endpoint.Close()
			catalog, err = write(endpoint.URL + check.basePath)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol {
				t.Fatalf("custom native route: %+v, err=%v", model, err)
			}
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
			messages := []provider.ChatMessage{{Role: "user", Content: "read file"}}
			var reasoning strings.Builder
			first, err := provider.ModelTurnStreamWithReasoning(context.Background(), model, messages, schemas, nil, func(s string) error { _, err := reasoning.WriteString(s); return err })
			if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" || check.route == "messages" && reasoning.String() != "Check." {
				t.Fatalf("native tool turn: %+v, reasoning=%q, err=%v", first, reasoning.String(), err)
			}
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls, AnthropicBlocks: first.AnthropicBlocks, ResponseOutput: first.ResponseOutput}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
			second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || second.Text != "Done" || calls != 2 {
				t.Fatalf("native tool continuation: %+v, calls=%d, err=%v", second, calls, err)
			}
		})
	}
}

func TestMoonshotRejectsUnknownRoute(t *testing.T) {
	root := t.TempDir()
	valid := `{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"chat","reasoningEffort":"high","apiKey":"key"}]}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-chat" || model.ReasoningEffort != "high" {
		t.Fatalf("Moonshot Chat reasoningEffort configuration: %+v, err=%v", model, err)
	}
	for _, data := range []string{
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"unknown","apiKey":"key"}]}`,
		`{"default":"qwen","models":[{"key":"qwen","provider":"catalog","providerId":"alibaba","id":"qwen-plus","route":"unknown","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"messages","reasoningEffort":"high","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"responses","reasoningEffort":"medium","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"chat","effort":"high","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"messages","effort":"medium","thinking":{"type":"enabled"},"apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"messages","effort":"medium","safetyIdentifier":"user","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"responses","safetyIdentifier":" bad ","apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"chat","thinking":{"type":"adaptive"},"apiKey":"key"}]}`,
		`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":"moonshotai","id":"kimi-k2.7-code","route":"chat","thinking":{"type":"enabled","keep":"unknown"},"apiKey":"key"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid Moonshot route/effort was accepted: %s", data)
		}
	}
}

func TestMoonshotChatThinkingOption(t *testing.T) {
	for _, providerID := range []string{"moonshotai", "moonshotai-cn"} {
		t.Run(providerID, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("Moonshot Chat route/auth: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "enabled" || thinking["keep"] != "all" || thinking["clear_thinking"] != nil || body["reasoning_effort"] != "high" {
					t.Errorf("Moonshot Chat thinking/effort: %+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer endpoint.Close()
			root := t.TempDir()
			data := fmt.Sprintf(`{"default":"kimi","models":[{"key":"kimi","provider":"catalog","providerId":%q,"id":"kimi-k2.7-code","route":"chat","baseUrl":%q,"apiKey":"test-key","reasoningEffort":"high","thinking":{"type":"enabled","keep":"all"}}]}`, providerID, endpoint.URL+"/v1")
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
				t.Fatalf("Moonshot Chat turn: %+v, err=%v", turn, err)
			}
		})
	}
}
