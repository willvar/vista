package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"vista/internal/modelcatalog"

	"vista/internal/provider"
)

func TestCatalogConnectionUsesAllModelsWithoutSetupSelection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{configDir: dir, models: base, baseModels: base}
	for _, name := range []string{"First", "Second"} {
		if err := app.changeProviderSettings(ctx, http.MethodPost, providerSettingsInput{Name: name, Provider: "catalog", ProviderID: "zai", APIKey: name + "-key"}); err != nil {
			t.Fatal(err)
		}
	}
	ids := provider.CatalogTextModelIDs("zai")
	if len(ids) < 2 || len(app.modelsSnapshot().Models) != len(ids)*2 {
		t.Fatal("connecting with only a key did not load both model inventories")
	}
	raw, _, err := modelcatalog.Read(dir)
	if err != nil || len(raw.Models) != 2 {
		t.Fatalf("persisted inventory instead of connections: %v", err)
	}
	first := raw.Models[0]
	var chosen configuredModel
	for _, model := range app.modelsSnapshot().Models {
		if model.ConnectionID == first.ConnectionID && model.ID != first.ID {
			chosen = model
			break
		}
	}
	if err := app.selectDefaultModel(ctx, chosen.Key); err != nil {
		t.Fatalf("could not select a model after connection: %v", err)
	}
	if err := app.changeProviderSettings(ctx, http.MethodPut, providerSettingsInput{Key: first.Key, Provider: "catalog", ProviderID: "zai", Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := modelcatalog.Load(dir)
	if err != nil || loaded.Default != chosen.Key || len(loaded.Models) != len(ids)*2 {
		t.Fatalf("reload lost the default or inventory: %v", err)
	}
	for _, model := range loaded.Models {
		if model.ConnectionID == first.ConnectionID {
			if model.APIKey != "First-key" || model.Name != "Renamed" {
				t.Fatal("edit lost the first account's credentials or name")
			}
		} else if model.APIKey != "Second-key" || model.Name != "Second" {
			t.Fatal("edit changed another account")
		}
	}
	if err := app.changeProviderSettings(ctx, http.MethodDelete, providerSettingsInput{Key: first.Key}); err != nil {
		t.Fatal(err)
	}
	if len(app.modelsSnapshot().Models) != len(ids) || app.modelsSnapshot().Default == chosen.Key {
		t.Fatal("disconnect retained models or a default from the removed connection")
	}
}

func TestProviderSetupReadbackMasksCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{configDir: dir, models: base, baseModels: base}
	input := providerSettingsInput{Provider: "catalog", ProviderID: "google-vertex", Project: "test-project", Location: "us-central1", CredentialMode: "accessToken", AccessToken: "private-google-token", IDs: []string{"gemini-2.5-flash"}}
	if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.modelSettings(response, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-google-token") || strings.Contains(response.Body.String(), `"accessToken":`) || strings.Contains(response.Body.String(), `"apiKey":`) {
		t.Fatalf("settings response exposed credentials or failed: %d", response.Code)
	}
	var result struct {
		Models []modelSetting `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Models) != 1 {
		t.Fatalf("settings readback: %v", err)
	}
	item := result.Models[0]
	if !item.HasAccessToken || item.HasAPIKey || item.CredentialMode != "accessToken" || item.Project != input.Project || item.Kind != "google-vertex" || !item.Editable {
		t.Fatalf("settings lost the connection form: %+v", item)
	}
}

func TestProviderConnectionLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6-luna"},{"id":"new-model"}]}`))
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, models: base, baseModels: base}
	call := func(handler http.HandlerFunc, method string, input any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(method, "/api/provider-config", strings.NewReader(string(body))))
		return response
	}
	input := providerSettingsInput{Name: "Herdex", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: "test-key"}
	if response := call(app.providerSettings, http.MethodPost, input); response.Code != http.StatusNoContent {
		t.Fatalf("connect without model IDs: %d %s", response.Code, response.Body.String())
	}
	app.refreshCustomModels(context.Background())
	catalog := app.modelsSnapshot()
	if len(catalog.Models) != 2 {
		t.Fatalf("expected automatic inventory, got %d models", len(catalog.Models))
	}
	anchor := catalog.Models[0].Key
	if catalog.Models[0].ConnectionID == "" {
		t.Fatal("connection has no independent ID")
	}
	var discoveredKey string
	for _, model := range catalog.Models {
		if model.ID == "new-model" {
			discoveredKey = model.Key
		}
	}
	if response := call(app.modelSettings, http.MethodPatch, map[string]string{"key": discoveredKey}); response.Code != http.StatusNoContent {
		t.Fatalf("choose discovered default: %d %s", response.Code, response.Body.String())
	}
	if app.modelsSnapshot().Default != discoveredKey {
		t.Fatal("discovered default was not applied")
	}
	input.Key, input.Name, input.Provider, input.APIKey = anchor, "Renamed", "openai-chat", ""
	if response := call(app.providerSettings, http.MethodPut, input); response.Code != http.StatusNoContent {
		t.Fatalf("edit with saved credential: %d %s", response.Code, response.Body.String())
	}
	raw, _, err := modelcatalog.Read(dir)
	if err != nil || len(raw.Models) != 2 {
		t.Fatalf("cannot read edited connection: %v", err)
	}
	for _, model := range raw.Models {
		if model.Name != "Renamed" || model.APIKey != "test-key" {
			t.Fatal("connection edit did not update all models with the saved credential")
		}
	}
	if reloaded, err := modelcatalog.Load(dir); err != nil || reloaded.Default != discoveredKey {
		t.Fatalf("default did not survive reload: %v", err)
	}
	if response := call(app.providerSettings, http.MethodDelete, providerSettingsInput{Key: anchor}); response.Code != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", response.Code, response.Body.String())
	}
	if catalog := app.modelsSnapshot(); len(catalog.Models) != 1 || catalog.Default != "demo" {
		t.Fatal("disconnect left provider models available")
	}
}

