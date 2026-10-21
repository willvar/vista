package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"vista/internal/modelcatalog"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

func TestNamedCustomResponsesDiscoveryAndReply(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("custom Responses credential missing")
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6-luna"}]}`))
		case "/v1/responses":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gpt-6-luna" {
				t.Errorf("wrong Responses request: %+v %v", body, err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Hi from Herdex\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusForbidden) // Herdex can return 403 for its unused route.
		default:
			http.Error(w, "unexpected path", 404)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	demo := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: demo, models: demo}
	base := upstream.URL + "/v1"
	discovery := httptest.NewRecorder()
	app.discoverCustomModels(discovery, httptest.NewRequest(http.MethodPost, "/api/model-discovery", strings.NewReader(`{"provider":"openai-responses","credentialMode":"bearer","baseUrl":"`+base+`","apiKey":"private-key"}`)))
	if discovery.Code != 200 || !strings.Contains(discovery.Body.String(), "gpt-6-luna") {
		t.Fatalf("Responses discovery: %d %s", discovery.Code, discovery.Body.String())
	}
	save := httptest.NewRecorder()
	app.providerSettings(save, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"name":"Herdex","provider":"openai-responses","providerId":"custom","credentialMode":"bearer","baseUrl":"`+base+`","apiKey":"private-key","ids":["gpt-6-luna"]}`)))
	if save.Code != http.StatusNoContent {
		t.Fatalf("Responses save: %d %s", save.Code, save.Body.String())
	}
	configured := app.modelsSnapshot()
	if configured.Default != "herdex-gpt-6-luna" || configured.Models[0].Key != configured.Default {
		t.Fatalf("custom model key omitted provider name: %q", configured.Default)
	}
	if len(configured.Models) != 1 || configured.Models[0].Name != "Herdex" || configured.Models[0].Provider != "openai-responses" || configured.Models[0].Authentication != "bearer" || !modelcatalog.Editable(configured.Models[0]) {
		t.Fatalf("named Responses model: %+v", configured.Models)
	}
	if options := configured.Public()[0].ReasoningOptions; !containsString(options, "high") || !containsString(options, "max") {
		t.Fatalf("Herdex Luna reasoning options missing: %v", options)
	}
	model, _, err := configured.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "Hi"}}, nil)
	if err != nil || turn.Text != "Hi from Herdex" {
		t.Fatalf("Responses reply: %+v %v", turn, err)
	}
	public := configured.Public()
	if len(public) != 1 || public[0].Name != "Herdex" || public[0].BaseURL != base {
		t.Fatalf("public named model: %+v", public)
	}
	settings := httptest.NewRecorder()
	app.modelSettings(settings, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if !strings.Contains(settings.Body.String(), `"name":"Herdex"`) || strings.Contains(settings.Body.String(), "private-key") {
		t.Fatalf("unsafe model settings: %s", settings.Body.String())
	}
	add := httptest.NewRecorder()
	app.providerSettings(add, httptest.NewRequest(http.MethodPut, "/api/provider-config", strings.NewReader(`{"key":"`+configured.Models[0].Key+`","name":"Herdex","provider":"openai-responses","providerId":"custom","credentialMode":"bearer","baseUrl":"`+base+`","ids":["gpt-6-luna","gpt-5.6-luna"]}`)))
	if add.Code != http.StatusNoContent || app.modelsSnapshot().Models[1].Name != "Herdex" {
		t.Fatalf("connection name or credential not reused: %d %s", add.Code, add.Body.String())
	}
	rename := httptest.NewRecorder()
	app.providerSettings(rename, httptest.NewRequest(http.MethodPut, "/api/provider-config", strings.NewReader(`{"key":"`+configured.Models[0].Key+`","name":"Herdex Pool","provider":"openai-responses","providerId":"custom","credentialMode":"bearer","baseUrl":"`+base+`"}`)))
	models := app.modelsSnapshot().Models
	if rename.Code != http.StatusNoContent || models[0].Name != "Herdex Pool" || models[1].Name != "Herdex Pool" {
		t.Fatalf("provider rename did not reach all models: %d %s %+v", rename.Code, rename.Body.String(), models)
	}
}

func TestCustomProvidersWithSameModelIDKeepDistinctKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}}
	for _, item := range []struct{ name, endpoint string }{{"Herdex", "http://127.0.0.1:12001/v1"}, {"Other", "http://127.0.0.1:12002/v1"}} {
		if err := app.changeProviderSettings(context.Background(), http.MethodPost, providerSettingsInput{Name: item.name, Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: item.endpoint, APIKey: "private-key", IDs: []string{"gpt-6-astra"}}); err != nil {
			t.Fatal(err)
		}
	}
	models := app.modelsSnapshot()
	if len(models.Models) != 2 || models.Models[0].Key != "herdex-gpt-6-astra" || models.Models[1].Key != "other-gpt-6-astra" {
		t.Fatalf("provider model keys: %+v", models.Models)
	}
}

func TestCustomDiscoveryUsesSelectedFormatWithoutProbingInference(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"custom-model"}]}`))
		default:
			t.Errorf("discovery probed inference endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	demo := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: demo, models: demo}
	base := upstream.URL + "/v1"
	discovery := httptest.NewRecorder()
	app.discoverCustomModels(discovery, httptest.NewRequest(http.MethodPost, "/api/model-discovery", strings.NewReader(`{"provider":"openai-responses","credentialMode":"bearer","baseUrl":"`+base+`","apiKey":"private-key"}`)))
	if discovery.Code != http.StatusOK || strings.Contains(discovery.Body.String(), `"protocol"`) {
		t.Fatalf("ambiguous discovery: %d %s", discovery.Code, discovery.Body.String())
	}
	saved := httptest.NewRecorder()
	app.providerSettings(saved, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"provider":"openai-responses","providerId":"custom","credentialMode":"bearer","baseUrl":"`+base+`","apiKey":"private-key"}`)))
	if saved.Code != http.StatusNoContent || app.modelsSnapshot().Models[0].Provider != "openai-responses" {
		t.Fatalf("selected format was not preserved: %d %s", saved.Code, saved.Body.String())
	}
}

type modelListTransport func(*http.Request) (*http.Response, error)

func (f modelListTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func stubZenModelList(t *testing.T, providerID, key string, ids ...string) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = modelListTransport(func(request *http.Request) (*http.Response, error) {
		expectedPath := "/zen/v1/models"
		if providerID == "opencode-go" {
			expectedPath = "/zen/go/v1/models"
		}
		if request.URL.Host != "opencode.ai" || request.URL.Path != expectedPath || request.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("unexpected Zen model discovery request: %s %s", request.URL.Host, request.URL.Path)
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)), Header: http.Header{}}, nil
		}
		data := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		body, _ := json.Marshal(map[string]any{"data": data})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestCustomModelDiscoveryAndBatchSave(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("unexpected discovery request")
			http.Error(w, "private upstream text", 401)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"second"},{"id":"first"},{"id":"second"}]}`))
	}))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerSettingsInput{Name: "Example", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: "private-key", IDs: []string{"first", "second"}}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
		t.Fatal(err)
	}
	models := app.modelsSnapshot()
	if len(models.Models) != 2 || models.Default != models.Models[0].Key || models.Models[0].ID != "first" || models.Models[1].ID != "second" {
		t.Fatal("batch did not preserve models and default")
	}
	input.Key, input.APIKey = models.Default, ""
	before, err := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	input.IDs = []string{"third", "third"}
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	after, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed batch changed settings")
	}
	discovery := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"provider": input.Provider, "credentialMode": input.CredentialMode, "baseUrl": input.BaseURL, "connectionId": models.Models[0].ConnectionID})
	app.discoverCustomModels(discovery, httptest.NewRequest(http.MethodPost, "/api/model-discovery", bytes.NewReader(body)))
	if discovery.Code != 200 || !strings.Contains(discovery.Body.String(), `"models":["first","second"]`) {
		t.Fatalf("saved credential discovery: %d %s", discovery.Code, discovery.Body.String())
	}
	input.IDs = []string{"first", "second", "third"}
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err != nil {
		t.Fatal(err)
	}
	updated := app.modelsSnapshot()
	if len(updated.Models) != 3 || updated.Default != models.Default || updated.Models[0].Key != models.Default || updated.Models[2].ID != "third" || updated.Models[2].APIKey != "private-key" {
		t.Fatal("editing a connection lost keys, credentials or default")
	}
}

