package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"vista/internal/provider"
)

func TestModelCatalogIsPrivateAndDoesNotExposeCredentials(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := `{"default":"claude","models":[{"key":"gpt","provider":"openai-chat","providerId":"custom-host","id":"gpt-test","baseUrl":"http://127.0.0.1:1234/v1","apiKey":"openai-secret","tools":false,"context":8192},{"key":"claude","provider":"anthropic","id":"claude-test","apiKey":"anthropic-secret"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, key, err := catalog.Resolve("")
	if err != nil || key != "claude" || model.Provider != "anthropic" || model.APIKey != "anthropic-secret" {
		t.Fatalf("default model: %+v %q %v", model, key, err)
	}
	if _, _, err := catalog.Resolve("unknown"); err == nil {
		t.Fatal("unknown model key was accepted")
	}
	public := catalog.Public()
	if len(public) != 2 || public[0].Key != "claude" || public[1].Key != "gpt" {
		t.Fatalf("model list: %+v", public)
	}
	if public[1].ProviderID != "custom-host" || public[1].Tools == nil || *public[1].Tools || public[1].Context != 8192 {
		t.Fatalf("custom model metadata: %+v", public[1])
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("public credential file was accepted")
	}
}

func TestDirectAnthropicThinkingConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(entry string) (Catalog, error) {
		data := `{"default":"claude","models":[` + entry + `]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(root)
	}
	for _, entry := range []string{
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-opus-5-5","apiKey":"key","thinking":{"type":"enabled","budgetTokens":20000,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"error"}},"effort":"high"}`,
		`{"key":"claude","provider":"anthropic","id":"claude-opus-5-5","apiKey":"key","thinking":{"type":"adaptive"},"effort":"medium"}`,
	} {
		catalog, err := write(entry)
		if err != nil {
			t.Fatalf("direct Anthropic config rejected: %v", err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || model.Provider != "anthropic" || model.Thinking == nil || model.Effort == "" {
			t.Fatalf("direct Anthropic options lost: %+v, err=%v", model, err)
		}
	}
	for _, entry := range []string{
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-opus-5-5","apiKey":"key","thinking":{"type":"enabled","budgetTokens":1000}}`,
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-opus-5-5","apiKey":"key","thinking":{"type":"enabled","budgetTokens":2000,"budget_tokens":2000}}`,
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-opus-5-5","apiKey":"key","thinking":{"type":"disabled","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`,
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-opus-5-5","apiKey":"key","effort":"unbounded"}`,
		`{"key":"claude","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","apiKey":"key","thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`,
	} {
		if _, err := write(entry); err == nil {
			t.Fatalf("invalid Anthropic thinking config accepted: %s", entry)
		}
	}
}

func TestGeminiThinkingBudgetConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	for _, entry := range []string{
		`{"key":"gemini","provider":"gemini","id":"gemini-2.5-flash","apiKey":"key","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"catalog","providerId":"google","id":"gemini-2.5-flash","apiKey":"key","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"gemini","id":"gemini-flash-latest","apiKey":"key","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"catalog","providerId":"google","id":"gemini-flash-lite-latest","apiKey":"key","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"catalog","providerId":"google-vertex","id":"gemini-flash-latest","accessToken":"token","project":"test-project","location":"us-central1","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"catalog","providerId":"google-vertex","id":"gemini-2.5-flash","accessToken":"token","project":"test-project","location":"us-central1","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"gemini","id":"gemini-3-flash-preview","apiKey":"key","thinkingLevel":"low"}`,
		`{"key":"gemini","provider":"catalog","providerId":"google","id":"gemini-3-flash-preview","apiKey":"key","thinkingLevel":"low"}`,
	} {
		if err := os.WriteFile(path, []byte(`{"default":"gemini","models":[`+entry+`]}`), 0600); err != nil {
			t.Fatal(err)
		}
		catalog, err := Load(root)
		if err != nil {
			t.Fatalf("Gemini thinking budget rejected: %s: %v", entry, err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || strings.Contains(entry, `"thinkingBudget"`) && (model.ThinkingBudget == nil || *model.ThinkingBudget != 1024) || strings.Contains(entry, `"thinkingLevel"`) && model.ThinkingLevel != "low" {
			t.Fatalf("Gemini thinking option lost: %+v %v", model, err)
		}
	}
	for _, entry := range []string{
		`{"key":"gemini","provider":"gemini","id":"gemini-2.5-flash","apiKey":"key","thinkingLevel":"low"}`,
		`{"key":"gemini","provider":"gemini","id":"gemini-3-flash-preview","apiKey":"key","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"gemini","id":"gemini-3-flash-preview","apiKey":"key","thinkingLevel":"high","thinkingBudget":1024}`,
		`{"key":"gemini","provider":"gemini","id":"gemini-2.5-flash","apiKey":"key","thinkingBudget":32769}`,
		`{"key":"gemini","provider":"openai-chat","id":"gemini-2.5-flash","apiKey":"key","thinkingBudget":1024}`,
	} {
		if err := os.WriteFile(path, []byte(`{"default":"gemini","models":[`+entry+`]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid Gemini thinking option accepted: %s", entry)
		}
	}
}

func TestDirectAnthropicAuthTokenConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(entry string) (Catalog, error) {
		data := `{"default":"claude","models":[` + entry + `]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(root)
	}
	for _, entry := range []string{
		`{"key":"claude","provider":"anthropic","id":"claude-sonnet-4-5","authToken":"private-token"}`,
		`{"key":"claude","provider":"catalog","providerId":"anthropic","id":"claude-sonnet-4-5","authToken":"private-token"}`,
	} {
		catalog, err := write(entry)
		if err != nil {
			t.Fatalf("direct Anthropic authToken rejected: %v", err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || model.APIKey != "private-token" || !model.AnthropicAuthToken {
			t.Fatalf("Anthropic bearer credential lost: %+v, err=%v", model, err)
		}
		if encoded, err := json.Marshal(catalog.Public()); err != nil || strings.Contains(string(encoded), "private-token") {
			t.Fatalf("Anthropic bearer credential leaked in public catalog: %s, err=%v", encoded, err)
		}
	}
	for _, entry := range []string{
		`{"key":"claude","provider":"anthropic","id":"claude-sonnet-4-5","authToken":"private-token","apiKey":"private-key"}`,
		`{"key":"claude","provider":"catalog","providerId":"minimax","id":"MiniMax-M2.7","authToken":"private-token"}`,
		`{"key":"claude","provider":"openai-chat","id":"test","authToken":"private-token"}`,
	} {
		if _, err := write(entry); err == nil {
			t.Fatalf("invalid authToken config accepted: %s", entry)
		}
	}
}

func TestDirectOpenAIAccountHeaders(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer private-key" || r.Header.Get("OpenAI-Organization") != "org_test" || r.Header.Get("OpenAI-Project") != "proj_test" {
			t.Errorf("OpenAI account headers missing: %+v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/chat/completions":
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
		case "/v1/responses":
			fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg","delta":"Done."}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
		default:
			t.Errorf("unexpected OpenAI route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	write := func(entry string) (Catalog, error) {
		if err := os.WriteFile(path, []byte(`{"default":"openai","models":[`+entry+`]}`), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(root)
	}
	for _, entry := range []string{
		fmt.Sprintf(`{"key":"openai","provider":"openai-chat","id":"gpt-4.1-mini","baseUrl":%q,"apiKey":"private-key","organization":"org_test","project":"proj_test"}`, server.URL+"/v1"),
		fmt.Sprintf(`{"key":"openai","provider":"catalog","providerId":"openai","id":"gpt-4.1-mini","baseUrl":%q,"apiKey":"private-key","organization":"org_test","project":"proj_test"}`, server.URL+"/v1"),
	} {
		catalog, err := write(entry)
		if err != nil {
			t.Fatal(err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || model.OpenAIOrganization != "org_test" || model.OpenAIProject != "proj_test" {
			t.Fatalf("OpenAI account options missing: %+v, err=%v", model, err)
		}
		if encoded, err := json.Marshal(catalog.Public()); err != nil || strings.Contains(string(encoded), "org_test") || strings.Contains(string(encoded), "proj_test") {
			t.Fatalf("OpenAI account options leaked in public catalog: %s, err=%v", encoded, err)
		}
		turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
		if err != nil || turn.Text != "Done." {
			t.Fatalf("OpenAI account request: %+v, err=%v", turn, err)
		}
	}
	if requests != 2 {
		t.Fatalf("expected two OpenAI account requests, got %d", requests)
	}
	for _, entry := range []string{
		`{"key":"openai","provider":"openai-chat","id":"test","apiKey":"key","organization":"bad\nheader"}`,
		`{"key":"openai","provider":"openai-chat","id":"test","apiKey":"key","project":"bad.project"}`,
		`{"key":"openai","provider":"catalog","providerId":"deepseek","id":"deepseek-flash","apiKey":"key","organization":"org_test"}`,
		`{"key":"openai","provider":"catalog","providerId":"deepseek","id":"deepseek-flash","apiKey":"key","project":"proj_test"}`,
	} {
		if _, err := write(entry); err == nil {
			t.Fatalf("invalid OpenAI account config accepted: %s", entry)
		}
	}
}

func TestExplicitImageInputCapabilityForProtocolModel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(data string) (Catalog, error) {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(root)
	}
	valid := `{"default":"image","models":[{"key":"image","provider":"openai-chat","id":"test","apiKey":"key","inputModalities":["text","image"]}]}`
	catalog, err := write(valid)
	if err != nil || len(catalog.Public()) != 1 || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("explicit image model: %+v %v", catalog, err)
	}
	mistral := `{"default":"image","models":[{"key":"image","provider":"mistral","id":"test","apiKey":"key","inputModalities":["text","image"]}]}`
	catalog, err = write(mistral)
	if err != nil || len(catalog.Public()) != 1 || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("Mistral image model: %+v %v", catalog, err)
	}
	cohere := `{"default":"image","models":[{"key":"image","provider":"cohere","id":"test","apiKey":"key","inputModalities":["text","image"]}]}`
	catalog, err = write(cohere)
	if err != nil || len(catalog.Public()) != 1 || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("Cohere image model: %+v %v", catalog, err)
	}
	bedrock := `{"default":"image","models":[{"key":"image","provider":"catalog","providerId":"amazon-bedrock","id":"us.amazon.nova-lite-v1:0","region":"us-east-1","apiKey":"key"}]}`
	catalog, err = write(bedrock)
	if err != nil || len(catalog.Public()) != 1 || catalog.Public()[0].Provider != "bedrock" || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("Bedrock image model: %+v %v", catalog, err)
	}
	vercel := `{"default":"image","models":[{"key":"image","provider":"catalog","providerId":"vercel","id":"alibaba/qwen3.5-flash","apiKey":"key"}]}`
	catalog, err = write(vercel)
	if err != nil || len(catalog.Public()) != 1 || catalog.Public()[0].Provider != "vercel-gateway" || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("Vercel Gateway image model: %+v %v", catalog, err)
	}
	mantle := `{"default":"image","models":[{"key":"image","provider":"catalog","providerId":"amazon-bedrock","id":"openai.gpt-5.4","region":"us-east-1","apiKey":"key"}]}`
	catalog, err = write(mantle)
	if err != nil || len(catalog.Public()) != 1 || catalog.Public()[0].Provider != "bedrock-mantle-responses" || !slices.Contains(catalog.Public()[0].InputModalities, "image") {
		t.Fatalf("Bedrock Mantle Responses image model: %+v %v", catalog, err)
	}

	for _, config := range []string{
		`{"default":"bad","models":[{"key":"bad","provider":"bedrock-mantle-chat","id":"test","apiKey":"key","inputModalities":["text","image"]}]}`,
		`{"default":"bad","models":[{"key":"bad","provider":"openai-chat","id":"test","apiKey":"key","inputModalities":["image"]}]}`,
		`{"default":"bad","models":[{"key":"bad","provider":"openai-chat","id":"test","apiKey":"key","inputModalities":["text","video"]}]}`,
	} {
		if _, err := write(config); err == nil {
			t.Fatalf("invalid image model accepted: %s", config)
		}
	}
}

func TestResponsesReasoningSummaryConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(data string) (Catalog, error) {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(root)
	}
	valid := `{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-responses","id":"mock","apiKey":"key","reasoningSummary":"auto"}]}`
	catalog, err := write(valid)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.ReasoningSummary != "auto" {
		t.Fatalf("reasoning option lost: %+v, err=%v", model, err)
	}
	for _, data := range []string{
		`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-chat","id":"mock","apiKey":"key","reasoningSummary":"auto"}]}`,
		`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-responses","id":"mock","apiKey":"key","reasoningSummary":"unbounded"}]}`,
		`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-responses","providerId":"xai","id":"mock","apiKey":"key","reasoningSummary":"auto"}]}`,
	} {
		if _, err := write(data); err == nil {
			t.Fatalf("unsupported reasoning option accepted: %s", data)
		}
	}
}

func TestOpenAIResponsesReasoningEffortConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	for _, check := range []struct{ data, summary string }{
		{`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-responses","id":"gpt-5","apiKey":"key","reasoningEffort":"high"}]}`, ""},
		{`{"default":"reasoning","models":[{"key":"reasoning","provider":"catalog","providerId":"azure","id":"gpt-5","apiKey":"key","baseUrl":"http://127.0.0.1:1234/openai/v1","reasoningEffort":"high"}]}`, ""},
		{`{"default":"reasoning","models":[{"key":"reasoning","provider":"catalog","providerId":"amazon-bedrock","id":"openai.gpt-5.4","region":"us-east-1","apiKey":"key","reasoningEffort":"high","reasoningSummary":"concise"}]}`, "concise"},
	} {
		if err := os.WriteFile(path, []byte(check.data), 0600); err != nil {
			t.Fatal(err)
		}
		catalog, err := Load(root)
		if err != nil {
			t.Fatalf("valid OpenAI Responses effort rejected: %v", err)
		}
		model, _, err := catalog.Resolve("")
		if err != nil || model.ReasoningEffort != "high" || model.ReasoningSummary != check.summary {
			t.Fatalf("OpenAI Responses effort lost: %+v, err=%v", model, err)
		}
	}
	for _, data := range []string{
		`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-chat","id":"gpt-5","apiKey":"key","reasoningEffort":"high"}]}`,
		`{"default":"reasoning","models":[{"key":"reasoning","provider":"openai-responses","id":"gpt-5","apiKey":"key","reasoningEffort":"invalid"}]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("unsupported OpenAI effort accepted: %s", data)
		}
	}
}

func TestCatalogOpenAIChatVendorRoutes(t *testing.T) {
	for _, check := range []struct{ provider, id, base, custom string }{
		{"cerebras", "gpt-oss-120b", "https://api.cerebras.ai/v1", "http://127.0.0.1:1234/v1"},
		{"deepinfra", "Qwen/Qwen3-32B", "https://api.deepinfra.com/v1/openai", "http://127.0.0.1:1234/v1/openai"},
		{"groq", "llama-3.3-70b-versatile", "https://api.groq.com/openai/v1", "http://127.0.0.1:1234/v1"},
		{"openrouter", "deepseek/deepseek-chat", "https://openrouter.ai/api/v1", "http://127.0.0.1:1234/v1"},
		{"togetherai", "MiniMaxAI/MiniMax-M2.7", "https://api.together.xyz/v1", "http://127.0.0.1:1234/v1"},
	} {
		t.Run(check.provider, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "models.json")
			write := func(base string) Catalog {
				data := fmt.Sprintf(`{"default":"vendor","models":[{"key":"vendor","provider":"catalog","providerId":%q,"id":%q,"baseUrl":%q,"apiKey":"test-key"}]}`, check.provider, check.id, base)
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				catalog, err := Load(root)
				if err != nil {
					t.Fatal(err)
				}
				return catalog
			}
			for input, want := range map[string]string{"": check.base, "http://127.0.0.1:1234/v1": check.custom} {
				model, _, err := write(input).Resolve("")
				if err != nil || model.Provider != "openai-chat" || model.BaseURL != want || model.ProviderID != check.provider {
					t.Fatalf("catalog %s route for %q: %+v %v", check.provider, input, model, err)
				}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/v1/chat/completions"
				if check.provider == "deepinfra" {
					path = "/v1/openai/chat/completions"
				}
				if r.URL.Path != path || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("vendor request: %s, %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"vendor reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model, _, err := write(server.URL + "/v1").Resolve("")
			if err != nil {
				t.Fatal(err)
			}
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "vendor reply" || calls != 1 {
				t.Fatalf("vendor text turn: %+v, calls=%d, err=%v", turn, calls, err)
			}
		})
	}
}

func TestCatalogPackageOverridesKeepProviderEndpoint(t *testing.T) {
	const chat = "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	const responses = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	const anthropic = "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	const gemini = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	for _, check := range []struct {
		provider, id, protocol, base, path, keyHeader, stream string
	}{
		{"opencode", "deepseek-v4-flash", "openai-chat", "https://opencode.ai/zen/v1", "/zen/v1/chat/completions", "Authorization", chat},
		{"opencode", "claude-opus-4-5", "anthropic", "https://opencode.ai/zen/v1", "/zen/v1/messages", "x-api-key", anthropic},
		{"opencode", "gemini-3-flash", "gemini", "https://opencode.ai/zen/v1", "/zen/v1/models/gemini-3-flash:streamGenerateContent", "x-goog-api-key", gemini},
		{"opencode", "gpt-5", "openai-responses", "https://opencode.ai/zen/v1", "/zen/v1/responses", "Authorization", responses},
		{"opencode-go", "minimax-m2.5", "anthropic", "https://opencode.ai/zen/go/v1", "/zen/go/v1/messages", "x-api-key", anthropic},
		{"opencode-go", "deepseek-v4-flash", "openai-responses", "https://opencode.ai/zen/go/v1", "/zen/go/v1/responses", "Authorization", responses},
		{"opencode-go", "gpt-5.6-luna", "openai-responses", "https://opencode.ai/zen/go/v1", "/zen/go/v1/responses", "Authorization", responses},
	} {
		t.Run(check.provider+"/"+check.id, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "models.json")
			write := func(base string) (Catalog, error) {
				data := fmt.Sprintf(`{"default":"vendor","models":[{"key":"vendor","provider":"catalog","providerId":%q,"id":%q,"baseUrl":%q,"apiKey":"test-key"}]}`, check.provider, check.id, base)
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					return Catalog{}, err
				}
				return Load(root)
			}
			catalog, err := write("")
			if err != nil || catalog.Models[0].Provider != check.protocol || catalog.Models[0].BaseURL != check.base {
				t.Fatalf("pinned endpoint or protocol: %+v %v", catalog, err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				wantKey := "test-key"
				if check.keyHeader == "Authorization" {
					wantKey = "Bearer test-key"
				}
				if r.URL.Path != check.path || (check.protocol == "gemini" && r.URL.Query().Get("alt") != "sse") || r.Header.Get(check.keyHeader) != wantKey {
					t.Errorf("override request: %s, key=%q", r.URL, r.Header.Get(check.keyHeader))
				}
				if check.keyHeader != "Authorization" && r.Header.Get("Authorization") != "" {
					t.Error("catalog credential also appeared in Authorization header")
				}
				var body struct {
					Model string `json:"model"`
				}
				if check.protocol != "gemini" {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != check.id {
						t.Errorf("override model ID: %q, %v", body.Model, err)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, check.stream)
			}))
			defer server.Close()
			pinnedURL, err := url.Parse(check.base)
			if err != nil {
				t.Fatal(err)
			}
			base := server.URL + pinnedURL.Path
			catalog, err = write(base)
			if err != nil {
				t.Fatal(err)
			}
			model, _, err := catalog.Resolve("")
			if err != nil {
				t.Fatal(err)
			}
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "done" || requests != 1 {
				t.Fatalf("override turn: %+v, requests=%d, err=%v", turn, requests, err)
			}
		})
	}
}

func TestCatalogXAIResponsesTextAndToolRoute(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	write := func(base string) (Catalog, error) {
		data := fmt.Sprintf(`{"default":"grok","models":[{"key":"grok","provider":"catalog","providerId":"xai","id":"grok-4.3","baseUrl":%q,"apiKey":"xai-test-key","reasoningEffort":"high"}]}`, base)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			return Catalog{}, err
		}
		return Load(root)
	}
	catalog, err := write("")
	if err != nil || catalog.Models[0].Provider != "openai-responses" || catalog.Models[0].BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("xAI catalog route: %+v %v", catalog, err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer xai-test-key" {
			t.Errorf("xAI route or authorization: %s, %q", r.URL, r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string           `json:"model"`
			Store     bool             `json:"store"`
			Input     []map[string]any `json:"input"`
			Tools     []map[string]any `json:"tools"`
			Include   []string         `json:"include"`
			Reasoning map[string]any   `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "grok-4.3" || body.Store || len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" || body.Reasoning["effort"] != "high" {
			t.Errorf("xAI model request: %+v, %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			if len(body.Tools) != 1 || body.Tools[0]["name"] != "lookup" {
				t.Errorf("xAI function schema: %+v", body.Tools)
			}
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			return
		}
		if len(body.Input) != 3 || body.Input[2]["type"] != "function_call_output" || body.Input[2]["call_id"] != "call_1" {
			t.Errorf("xAI tool result history: %+v", body.Input)
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	catalog, err = write(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}
	messages := []provider.ChatMessage{{Role: "user", Content: "look up something"}}
	turn, err := provider.ModelTurn(context.Background(), model, messages, []map[string]any{schema})
	if err != nil || len(turn.Calls) != 1 || turn.Calls[0].ID != "call_1" {
		t.Fatalf("xAI function turn: %+v %v", turn, err)
	}
	messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: turn.Calls}, provider.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "found"})
	turn, err = provider.ModelTurn(context.Background(), model, messages, []map[string]any{schema})
	if err != nil || turn.Text != "done" || requests != 2 {
		t.Fatalf("xAI text turn: %+v, requests=%d, err=%v", turn, requests, err)
	}
}

func TestCloudflareGatewayRequiresAccountOrBaseURL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	t.Setenv("CLOUDFLARE_API_TOKEN", "gateway-secret")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account123")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "gateway-one")
	data := `{"default":"claude","models":[{"key":"claude","provider":"catalog","providerId":"cloudflare-ai-gateway","id":"anthropic/claude-opus-4.5"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "anthropic" || model.ProviderID != "cloudflare-ai-gateway" || model.BaseURL != "https://api.cloudflare.com/client/v4/accounts/account123/ai/v1" || model.GatewayID != "gateway-one" || model.APIKey != "gateway-secret" {
		t.Fatalf("Cloudflare catalog route: %+v %v", model, err)
	}
	if len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "CLOUDFLARE_API_TOKEN" {
		t.Fatalf("Cloudflare credential was not isolated: %q", catalog.CredentialEnvironment())
	}
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "accountId") {
		t.Fatalf("missing Cloudflare account was accepted: %v", err)
	}
	data = `{"default":"chat","models":[{"key":"chat","provider":"catalog","providerId":"cloudflare-ai-gateway","id":"alibaba/qwen3-max","baseUrl":"http://127.0.0.1:1234/ai/v1","gatewayId":"alternate","apiKey":"key"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil || catalog.Models[0].Provider != "openai-chat" || catalog.Models[0].BaseURL != "http://127.0.0.1:1234/ai/v1" || catalog.Models[0].GatewayID != "alternate" {
		t.Fatalf("custom Cloudflare gateway route: %+v %v", catalog, err)
	}
}

func TestBothEnvironmentProvidersCanBeConfigured(t *testing.T) {
	t.Setenv("VISTA_OPENAI_API_KEY", "openai-key")
	t.Setenv("VISTA_ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("VISTA_DEFAULT_MODEL", "anthropic")
	catalog, err := FromEnvironment()
	if err != nil || len(catalog.Models) != 3 || catalog.Default != "anthropic" || catalog.Models[1].Provider != "openai-responses" {
		t.Fatalf("environment catalog: %+v %v", catalog, err)
	}
}

func TestGeminiEnvironmentModelCanBeConfigured(t *testing.T) {
	t.Setenv("VISTA_OPENAI_API_KEY", "")
	t.Setenv("VISTA_ANTHROPIC_API_KEY", "")
	t.Setenv("VISTA_GEMINI_API_KEY", "gemini-secret")
	t.Setenv("VISTA_GEMINI_MODEL", "gemini-test")
	catalog, err := FromEnvironment()
	if err != nil || len(catalog.Models) != 1 || catalog.Models[0].Provider != "gemini" || catalog.Models[0].ProviderID != "google" || catalog.Models[0].ID != "gemini-test" {
		t.Fatalf("Gemini environment catalog: %+v %v", catalog, err)
	}
}

func TestMistralEnvironmentModelCanBeConfigured(t *testing.T) {
	t.Setenv("VISTA_OPENAI_API_KEY", "")
	t.Setenv("VISTA_ANTHROPIC_API_KEY", "")
	t.Setenv("VISTA_GEMINI_API_KEY", "")
	t.Setenv("VISTA_MISTRAL_API_KEY", "mistral-secret")
	catalog, err := FromEnvironment()
	if err != nil || len(catalog.Models) != 1 || catalog.Models[0].Provider != "mistral" || catalog.Models[0].ID != "codestral-latest" {
		t.Fatalf("Mistral environment model: %+v %v", catalog, err)
	}
}

func TestPinnedCatalogModelResolvesProtocolAndRoute(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := `{"default":"deepseek","models":[{"key":"deepseek","provider":"catalog","providerId":"deepseek","id":"deepseek-flash","apiKey":"private-key"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	public := catalog.Public()
	if len(public) != 1 || public[0].Tools == nil || !*public[0].Tools || public[0].Context != 1000000 || public[0].OutputLimit != 384000 || !public[0].CatalogReasoning || public[0].Status != "active" {
		t.Fatalf("pinned model capabilities: %+v", public)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.Provider != "openai-chat" || model.ProviderID != "deepseek" || model.BaseURL != "https://api.deepseek.com/v1" || model.APIKey != "private-key" || model.Context != 1000000 || model.InputLimit != 0 {
		t.Fatalf("catalog route: %+v %v", model, err)
	}
	if err := os.WriteFile(path, []byte(`{"default":"bad","models":[{"key":"bad","provider":"catalog","providerId":"amazon-bedrock","id":"x","apiKey":"key"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("unimplemented native Provider route was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"default":"openai","models":[{"key":"openai","provider":"catalog","providerId":"openai","id":"gpt-4.1-mini","apiKey":"key"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil || catalog.Models[0].Provider != "openai-responses" {
		t.Fatalf("OpenAI Responses route: %+v %v", catalog, err)
	}
	if err := os.WriteFile(path, []byte(`{"default":"mistral","models":[{"key":"mistral","provider":"catalog","providerId":"mistral","id":"codestral-latest","apiKey":"key"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil || catalog.Models[0].Provider != "mistral" || catalog.Models[0].Context != 256000 || catalog.Models[0].Tools == nil || !*catalog.Models[0].Tools {
		t.Fatalf("Mistral catalog route and capabilities: %+v %v", catalog, err)
	}
}

func TestPinnedCatalogCredentialUsesProviderEnvironmentWithoutWorkerLeak(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEEPSEEK_API_KEY", "catalog-env-secret")
	path := filepath.Join(root, "models.json")
	data := `{"default":"deepseek","models":[{"key":"deepseek","provider":"catalog","providerId":"deepseek","id":"deepseek-flash"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "DEEPSEEK_API_KEY" {
		t.Fatalf("catalog environment credential: %+v %v", catalog.CredentialEnvironment(), err)
	}
	model, _, err := catalog.Resolve("")
	if err != nil || model.APIKey != "catalog-env-secret" {
		t.Fatalf("catalog model credential: %+v %v", model, err)
	}
	filtered := WorkerEnvironment([]string{"PATH=/usr/bin", "DEEPSEEK_API_KEY=catalog-env-secret", "UNRELATED=value"}, catalog.CredentialEnvironment()...)
	if len(filtered) != 2 || filtered[0] != "PATH=/usr/bin" || filtered[1] != "UNRELATED=value" {
		t.Fatalf("configured credential reached Worker environment: %q", filtered)
	}
}

func TestCatalogCredentialSkipsResourceEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AZURE_RESOURCE_NAME", "resource-is-not-a-key")
	t.Setenv("AZURE_API_KEY", "actual-secret")
	path := filepath.Join(root, "models.json")
	data := `{"default":"azure","models":[{"key":"azure","provider":"catalog","providerId":"azure","id":"gpt-4.1-mini","baseUrl":"http://127.0.0.1:1234/models"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil || catalog.Models[0].APIKey != "actual-secret" || len(catalog.CredentialEnvironment()) != 1 || catalog.CredentialEnvironment()[0] != "AZURE_API_KEY" {
		t.Fatalf("catalog selected a resource as a credential: %+v %v", catalog, err)
	}
	t.Setenv("AZURE_API_KEY", "")
	if _, err := Load(root); err == nil {
		t.Fatal("resource name alone satisfied a catalog credential")
	}
}

func TestCatalogModelCapabilitiesRejectInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	for _, check := range []struct{ name, model string }{
		{"unknown", `{"key":"x","provider":"catalog","providerId":"deepseek","id":"not-in-snapshot","apiKey":"key"}`},
		{"context override", `{"key":"x","provider":"catalog","providerId":"openai","id":"gpt-4.1-mini","apiKey":"key","context":9999999}`},
		{"image output", `{"key":"x","provider":"catalog","providerId":"openai","id":"gpt-image-1","apiKey":"key"}`},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(`{"default":"x","models":[`+check.model+`]}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil {
				t.Fatal("invalid pinned capability was accepted")
			}
		})
	}
}
