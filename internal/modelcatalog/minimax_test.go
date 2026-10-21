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

func TestPinnedMiniMaxMessagesUseBearerAuthentication(t *testing.T) {
	for _, check := range []struct{ id, base string }{
		{"minimax", "https://api.minimax.io/anthropic/v1"},
		{"minimax-cn", "https://api.minimax.cn/anthropic/v1"},
		{"minimax-coding-plan", "https://api.minimax.io/anthropic/v1"},
		{"minimax-cn-coding-plan", "https://api.minimax.cn/anthropic/v1"},
	} {
		t.Run(check.id, func(t *testing.T) {
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/anthropic/v1/messages" || r.Header.Get("Authorization") != "Bearer minimax-key" || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("MiniMax Messages route or authentication: %s, auth=%q, x-api-key=%q", r.URL, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "MiniMax-M2.7" {
					t.Errorf("MiniMax wire model: %q, err=%v", body["model"], err)
				}
				if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "adaptive" || thinking["clear_thinking"] != nil {
					t.Errorf("MiniMax Messages thinking: %+v", body["thinking"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer endpoint.Close()
			root := t.TempDir()
			path := filepath.Join(root, "models.json")
			data := fmt.Sprintf(`{"default":"minimax","models":[{"key":"minimax","provider":"catalog","providerId":%q,"id":"MiniMax-M2.7","apiKey":"minimax-key"}]}`, check.id)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != "anthropic" || model.ProviderID != check.id || model.BaseURL != check.base {
				t.Fatalf("MiniMax route: %+v, err=%v", model, err)
			}
			data = fmt.Sprintf(`{"default":"minimax","models":[{"key":"minimax","provider":"catalog","providerId":%q,"id":"MiniMax-M2.7","baseUrl":%q,"thinking":{"type":"adaptive"},"apiKey":"minimax-key"}]}`, check.id, endpoint.URL+"/anthropic/v1")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
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
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" || requests != 1 {
				t.Fatalf("MiniMax turn: %+v, requests=%d, err=%v", turn, requests, err)
			}
		})
	}
}

func TestPinnedMiniMaxChatAndResponsesRoutes(t *testing.T) {
	for _, check := range []struct{ id, host, route, protocol, path string }{
		{"minimax", "https://api.minimax.io", "chat", "openai-chat", "/v1/chat/completions"},
		{"minimax", "https://api.minimax.io", "responses", "openai-responses", "/v1/responses"},
		{"minimax-cn", "https://api.minimax.cn", "chat", "openai-chat", "/v1/chat/completions"},
		{"minimax-cn", "https://api.minimax.cn", "responses", "openai-responses", "/v1/responses"},
		{"minimax-coding-plan", "https://api.minimax.io", "chat", "openai-chat", "/v1/chat/completions"},
		{"minimax-coding-plan", "https://api.minimax.io", "responses", "openai-responses", "/v1/responses"},
		{"minimax-cn-coding-plan", "https://api.minimax.cn", "chat", "openai-chat", "/v1/chat/completions"},
		{"minimax-cn-coding-plan", "https://api.minimax.cn", "responses", "openai-responses", "/v1/responses"},
	} {
		t.Run(check.id+"/"+check.route, func(t *testing.T) {
			root := t.TempDir()
			write := func(base string) (Catalog, error) {
				options := ""
				if check.route == "responses" {
					options = `,"reasoningEffort":"high"`
				} else {
					options = `,"thinking":{"type":"disabled"}`
				}
				data := fmt.Sprintf(`{"default":"minimax","models":[{"key":"minimax","provider":"catalog","providerId":%q,"id":"MiniMax-M2.7","route":%q,"baseUrl":%q,"apiKey":"minimax-key"%s}]}`, check.id, check.route, base, options)
				if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != check.protocol || catalog.Models[0].BaseURL != check.host+"/v1" {
				t.Fatalf("MiniMax default route: %+v, err=%v", catalog.Models, err)
			}
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != check.path || r.Header.Get("Authorization") != "Bearer minimax-key" {
					t.Errorf("MiniMax route/auth: %s, auth=%q", r.URL.Path, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "MiniMax-M2.7" || body["tools"] == nil {
					t.Errorf("MiniMax model/tools missing: %+v", body)
				}
				if check.route == "chat" && body["reasoning_split"] != true {
					t.Errorf("MiniMax Chat reasoning_split missing: %+v", body)
				}
				if check.route == "chat" {
					if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "disabled" || thinking["clear_thinking"] != nil {
						t.Errorf("MiniMax Chat thinking: %+v", body["thinking"])
					}
				}
				if check.route == "responses" {
					reasoning, ok := body["reasoning"].(map[string]any)
					if !ok || reasoning["effort"] != "high" {
						t.Errorf("MiniMax Responses effort missing: %+v", body)
					}
				}
				if calls == 2 {
					field, marker := "messages", "tool"
					if check.route == "responses" {
						field, marker = "input", "function_call_output"
					}
					encoded, _ := json.Marshal(body[field])
					if !strings.Contains(string(encoded), marker) || !strings.Contains(string(encoded), "file data") {
						t.Errorf("MiniMax tool result missing: %s", encoded)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if check.route == "chat" {
					if calls == 1 {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
					} else {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\n")
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else if calls == 1 {
					fmt.Fprint(w, `data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
				} else {
					fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Done"}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
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
			if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" {
				t.Fatalf("MiniMax first turn: %+v, err=%v", first, err)
			}
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls, ResponseOutput: first.ResponseOutput}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file data"})
			second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
			if err != nil || second.Text != "Done" || calls != 2 {
				t.Fatalf("MiniMax continuation: %+v, calls=%d, err=%v", second, calls, err)
			}
		})
	}
}

func TestMiniMaxNativeOptionsValidation(t *testing.T) {
	root := t.TempDir()
	for _, data := range []string{
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"bad","apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"chat","reasoningEffort":"high","apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"messages","reasoningSplit":false,"apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"responses","reasoningEffort":"max","apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"responses","thinking":{"type":"adaptive"},"apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"chat","thinking":{"type":"enabled"},"apiKey":"key"}]}`,
		`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"messages","thinking":{"type":"adaptive","clear_thinking":false},"apiKey":"key"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid MiniMax option accepted: %s", data)
		}
	}
	valid := `{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"chat","reasoningSplit":false,"apiKey":"key"}]}`
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.ReasoningSplit == nil || *model.ReasoningSplit {
		t.Fatalf("MiniMax explicit reasoningSplit lost: %+v, err=%v", model, err)
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["reasoning_split"] != false {
			t.Errorf("MiniMax explicit reasoning_split was not sent: %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	valid = fmt.Sprintf(`{"default":"mini","models":[{"key":"mini","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","route":"chat","reasoningSplit":false,"baseUrl":%q,"apiKey":"key"}]}`, endpoint.URL+"/v1")
	if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(valid), 0600); err != nil {
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
	turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "Done" {
		t.Fatalf("MiniMax explicit reasoningSplit turn: %+v, err=%v", turn, err)
	}
}