func TestCustomModelDiscoveryFailureDoesNotExposeBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sensitive upstream details", 403)
	}))
	defer upstream.Close()
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/api/model-discovery", strings.NewReader(`{"provider":"openai-chat","credentialMode":"bearer","baseUrl":"`+upstream.URL+`/v1","apiKey":"test"}`))
	response := httptest.NewRecorder()
	app.discoverCustomModels(response, request)
	if response.Code != 502 || strings.Contains(response.Body.String(), "sensitive upstream details") {
		t.Fatalf("unsafe failure: %d %s", response.Code, response.Body.String())
	}
}

func TestZenProviderBatchKeepsOneDefaultAndStableKeys(t *testing.T) {
	stubZenModelList(t, "opencode", "zen-key", "deepseek-v4-flash", "gpt-5.4", "claude-sonnet-4-6")
	app := directoryTestApp(t)
	input := providerSettingsInput{Provider: "catalog", ProviderID: "opencode", APIKey: "zen-key", IDs: []string{"deepseek-v4-flash", "gpt-5.4"}}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
		t.Fatal(err)
	}
	models := app.modelsSnapshot()
	if len(models.Models) != 2 || models.Default != models.Models[0].Key || models.Models[0].ProviderID != "opencode" {
		t.Fatal("Zen models or default missing")
	}
	input.Key, input.APIKey = models.Default, ""
	before, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	input.IDs = []string{"claude-sonnet-4-6", "not-in-catalog"}
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err == nil {
		t.Fatal("invalid batch accepted")
	}
	after, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("invalid Zen batch changed settings")
	}
	input.IDs = []string{"deepseek-v4-flash", "gpt-5.4", "claude-sonnet-4-6"}
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err != nil {
		t.Fatal(err)
	}
	models = app.modelsSnapshot()
	if len(models.Models) != 3 || models.Default != input.Key || models.Models[2].APIKey != "zen-key" {
		t.Fatal("existing default or credential changed")
	}
}