func TestProviderAccountsAtSameEndpointStayIndependent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "first-key" && key != "second-key" && key != "rotated-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"anchor"},{"id":"shared"}]}`))
		case "/v1/chat/completions":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + key + "\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, models: base, baseModels: base}
	call := func(handler http.HandlerFunc, method string, input any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(input)
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(method, "/api/provider-config", strings.NewReader(string(body))))
		return response
	}
	connect := func(name, key string) configuredModel {
		t.Helper()
		input := providerSettingsInput{Name: name, Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: key}
		if response := call(app.providerSettings, http.MethodPost, input); response.Code != http.StatusNoContent {
			t.Fatalf("connect %s: %d %s", name, response.Code, response.Body.String())
		}
		return app.baseModels.Models[len(app.baseModels.Models)-1]
	}
	first, second := connect("First", "first-key"), connect("Second", "second-key")
	// Even an identical name, URL and credential represents a separate connection.
	duplicate := connect("First", "first-key")
	if first.ConnectionID == "" || first.ConnectionID == second.ConnectionID || first.ConnectionID == duplicate.ConnectionID {
		t.Fatal("separate accounts did not get separate connection IDs")
	}
	if response := call(app.providerSettings, http.MethodDelete, providerSettingsInput{Key: duplicate.Key}); response.Code != http.StatusNoContent {
		t.Fatalf("disconnect duplicate: %s", response.Body.String())
	}
	app.refreshCustomModels(context.Background())
	shared := map[string]string{}
	for _, model := range app.modelsSnapshot().Models {
		if model.ID == "shared" {
			shared[model.ConnectionID] = model.Key
		}
	}
	if len(app.modelsSnapshot().Models) != 4 || len(shared) != 2 || shared[first.ConnectionID] == shared[second.ConnectionID] {
		t.Fatal("same endpoint model inventories were merged")
	}
	if response := call(app.modelSettings, http.MethodPatch, map[string]string{"key": shared[second.ConnectionID]}); response.Code != http.StatusNoContent {
		t.Fatalf("default: %s", response.Body.String())
	}
	if response := call(app.discoverCustomModels, http.MethodPost, map[string]string{"provider": "openai-chat", "credentialMode": "bearer", "baseUrl": first.BaseURL}); response.Code != http.StatusBadRequest {
		t.Fatal("ambiguous discovery silently reused another account's key")
	}
	if response := call(app.discoverCustomModels, http.MethodPost, map[string]string{"provider": "openai-chat", "credentialMode": "bearer", "baseUrl": second.BaseURL, "connectionId": second.ConnectionID}); response.Code != http.StatusOK {
		t.Fatalf("connection-specific discovery: %s", response.Body.String())
	}
	input := providerSettingsInput{Key: first.Key, Name: "First renamed", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: first.BaseURL, APIKey: "rotated-key"}
	if response := call(app.providerSettings, http.MethodPut, input); response.Code != http.StatusNoContent {
		t.Fatalf("edit one account: %s", response.Body.String())
	}
	app.refreshCustomModels(context.Background())
	for connectionID, modelKey := range shared {
		model, _, err := app.modelsSnapshot().Resolve(modelKey)
		if err != nil {
			t.Fatalf("model key changed after connection edit: %v", err)
		}
		wantKey := "second-key"
		if connectionID == first.ConnectionID {
			wantKey = "rotated-key"
		}
		turn, err := provider.OpenAIChatTurn(context.Background(), model, []provider.ChatMessage{{Role: "user", Content: "Hi"}}, nil)
		if err != nil || turn.Text != wantKey {
			t.Fatalf("wrong account used for reply: %v", err)
		}
	}
	settings := call(app.modelSettings, http.MethodGet, nil)
	if strings.Contains(settings.Body.String(), "rotated-key") || strings.Contains(settings.Body.String(), "second-key") {
		t.Fatal("settings returned an API key")
	}
	reloaded, err := modelcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	app.baseModels = reloaded
	app.customInventories = nil
	app.refreshCustomModels(context.Background())
	if app.modelsSnapshot().Default != shared[second.ConnectionID] || len(app.modelsSnapshot().Models) != 4 {
		t.Fatal("reload lost account inventories or default")
	}
	if response := call(app.providerSettings, http.MethodDelete, providerSettingsInput{Key: first.Key}); response.Code != http.StatusNoContent {
		t.Fatalf("disconnect first: %s", response.Body.String())
	}
	remaining := app.modelsSnapshot()
	if len(remaining.Models) != 2 || remaining.Default != shared[second.ConnectionID] {
		t.Fatal("disconnecting first affected second account or default")
	}
	for _, model := range remaining.Models {
		if model.ConnectionID != second.ConnectionID || model.Name != "Second" || model.APIKey != "second-key" {
			t.Fatal("another account was edited or disconnected")
		}
	}
}

