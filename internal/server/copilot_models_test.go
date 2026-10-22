package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/copilotauth"
	"vista/internal/modelcatalog"
	"vista/internal/store"
)

func TestCopilotDiscoveryOverridesPinnedRouteAndRetainsInventoryOnFailure(t *testing.T) {
	wantAgent := copilotauth.UserAgent

	var fail atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer copilot-token" || r.Header.Get("User-Agent") != wantAgent || r.Header.Get("X-GitHub-Api-Version") != "2026-08-01" {
			t.Errorf("Copilot discovery request: %s, headers=%+v", r.URL, r.Header)
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[
			{"id":"gpt-6-sol","model_picker_enabled":true,"supported_endpoints":["/chat/completions"],"capabilities":{"limits":{"max_context_window_tokens":9000,"max_prompt_tokens":8000,"max_output_tokens":1000},"supports":{"tool_calls":true,"vision":true}}},
			{"id":"new-copilot","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500,"vision":{"supported_media_types":["image/png"]}},"supports":{"tool_calls":false,"vision":false}}},
			{"id":"text-only","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true,"vision":false}}},
			{"id":"disabled","model_picker_enabled":true,"policy":{"state":"disabled"},"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}},
			{"id":"hidden","model_picker_enabled":false,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}},
			{"id":"missing-tools","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{}}},
			{"id":"unknown-route","model_picker_enabled":true,"supported_endpoints":["/unknown"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}}
		]}`)
	}))
	defer endpoint.Close()
	root := t.TempDir()
	data := []byte(fmt.Sprintf(`{"default":"copilot","models":[{"key":"copilot","provider":"catalog","providerId":"github-copilot","id":"gpt-6-sol","baseUrl":%q}]}`, endpoint.URL))
	if err := os.WriteFile(filepath.Join(root, "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "copilot-token")
	base, err := modelcatalog.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}}
	app.refreshCopilotModels(context.Background())
	catalog := app.modelsSnapshot()
	if len(catalog.Models) != 3 || catalog.Default != "copilot" {
		t.Fatalf("Copilot discovered inventory: %+v", catalog)
	}
	first := catalog.Models[0]
	if first.Key != "copilot" || first.Provider != "openai-chat" || first.Context != 9000 || first.Info().InputLimit != 8000 || first.Info().OutputLimit != 1000 || first.Info().DiscoverySource != "github-copilot" || !slices.Contains(first.EffectiveInputModalities(), "image") {
		t.Fatalf("Copilot pinned model did not use advertised route and limits: %+v", first)
	}
	second := catalog.Models[1]
	if second.ID != "new-copilot" || !strings.HasPrefix(second.Key, "copilot-") || second.Provider != "openai-responses" || second.Context != 4000 || second.Tools == nil || *second.Tools || second.Info().DiscoverySource != "github-copilot" || !slices.Contains(second.EffectiveInputModalities(), "image") {
		t.Fatalf("Copilot discovered model: %+v", second)
	}
	third := catalog.Models[2]
	if third.ID != "text-only" || slices.Contains(third.EffectiveInputModalities(), "image") {
		t.Fatalf("Copilot text-only model gained image capability: %+v", third)
	}
	public := catalog.Public()
	publicByID := map[string]modelInfo{}
	for _, model := range public {
		publicByID[model.ID] = model
	}
	if len(public) != 3 || publicByID["gpt-6-sol"].DiscoverySource != "github-copilot" || !slices.Contains(publicByID["gpt-6-sol"].InputModalities, "image") || !slices.Contains(publicByID["new-copilot"].InputModalities, "image") || slices.Contains(publicByID["text-only"].InputModalities, "image") {
		t.Fatalf("Copilot discovered image capabilities missing from public catalog: %+v", public)
	}
	fail.Store(true)
	app.refreshCopilotModels(context.Background())
	if after := app.modelsSnapshot(); len(after.Models) != 3 || after.Models[0].Provider != "openai-chat" || !slices.Contains(after.Models[0].EffectiveInputModalities(), "image") {
		t.Fatalf("Copilot transient failure discarded successful inventory: %+v", after)
	}
}

func TestCopilotDiscoveryRequiresConfiguredModel(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer endpoint.Close()
	t.Setenv("GITHUB_TOKEN", "ambient-token")
	base := modelCatalog{}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}}
	app.refreshCopilotModels(context.Background())
	if calls.Load() != 0 || len(app.modelsSnapshot().Models) != 0 {
		t.Fatal("ambient token triggered Copilot discovery")
	}
	if _, err := modelcatalog.DiscoverCopilotModels(context.Background(), configuredModel{BaseURL: "http://example.com", APIKey: "token"}); err == nil {
		t.Fatal("external HTTP Copilot discovery accepted")
	}
}

func TestCopilotDiscoveryPrefersMessagesAndRejectsIncompleteInventory(t *testing.T) {
	var incomplete atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if incomplete.Load() {
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-new","model_picker_enabled":true,"supported_endpoints":["/chat/completions","/responses","/v1/messages"],"capabilities":{"limits":{"max_prompt_tokens":8000,"max_output_tokens":1000},"supports":{"tool_calls":true}}}]}`)
	}))
	defer endpoint.Close()
	source := configuredModel{BaseURL: endpoint.URL + "/v1", APIKey: "token"}
	models, err := modelcatalog.DiscoverCopilotModels(context.Background(), source)
	if err != nil || len(models) != 1 || models[0].Provider != "anthropic" || models[0].BaseURL != endpoint.URL+"/v1" {
		t.Fatalf("Copilot Messages route: %+v, err=%v", models, err)
	}
	incomplete.Store(true)
	if _, err := modelcatalog.DiscoverCopilotModels(context.Background(), source); err == nil {
		t.Fatal("incomplete Copilot inventory accepted")
	}
}