func TestZenDiscoveryExcludesAbsentLegacyModelAndSaveRejectsIt(t *testing.T) {
	stubZenModelList(t, "opencode-go", "zen-key", "deepseek-v4-flash", "unknown-new-model")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	demo := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: demo, models: demo}
	response := httptest.NewRecorder()
	app.discoverZenModels(response, httptest.NewRequest(http.MethodPost, "/api/zen-model-discovery", strings.NewReader(`{"providerId":"opencode-go"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("anonymous Zen discovery accepted: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	app.discoverZenModels(response, httptest.NewRequest(http.MethodPost, "/api/zen-model-discovery", strings.NewReader(`{"providerId":"opencode-go","apiKey":"zen-key"}`)))
	if response.Code != 200 || strings.Contains(response.Body.String(), `"retired-model"`) || !strings.Contains(response.Body.String(), `"models":["deepseek-v4-flash"]`) || !strings.Contains(response.Body.String(), `"unsupported":["unknown-new-model"]`) {
		t.Fatalf("live Zen inventory: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	app.providerSettings(response, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"provider":"catalog","providerId":"opencode-go","apiKey":"zen-key","ids":["retired-model"]}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "absent from the current") {
		t.Fatalf("absent model accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestSessionReasoningEffortFollowsSelectedModel(t *testing.T) {
	tests := []struct {
		name   string
		model  modelcfg.Model
		effort string
		valid  bool
		field  string
	}{
		{"Zen GPT", modelcfg.Model{Provider: "openai-responses", ProviderID: "opencode", ID: "gpt-5.4"}, "high", true, "responses"},
		{"Zen GPT none is absent from OMP", modelcfg.Model{Provider: "openai-responses", ProviderID: "opencode", ID: "gpt-5.4"}, "none", false, ""},
		{"Zen Claude max is absent from OMP", modelcfg.Model{Provider: "anthropic", ProviderID: "opencode", ID: "claude-sonnet-4-6"}, "max", false, ""},
		{"Zen Gemini minimal", modelcfg.Model{Provider: "gemini", ProviderID: "opencode", ID: "gemini-3-flash"}, "minimal", true, "gemini"},
		{"Zen DeepSeek max", modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode", ID: "deepseek-v4-flash"}, "max", true, "chat"},
		{"Herdex Luna high", modelcfg.Model{Provider: "openai-responses", ID: "gpt-6-luna"}, "high", true, "responses"},
		{"Herdex Sol 6.1 max", modelcfg.Model{Provider: "openai-responses", ID: "gpt-6.1-sol"}, "max", true, "responses"},
		{"custom Chat Sol 6.1 low", modelcfg.Model{Provider: "openai-chat", ID: "gpt-6.1-sol"}, "low", true, "chat"},
		{"Sol 6.1 does not offer none", modelcfg.Model{Provider: "openai-responses", ID: "gpt-6.1-sol"}, "none", false, ""},
		{"custom Chat has no OMP identity", modelcfg.Model{Provider: "openai-chat", ID: "custom"}, "medium", false, ""},
		{"Zen Big Pickle high", modelcfg.Model{Provider: "openai-chat", ProviderID: "opencode", ID: "big-pickle"}, "high", true, "chat"},
		{"Claude does not offer xhigh", modelcfg.Model{Provider: "anthropic", ProviderID: "opencode", ID: "claude-sonnet-4-6"}, "xhigh", false, ""},
		{"unsupported effort", modelcfg.Model{Provider: "openai-responses", ProviderID: "opencode", ID: "gpt-5.4"}, "ultra", false, ""},
	}
	for _, check := range tests {
		t.Run(check.name, func(t *testing.T) {
			model := check.model
			err := modelcatalog.ApplyReasoningEffort(&model, check.effort)
			if (err == nil) != check.valid {
				t.Fatalf("override: %v", err)
			}
			if !check.valid {
				return
			}
			switch check.field {
			case "responses":
				if model.ReasoningEffort != check.effort {
					t.Fatal("Responses effort missing")
				}
			case "messages":
				if model.Effort != check.effort {
					t.Fatal("Messages effort missing")
				}
			case "gemini":
				if model.ThinkingLevel != check.effort {
					t.Fatal("Gemini level missing")
				}
			case "chat":
				if model.ReasoningEffort != check.effort || !model.ChatReasoningOverride {
					t.Fatal("Chat effort missing")
				}
			}
		})
	}
}

func TestNewModelReasoningOptionsReachCustomProviderRequests(t *testing.T) {
	for _, protocol := range []string{"openai-responses", "openai-chat"} {
		t.Run(protocol, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model     string `json:"model"`
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
					Effort string `json:"reasoning_effort"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gpt-6.1-sol" {
					t.Errorf("model request: %+v, %v", body, err)
					http.Error(w, "invalid model", 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == "openai-responses" {
					if r.URL.Path != "/v1/responses" || body.Reasoning.Effort != "max" {
						t.Errorf("Responses effort missing: %+v", body)
					}
					_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
				} else {
					if r.URL.Path != "/v1/chat/completions" || body.Effort != "max" {
						t.Errorf("Chat effort missing: %+v", body)
					}
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
				}
			}))
			defer upstream.Close()
			catalog := modelCatalog{Models: []configuredModel{{Key: "herdex-sol", Name: "Herdex", Provider: protocol, ProviderID: "custom", Authentication: "bearer", ID: "gpt-6.1-sol", BaseURL: upstream.URL + "/v1", APIKey: "test-key"}}}
			want := []string{"low", "medium", "high", "xhigh", "max"}
			if options := catalog.Public()[0].ReasoningOptions; !slices.Equal(options, want) {
				t.Fatalf("public model options: %v", options)
			}
			model, _, err := catalog.Resolve("herdex-sol")
			if err != nil {
				t.Fatal(err)
			}
			if err := modelcatalog.ApplyReasoningEffort(&model, "max"); err != nil {
				t.Fatal(err)
			}
			turn, err := provider.ModelTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "Hi"}}, nil)
			if err != nil || turn.Text != "Hi" {
				t.Fatalf("model reply: %+v, %v", turn, err)
			}
		})
	}
}

