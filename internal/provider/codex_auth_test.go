package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/codexauth"
	modelcfg "vista/internal/model"
	"vista/internal/useragent"
)

type codexTestTransport func(*http.Request) (*http.Response, error)

func (f codexTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexAccountRefreshAndToolContinuation(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		dir := configureTestUserAgent(t)
		if err := os.WriteFile(filepath.Join(dir, useragent.Filename), []byte(`{"preset":`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	token := func(version string) string {
		claims, _ := json.Marshal(map[string]any{"version": version, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account"}})
		return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	}
	ctx := context.Background()
	path := codexauth.Path(t.TempDir(), uuid.NewString())
	if err := codexauth.Save(ctx, path, codexauth.Credential{AccessToken: token("initial"), RefreshToken: "refresh", AccountID: "account", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	calls, refreshes := 0, 0
	var first map[string]any
	transport := codexTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.UserAgent() != useragent.Codex() || r.Header.Get("originator") != useragent.CodexOriginator {
			t.Errorf("native identity lost during inference or refresh: %v", r.Header)
		}
		response := func(status int, contentType, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if r.URL.Host == "auth.openai.com" {
			refreshes++
			_ = r.ParseForm()
			if r.URL.Path != "/oauth/token" || r.Form.Get("refresh_token") != "refresh" {
				t.Error("invalid refresh exchange")
			}
			body, _ := json.Marshal(map[string]any{"access_token": token("renewed"), "refresh_token": "rotated", "expires_in": 3600})
			return response(200, "application/json", string(body))
		}
		calls++
		if r.URL.String() != codexBaseURL+"/codex/responses" || r.Header.Get("chatgpt-account-id") != "account" {
			t.Errorf("incorrect account route: %s", r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			first = body
			return response(401, "application/json", `{"error":{"code":"authentication_error"}}`)
		}
		if r.Header.Get("Authorization") != "Bearer "+token("renewed") {
			t.Error("request did not use refreshed token")
		}
		if body["stream"] != true || body["store"] != false || body["instructions"] != "" {
			t.Errorf("invalid Codex request controls: %+v", body)
		}
		if body["reasoning"].(map[string]any)["effort"] != "max" || !reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"}) {
			t.Error("lost reasoning controls or state")
		}
		if calls == 2 {
			if !reflect.DeepEqual(body, first) {
				t.Error("401 retry changed the request")
			}
			input := body["input"].([]any)
			if _, ok := input[0].(map[string]any)["content"].([]any); !ok {
				t.Error("Codex user message lacks input_text parts")
			}
			return response(200, "text/event-stream", "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
		}
		input := body["input"].([]any)
		if input[len(input)-1].(map[string]any)["call_id"] != "call_1" {
			t.Error("tool result lost its call ID")
		}
		return response(200, "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	})
	oldClient, oldTransport := client, http.DefaultTransport
	client = &http.Client{Transport: transport}
	http.DefaultTransport = transport
	t.Cleanup(func() { client, http.DefaultTransport = oldClient, oldTransport })
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "openai-codex", ID: "gpt-6.1-sol", ResponsesRoute: modelcfg.ResponsesRouteCodex, CodexCredentialPath: path}
	if err := ApplyReasoningEffort(&model, "max"); err != nil {
		t.Fatalf("OMP-only Codex reasoning selection failed: %v", err)
	}
	messages := []ChatMessage{{Role: "user", Content: "Read a file"}}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	turn, err := OpenAIResponsesTurn(ctx, model, messages, schemas)
	if err != nil || len(turn.Calls) != 1 {
		t.Fatalf("Codex tool request failed: %+v %v", turn, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: turn.Calls}, ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file content"})
	turn, err = OpenAIResponsesTurn(ctx, model, messages, schemas)
	if err != nil || turn.Text != "Done" || calls != 3 || refreshes != 1 {
		t.Fatal(fmt.Sprintf("continuation failed: %+v %v, calls=%d refreshes=%d", turn, err, calls, refreshes))
	}
	model.BaseURL = "https://unrelated.example"
	if _, err := OpenAIResponsesTurn(ctx, model, messages, nil); err == nil || calls != 3 {
		t.Fatal("account credential sent to an unrelated endpoint")
	}
}
