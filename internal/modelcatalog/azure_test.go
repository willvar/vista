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

func TestAzureNativeCatalogResourceAndCredential(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	for _, check := range []struct{ id, resourceEnv, keyEnv, resource string }{
		{"azure", "AZURE_RESOURCE_NAME", "AZURE_API_KEY", "my-openai"},
	} {
		t.Run(check.id, func(t *testing.T) {
			t.Setenv(check.resourceEnv, check.resource)
			t.Setenv(check.keyEnv, "private-azure-key")
			data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":%q,"id":"gpt-4.1-mini"}]}`, check.id)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil || model.Provider != "openai-responses" || !model.AzureNative || model.BaseURL != "https://"+check.resource+".openai.azure.com/openai/v1" || model.APIKey != "private-azure-key" || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != check.keyEnv {
				t.Fatalf("Azure native route: %+v, env=%q, err=%v", model, catalog.CredentialEnvironment(), err)
			}
		})
	}
}

func TestAzureNativeCatalogToolAndTextStream(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/openai/v1/responses" || r.URL.Query().Get("api-version") != "v1" || r.Header.Get("api-key") != "azure-test-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("Azure route/auth: %s, api-key=%q, Authorization=%q", r.URL, r.Header.Get("api-key"), r.Header.Get("Authorization"))
		}
		var body struct {
			Model string           `json:"model"`
			Input []map[string]any `json:"input"`
			Tools []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gpt-4.1-mini" || len(body.Tools) != 1 {
			t.Errorf("Azure request: %+v, %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			return
		}
		if len(body.Input) != 3 || body.Input[1]["type"] != "function_call" || body.Input[2]["type"] != "function_call_output" {
			t.Errorf("Azure tool history: %+v", body.Input)
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_2\",\"delta\":\"Azure reply\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":"azure","id":"gpt-4.1-mini","baseUrl":%q,"apiVersion":"v1","apiKey":"azure-test-key"}]}`, server.URL+"/openai/v1")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
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
	messages := []provider.ChatMessage{{Role: "user", Content: "read"}}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	first, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" {
		t.Fatalf("Azure tool call: %+v %v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Azure reply" || requests != 2 {
		t.Fatalf("Azure text after tool: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestAzureNativeCatalogRejectsMissingOrInvalidResource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	t.Setenv("AZURE_RESOURCE_NAME", "")
	for _, resource := range []string{"", "bad.example.com", "-bad"} {
		data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":"azure","id":"gpt-4.1-mini","resourceName":%q,"apiKey":"key"}]}`, resource)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "resourceName") {
			t.Fatalf("resourceName %q accepted: %v", resource, err)
		}
	}
}

func TestAzureNativeCompletionAndDeploymentURLs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(options string) (Catalog, error) {
		data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":"azure","id":"gpt-4.1-mini","resourceName":"sample","apiKey":"azure-key",%s}]}`, options)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			return Catalog{}, err
		}
		return Load(root)
	}
	for _, check := range []struct {
		options, protocol, base string
		deployment              bool
	}{
		{`"useCompletionUrls":true`, "openai-chat", "https://sample.openai.azure.com/openai/v1", false},
		{`"useCompletionUrls":true,"useDeploymentBasedUrls":true`, "openai-chat", "https://sample.openai.azure.com/openai", true},
		{`"useDeploymentBasedUrls":true`, "openai-responses", "https://sample.openai.azure.com/openai", true},
	} {
		catalog, err := write(check.options)
		if err != nil {
			t.Fatal(err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || model.Provider != check.protocol || model.BaseURL != check.base || model.AzureDeploymentBasedURLs != check.deployment {
			t.Fatalf("Azure native options %s: %+v, err=%v", check.options, model, err)
		}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/openai/deployments/gpt-4.1-mini/chat/completions" || r.URL.Query().Get("api-version") != "2024-10-21" || r.Header.Get("api-key") != "azure-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("Azure deployment Chat route/auth: %s, headers=%v", r.URL, r.Header)
		}
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gpt-4.1-mini" || len(body.Tools) != 1 {
			t.Errorf("Azure deployment Chat request: %+v, err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		encoded, _ := json.Marshal(body.Messages)
		if !strings.Contains(string(encoded), `"role":"tool"`) || !strings.Contains(string(encoded), "file contents") {
			t.Errorf("Azure deployment Chat tool continuation: %s", encoded)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Azure Chat reply"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	catalog, err := write(fmt.Sprintf(`"baseUrl":%q,"useCompletionUrls":true,"useDeploymentBasedUrls":true,"apiVersion":"2024-10-21"`, server.URL+"/openai"))
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
	if err != nil || len(first.Calls) != 1 {
		t.Fatalf("Azure deployment Chat tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: first.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: "file contents"})
	second, err := provider.ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Azure Chat reply" || requests != 2 {
		t.Fatalf("Azure deployment Chat continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestAzureNativeDeploymentResponsesAndOptionScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/deployments/gpt-4.1-mini/responses" || r.URL.Query().Get("api-version") != "v1" || r.Header.Get("api-key") != "azure-key" {
			t.Errorf("Azure deployment Responses route/auth: %s, headers=%v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg","delta":"Azure deployment reply"}`+"\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":"azure","id":"gpt-4.1-mini","baseUrl":%q,"apiKey":"azure-key","useDeploymentBasedUrls":true}]}`, server.URL+"/openai")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-responses" || !model.AzureDeploymentBasedURLs {
		t.Fatalf("Azure deployment Responses model: %+v, err=%v", model, err)
	}
	turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "Azure deployment reply" {
		t.Fatalf("Azure deployment Responses turn: %+v, err=%v", turn, err)
	}
	for _, invalid := range []string{
		`"provider":"catalog","providerId":"azure","id":"deepseek-v4-flash","useCompletionUrls":true`, // non-native override
		`"provider":"catalog","providerId":"azure","id":"deepseek-v4-flash","useDeploymentBasedUrls":false`,
		`"provider":"openai-chat","id":"custom","useCompletionUrls":true`,
	} {
		data := fmt.Sprintf(`{"default":"azure","models":[{"key":"azure","apiKey":"key",%s}]}`, invalid)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid Azure option scope accepted: %s", data)
		}
	}
}