func TestModelSettingsSaveLiveAndKeepCredentialsPrivate(t *testing.T) {
	app := directoryTestApp(t)
	input := providerSettingsInput{Name: "Local", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: "http://127.0.0.1:11434/v1", APIKey: "private-key", IDs: []string{"tool-model"}}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
		t.Fatal(err)
	}
	input.Key = app.modelsSnapshot().Default
	if selected, _, err := app.modelsSnapshot().Resolve(input.Key); err != nil || selected.APIKey != "private-key" || selected.BaseURL != input.BaseURL {
		t.Fatal("live model was not updated")
	}
	info, err := os.Stat(filepath.Join(app.configDir, "models.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings file is not private: %v", err)
	}
	get := httptest.NewRecorder()
	app.modelSettings(get, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if get.Code != http.StatusOK || bytes.Contains(get.Body.Bytes(), []byte("private-key")) || !bytes.Contains(get.Body.Bytes(), []byte(`"hasApiKey":true`)) {
		t.Fatal("settings response leaked or lost credential metadata")
	}
	before, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	input.BaseURL, input.APIKey = "ftp://example.com/v1", "replacement"
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	after, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("invalid endpoint changed settings")
	}
	input.BaseURL, input.APIKey, input.IDs = "http://127.0.0.1:11434/v1", "", []string{"tool-model-2"}
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err != nil {
		t.Fatal(err)
	}
	reloaded, err := modelcatalog.Load(app.configDir)
	if err != nil || reloaded.Models[0].ID != "tool-model-2" || reloaded.Models[0].APIKey != "private-key" || reloaded.Models[0].Authentication != "bearer" {
		t.Fatal("reload lost connection settings")
	}
	if err := app.changeProviderSettings(context.Background(), http.MethodDelete, providerSettingsInput{Key: reloaded.Models[0].Key}); err != nil {
		t.Fatal(err)
	}
	if app.modelsSnapshot().Default != "demo" {
		t.Fatal("disconnect did not restore demo")
	}
}