func TestCopilotSuccessfulInventoryRemovesMissingModelsOnlyForItsAccount(t *testing.T) {
	var fail atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") == "Bearer second-token" {
			fmt.Fprint(w, `{"data":[{"id":"missing","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"available","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}},{"id":"new-model","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}}]}`)
	}))
	defer endpoint.Close()
	otherEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer otherEndpoint.Close()
	model := func(key, id, token, base string) configuredModel {
		return configuredModel{Key: key, Provider: "openai-chat", ProviderID: "github-copilot", ID: id, BaseURL: base, APIKey: token}
	}
	base := modelCatalog{Default: "missing", Models: []configuredModel{
		model("missing", "missing", "first-token", endpoint.URL),
		model("available", "available", "first-token", endpoint.URL),
		model("other-account", "missing", "second-token", endpoint.URL),
		model("other-endpoint", "missing", "first-token", otherEndpoint.URL),
	}}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}}
	fail.Store(true)
	app.refreshCopilotModels(context.Background())
	if got := app.modelsSnapshot(); len(got.Models) != 4 || got.Default != "missing" {
		t.Fatalf("initial discovery failure must retain pinned models: %+v", got)
	}
	fail.Store(false)
	app.refreshCopilotModels(context.Background())
	got := app.modelsSnapshot()
	if len(got.Models) != 4 || got.Default != "available" || got.Models[0].Key != "available" || got.Models[0].Provider != "openai-responses" || got.Models[1].Key != "other-account" || got.Models[2].Key != "other-endpoint" || got.Models[3].ID != "new-model" {
		t.Fatalf("successful discovery did not filter the source account: %+v", got)
	}
	if _, _, err := got.ResolveForRun(context.Background(), "missing"); err == nil {
		t.Fatal("missing source-account model is still runnable")
	}
	if selected, key, err := got.ResolveForRun(context.Background(), ""); err != nil || key != "available" || selected.Provider != "openai-responses" {
		t.Fatalf("default model was not switched: key=%q, model=%+v, err=%v", key, selected, err)
	}
	fail.Store(true)
	app.refreshCopilotModels(context.Background())
	if after := app.modelsSnapshot(); len(after.Models) != 4 || after.Default != "available" {
		t.Fatalf("temporary failure discarded successful inventory: %+v", after)
	}
}

