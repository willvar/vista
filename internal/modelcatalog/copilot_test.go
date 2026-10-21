package modelcatalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

func TestPinnedCopilotProtocolAndHeaders(t *testing.T) {
	for _, check := range []struct{ id, protocol, route string }{
		{"claude-haiku-4.5", "anthropic", "/v1/messages"},
		{"gpt-5-mini", "openai-chat", "/chat/completions"},
		{"gpt-6-sol", "openai-responses", "/responses"},
		{"gemini-3.5-flash", "openai-responses", "/responses"},
		{"kimi-k3", "openai-chat", "/chat/completions"},
	} {
		t.Run(check.id, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != check.route || r.Header.Get("Authorization") != "Bearer copilot-token" || r.Header.Get("x-api-key") != "" || r.Header.Get("Openai-Intent") != "conversation-edits" || r.Header.Get("User-Agent") != "vista" || r.Header.Get("X-GitHub-Api-Version") != "2026-08-01" || r.Header.Get("X-Interaction-Type") != "conversation-agent" || r.Header.Get("X-Interaction-Id") != "session-one" || r.Header.Get("x-initiator") != "user" || r.Header.Get("Copilot-Vision-Request") != "" {
					t.Errorf("Copilot request: %s, headers=%+v", r.URL, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != check.id {
					t.Errorf("Copilot body: %+v, err=%v", body, err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch check.protocol {
				case "anthropic":
					if r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" {
						t.Error("Copilot Anthropic beta header missing")
					}
					fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				case "openai-chat":
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case "openai-responses":
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer endpoint.Close()
			root := t.TempDir()
			data := []byte(fmt.Sprintf(`{"default":"copilot","models":[{"key":"copilot","provider":"catalog","providerId":"github-copilot","id":%q,"baseUrl":%q}]}`, check.id, endpoint.URL))
			if err := os.WriteFile(filepath.Join(root, "models.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_TOKEN", "copilot-token")
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol || model.APIKey != "copilot-token" || model.Tools == nil || !*model.Tools {
				t.Fatalf("Copilot catalog model: %+v, err=%v", model, err)
			}
			model.SessionID = "session-one"
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" || calls != 1 {
				t.Fatalf("Copilot turn: %+v, calls=%d, err=%v", turn, calls, err)
			}
			filtered := WorkerEnvironment([]string{"PATH=/usr/bin", "GITHUB_TOKEN=copilot-token"}, catalog.CredentialEnvironment()...)
			if len(filtered) != 1 || !strings.HasPrefix(filtered[0], "PATH=") {
				t.Fatalf("Copilot token reached Worker environment: %q", filtered)
			}
		})
	}
}

func TestCopilotImageRequestsSetVisionHeaderAcrossRoutes(t *testing.T) {
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	encodedImage := base64.StdEncoding.EncodeToString(image)
	for _, check := range []struct{ id, protocol, route, reply string }{
		{"claude-haiku-4.5", "anthropic", "/v1/messages", "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"seen\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"gpt-5-mini", "openai-chat", "/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"gpt-6-sol", "openai-responses", "/responses", "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"seen\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
	} {
		t.Run(check.id, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
				if err != nil || r.URL.Path != check.route || r.Header.Get("Copilot-Vision-Request") != "true" || !strings.Contains(string(body), encodedImage) {
					t.Errorf("Copilot %s image request: path=%s, vision=%q, image=%v, err=%v", check.protocol, r.URL.Path, r.Header.Get("Copilot-Vision-Request"), strings.Contains(string(body), encodedImage), err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, check.reply)
			}))
			defer endpoint.Close()
			root := t.TempDir()
			data := []byte(fmt.Sprintf(`{"default":"copilot","models":[{"key":"copilot","provider":"catalog","providerId":"github-copilot","id":%q,"baseUrl":%q}]}`, check.id, endpoint.URL))
			if err := os.WriteFile(filepath.Join(root, "models.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_TOKEN", "copilot-token")
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != check.protocol {
				t.Fatalf("Copilot image model: %+v, err=%v", model, err)
			}
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "describe", Images: []provider.Image{{MediaType: "image/png", Data: image}}}}, nil)
			if err != nil || turn.Text != "seen" || calls != 1 {
				t.Fatalf("Copilot image turn: %+v, calls=%d, err=%v", turn, calls, err)
			}
		})
	}
}

func TestCopilotToolContinuationIsAgentInitiated(t *testing.T) {
	count := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		initiator := "user"
		if count == 2 {
			initiator = "agent"
		}
		if r.Header.Get("x-initiator") != initiator || r.URL.Path != "/responses" {
			t.Errorf("Copilot continuation %d: path=%s, initiator=%q", count, r.URL.Path, r.Header.Get("x-initiator"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
		}
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "github-copilot", ID: "gpt-6-sol", BaseURL: endpoint.URL, APIKey: "token"}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	messages := []provider.ChatMessage{{Role: "user", Content: "read"}}
	first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 {
		t.Fatalf("Copilot tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "done" || count != 2 {
		t.Fatalf("Copilot tool continuation: %+v, requests=%d, err=%v", second, count, err)
	}
}