func TestModelSettingsDoNotFlattenAdvancedConfig(t *testing.T) {
	if modelcatalog.Editable(configuredModel{Key: "advanced", Provider: "openai-chat", ID: "model", APIKey: "key", InputModalities: []string{"text", "image"}}) {
		t.Fatal("advanced model options must not be discarded by the basic form")
	}
}

func TestModelSettingsUseConfigurationDirectory(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(configDir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	// A state file must not be interpreted as configuration or changed by settings.
	stateFile := filepath.Join(stateDir, "models.json")
	stateContents := []byte("not a model configuration")
	if err := os.WriteFile(stateFile, stateContents, 0600); err != nil {
		t.Fatal(err)
	}
	demo := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: stateDir, configDir: configDir, baseModels: demo, models: demo}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, providerSettingsInput{Name: "Example", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: "http://127.0.0.1:12001/v1", APIKey: "test-key", IDs: []string{"test-model"}}); err != nil {
		t.Fatalf("save configuration: %v", err)
	}
	raw, exists, err := modelcatalog.Read(configDir)
	if err != nil || !exists || len(raw.Models) != 1 || raw.Models[0].ID != "test-model" {
		t.Fatalf("configuration was not saved in its directory: exists=%v err=%v", exists, err)
	}
	if err := app.changeProviderSettings(context.Background(), http.MethodDelete, providerSettingsInput{Key: raw.Models[0].Key}); err != nil {
		t.Fatalf("delete configuration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "models.json")); !os.IsNotExist(err) {
		t.Fatalf("configuration file remains after disconnect: %v", err)
	}
	if data, err := os.ReadFile(stateFile); err != nil || !bytes.Equal(data, stateContents) {
		t.Fatalf("settings changed the state directory: %v", err)
	}
}

func TestAnonymousCustomSettingsKeepHeadersPrivateAndDiscoverLiveModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Access") != "secret-value" {
			t.Error("custom discovery credentials were not preserved")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"alias-a","supported_endpoints":["/v1/responses"]},{"id":"alias-b","supported_endpoints":["/v1/messages"]}]}`))
	}))
	defer upstream.Close()
	app := directoryTestApp(t)
	input := providerSettingsInput{Provider: "openai-chat", ProviderID: "custom", BaseURL: upstream.URL, CredentialMode: "none", Headers: map[string]string{"X-Access": "secret-value"}}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
		t.Fatal(err)
	}
	app.refreshCustomModels(context.Background())
	models := app.modelsSnapshot()
	if len(models.Models) != 2 {
		t.Fatalf("anonymous inventory has %d models", len(models.Models))
	}
	first, _, err := models.Resolve(models.Default)
	if err != nil || first.Provider != "openai-chat" || first.Authentication != "none" || first.Headers["X-Access"] != "secret-value" {
		t.Fatalf("anchor lost selected format, authentication or headers: %v", err)
	}
	response := httptest.NewRecorder()
	app.modelSettings(response, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if strings.Contains(response.Body.String(), "secret-value") || !strings.Contains(response.Body.String(), `"headerNames":["X-Access"]`) || !strings.Contains(response.Body.String(), `"credentialMode":"none"`) {
		t.Fatalf("incorrect settings projection: %s", response.Body)
	}
	public, _ := json.Marshal(models.Public())
	if strings.Contains(string(public), "secret-value") || strings.Contains(string(public), "X-Access") {
		t.Fatal("public models exposed headers")
	}
	input.Key = models.Default
	input.Headers = map[string]string{"X-Access": ""}
	input.Name = "Renamed"
	if err := app.changeProviderSettings(context.Background(), http.MethodPut, input); err != nil {
		t.Fatal(err)
	}
	loaded, err := modelcatalog.Load(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	app.baseModels = loaded
	app.customInventories = nil
	app.refreshCustomModels(context.Background())
	if len(app.modelsSnapshot().Models) != 2 {
		t.Fatal("reload lost anonymous inventory")
	}
	discovery := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"provider": "openai-chat", "baseUrl": upstream.URL, "connectionId": loaded.Models[0].ConnectionID, "credentialMode": "none"})
	app.discoverCustomModels(discovery, httptest.NewRequest(http.MethodPost, "/api/model-discovery", bytes.NewReader(body)))
	if discovery.Code != 200 {
		t.Fatalf("saved connection discovery: %s", discovery.Body)
	}
}