func TestZenAccountsStayIndependent(t *testing.T) {
	stubZenModelList(t, "opencode-go", "zen-key", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: base, models: base}
	for _, name := range []string{"Work", "Personal"} {
		input := providerSettingsInput{Name: name, Provider: "catalog", ProviderID: "opencode-go", APIKey: "zen-key"}
		if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
			t.Fatalf("connect Zen account: %v", err)
		}
	}
	app.refreshCustomModels(context.Background())
	if models := app.modelsSnapshot().Models; len(models) != 4 || models[0].ConnectionID == models[1].ConnectionID {
		t.Fatal("Zen accounts or their models were merged")
	}
	first := app.baseModels.Models[0]
	if err := app.changeProviderSettings(context.Background(), http.MethodDelete, providerSettingsInput{Key: first.Key}); err != nil {
		t.Fatal(err)
	}
	if models := app.modelsSnapshot().Models; len(models) != 2 || models[0].Name != "Personal" || models[1].Name != "Personal" {
		t.Fatal("disconnecting one Zen account affected the other")
	}
}

func TestZenConnectionAutomaticallyIncludesSupportedModels(t *testing.T) {
	stubZenModelList(t, "opencode-go", "zen-key", "deepseek-v4-flash-vision-exp", "deepseek-v4-flash", "unknown-model")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: base, models: base}
	response := httptest.NewRecorder()
	app.providerSettings(response, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"provider":"catalog","providerId":"opencode-go","apiKey":"zen-key"}`)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("connect Zen without choosing IDs: %d %s", response.Code, response.Body.String())
	}
	app.refreshCustomModels(context.Background())
	models := app.modelsSnapshot().Models
	if len(models) != 2 {
		t.Fatalf("expected two routed models, got %d", len(models))
	}
	for _, model := range models {
		if model.Provider == "catalog" || model.ID == "unknown-model" || model.ProviderID != "opencode-go" {
			t.Fatal("inventory contains an unrouted model")
		}
	}
}
