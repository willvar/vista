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

func TestOpenRouterNativeRoutingAndHeaders(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	options := `"openRouter":{"appName":"Vista test","appUrl":"https://example.com/vista","api_keys":{"Anthropic":"private-anthropic-key"},"usage":false,"models":["anthropic/claude-sonnet-4"],"provider":{"order":["Anthropic","Google"],"only":["Anthropic"],"ignore":["Other"],"quantizations":["fp8"],"max_price":{"prompt":"0.000001","completion":0.00001},"allow_fallbacks":false,"require_parameters":true,"data_collection":"deny","sort":"latency","zdr":true},"reasoning":{"enabled":true,"exclude":false,"effort":"high","max_tokens":30000},"plugins":[{"id":"file-parser","max_files":2,"pdf":{"engine":"mistral-ocr"}},{"id":"auto-router","allowed_models":["openai/gpt-5"]}],"user":"vista-user"}`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer openrouter-key" || r.Header.Get("X-OpenRouter-Title") != "Vista test" || r.Header.Get("HTTP-Referer") != "https://example.com/vista" {
			t.Errorf("OpenRouter route and headers: %s, %v", r.URL, r.Header)
		}
		var providerKeys map[string]string
		if json.Unmarshal([]byte(r.Header.Get("X-Provider-API-Keys")), &providerKeys) != nil || providerKeys["Anthropic"] != "private-anthropic-key" || len(providerKeys) != 1 {
			t.Error("OpenRouter provider API keys header is missing or invalid")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		routing, ok := body["provider"].(map[string]any)
		usage, usageOK := body["usage"].(map[string]any)
		if !ok || !usageOK || usage["include"] != false || routing["allow_fallbacks"] != false || routing["require_parameters"] != true || routing["data_collection"] != "deny" || routing["sort"] != "latency" || routing["zdr"] != true || body["model"] != "aion-labs/aion-2.0" {
			t.Errorf("OpenRouter native options: %+v", body)
		}
		price, priceOK := routing["max_price"].(map[string]any)
		if len(routing["order"].([]any)) != 2 || len(routing["only"].([]any)) != 1 || len(routing["ignore"].([]any)) != 1 || len(routing["quantizations"].([]any)) != 1 || !priceOK || price["prompt"] != "0.000001" || price["completion"] != 0.00001 || len(body["models"].([]any)) != 1 {
			t.Errorf("OpenRouter routing lists: %+v", routing)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok || reasoning["enabled"] != true || reasoning["exclude"] != false || reasoning["effort"] != "high" || reasoning["max_tokens"] != float64(16384) {
			t.Errorf("OpenRouter reasoning budget: %+v", reasoning)
		}
		plugins, pluginsOK := body["plugins"].([]any)
		if !pluginsOK || len(plugins) != 2 || plugins[0].(map[string]any)["id"] != "file-parser" || plugins[0].(map[string]any)["max_files"] != float64(2) || plugins[1].(map[string]any)["id"] != "auto-router" || body["user"] != "vista-user" {
			t.Errorf("OpenRouter plugins: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		messages, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(messages), `"role":"tool"`) || !strings.Contains(string(messages), "tool result") {
			t.Errorf("OpenRouter tool continuation: %s", messages)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	data := fmt.Sprintf(`{"default":"router","models":[{"key":"router","provider":"catalog","providerId":"openrouter","id":"aion-labs/aion-2.0","baseUrl":%q,"apiKey":"openrouter-key",%s}]}`, server.URL+"/api/v1", options)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(catalog.Public())
	if err != nil || strings.Contains(string(public), "private-anthropic-key") || strings.Contains(string(public), "openrouter-key") {
		t.Fatal("OpenRouter credentials leaked into public model list")
	}
	model, _, err := catalog.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
	messages := []provider.ChatMessage{{Role: "user", Content: "hello"}}
	first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 {
		t.Fatalf("OpenRouter first turn: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "tool result"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "done" || requests != 2 {
		t.Fatalf("OpenRouter continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestOpenRouterOptionsValidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	for _, options := range []string{
		`{"provider":{"sort":"random"}}`,
		`{"provider":{"data_collection":"unrestricted"}}`,
		`{"provider":{"order":["bad\nprovider"]}}`,
		`{"provider":{"order":["a","b","c","d","e","f","g","h","i"]}}`,
		`{"appName":"bad\nheader"}`,
		`{"appUrl":"http://example.com"}`,
		`{"appUrl":"https://user:pass@example.com"}`,
		`{"provider":{"unknown":true}}`,
		`{"reasoning":{"effort":"extreme"}}`,
		`{"reasoning":{"max_tokens":-2}}`,
		`{"reasoning":{"max_tokens":1000001}}`,
		`{"models":[" bad model"]}`,
		`{"provider":{"quantizations":["bad\nquantization"]}}`,
		`{"provider":{"max_price":{"prompt":"1x"}}}`,
		`{"provider":{"max_price":{"prompt":-1}}}`,
		`{"provider":{"max_price":{"prompt":null}}}`,
		`{"plugins":[{"id":"unknown"}]}`,
		`{"plugins":[{"id":"web"}]}`,
		`{"plugins":[{"id":"moderation","max_results":2}]}`,
		`{"plugins":[{"id":"web"},{"id":"web"}]}`,
		`{"user":"bad\nuser"}`,
		`{"api_keys":{"":"secret"}}`,
		`{"api_keys":{"Anthropic":""}}`,
		`{"api_keys":{"Anthropic":"bad\nkey"}}`,
		`{"api_keys":{"A":"1","B":"2","C":"3","D":"4","E":"5","F":"6","G":"7","H":"8","I":"9"}}`,
	} {
		data := fmt.Sprintf(`{"default":"router","models":[{"key":"router","provider":"catalog","providerId":"openrouter","id":"aion-labs/aion-2.0","apiKey":"key","openRouter":%s}]}`, options)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid OpenRouter options accepted: %s", options)
		}
	}
	data := `{"default":"router","models":[{"key":"router","provider":"catalog","providerId":"deepseek","id":"deepseek-chat","apiKey":"key","openRouter":{"provider":{"allow_fallbacks":false}}}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("OpenRouter options accepted on another Provider")
	}
}