func TestCopilotEmptySuccessfulInventoryDoesNotRestorePinnedModels(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer endpoint.Close()
	base := modelCatalog{Default: "copilot", Models: []configuredModel{{Key: "copilot", Provider: "openai-chat", ProviderID: "github-copilot", ID: "unavailable", BaseURL: endpoint.URL, APIKey: "token"}}}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}, workers: map[string]*worker{}, started: time.Now()}
	app.refreshCopilotModels(context.Background())
	got := app.modelsSnapshot()
	if len(got.Models) != 0 || got.Default != "" || len(got.Public()) != 0 {
		t.Fatalf("empty successful inventory restored pinned model: %+v", got)
	}
	if _, _, err := got.ResolveForRun(context.Background(), ""); err == nil {
		t.Fatal("default model remained runnable after empty inventory")
	}
	w := httptest.NewRecorder()
	app.modelList(w, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var list struct {
		Default string      `json:"default"`
		Models  []modelInfo `json:"models"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Default != "" || len(list.Models) != 0 {
		t.Fatalf("public model list leaked pinned model: %d %s", w.Code, w.Body.String())
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app.store = st
	w = httptest.NewRecorder()
	app.status(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var status struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Default  string `json:"defaultModel"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Provider != "" || status.Model != "" || status.Default != "" {
		t.Fatalf("status advertised unavailable model: %d %s", w.Code, w.Body.String())
	}
}

func TestCopilotDiscoverySeparatesAccountsAtOneEndpoint(t *testing.T) {
	var secondEmpty, firstFails atomic.Bool
	var firstCalls, secondCalls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer first-token":
			firstCalls.Add(1)
			if firstFails.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"shared","model_picker_enabled":true,"supported_endpoints":["/chat/completions"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}},{"id":"first-only","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}}]}`)
		case "Bearer second-token":
			secondCalls.Add(1)
			if secondEmpty.Load() {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"shared","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":8000,"max_output_tokens":1000},"supports":{"tool_calls":false}}},{"id":"second-only","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":4000,"max_output_tokens":500},"supports":{"tool_calls":true}}}]}`)
		default:
			t.Errorf("unexpected Copilot credential")
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer endpoint.Close()
	base := modelCatalog{Default: "first", Models: []configuredModel{
		{Key: "first", Provider: "openai-chat", ProviderID: "github-copilot", ID: "shared", BaseURL: endpoint.URL, APIKey: "first-token"},
		{Key: "second", Provider: "openai-chat", ProviderID: "github-copilot", ID: "shared", BaseURL: endpoint.URL, APIKey: "second-token"},
	}}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}}
	app.refreshCopilotModels(context.Background())
	got := app.modelsSnapshot()
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 || len(got.Models) != 4 || got.Models[0].Key != "first" || got.Models[0].Provider != "openai-chat" || got.Models[1].Key != "second" || got.Models[1].Provider != "openai-responses" || got.Models[1].Context != 8000 {
		t.Fatalf("Copilot accounts were not independently discovered: %+v", got)
	}
	if got.Models[2].Key == got.Models[3].Key || got.Models[2].ID != "first-only" || got.Models[3].ID != "second-only" {
		t.Fatalf("Copilot discovered keys or source order collided: %+v", got.Models)
	}
	firstFails.Store(true)
	secondEmpty.Store(true)
	app.refreshCopilotModels(context.Background())
	got = app.modelsSnapshot()
	if len(got.Models) != 2 || got.Models[0].Key != "first" || got.Models[0].Provider != "openai-chat" || got.Models[1].ID != "first-only" {
		t.Fatalf("one account's failure or empty inventory affected the other: %+v", got)
	}
	if _, _, err := got.ResolveForRun(context.Background(), "second"); err == nil {
		t.Fatal("second account's unavailable pinned model remained runnable")
	}
}

func TestStatusWithoutProviderHasNoDefaultModel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app := &App{store: st, workers: map[string]*worker{}, models: modelCatalog{}, started: time.Now()}
	w := httptest.NewRecorder()
	app.status(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var status struct {
		State      string `json:"state"`
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		Default    string `json:"defaultModel"`
		DefaultKey string `json:"defaultModelKey"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "running" || status.Provider != "" || status.Model != "" || status.Default != "" || status.DefaultKey != "" {
		t.Fatalf("empty status: %d %s", w.Code, w.Body.String())
	}
}
