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
	"vista/internal/store"
)

func TestProviderSaveReturnsAcceptedSettingsWithoutCredentials(t *testing.T) {
	app := &App{configDir: t.TempDir()}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	input := providerSettingsInput{Name: "Saved connection", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: "http://127.0.0.1:1/v1", APIKey: "private-save-key", Headers: map[string]string{"X-Account": "private-save-header"}, IDs: []string{"first-model"}}
	var previousRevision string
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		saved := httptest.NewRecorder()
		app.providerSettings(saved, httptest.NewRequest(method, "/api/provider-config", strings.NewReader(string(body))))
		var settings struct {
			Default          string         `json:"default"`
			Models           []modelSetting `json:"models"`
			SettingsRevision string         `json:"settingsRevision"`
		}
		if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &settings) != nil {
			t.Fatalf("%s did not return accepted settings: %d %s", method, saved.Code, saved.Body.String())
		}
		if len(settings.Models) != 1 || settings.Models[0].ID != input.IDs[0] || !settings.Models[0].HasAPIKey || settings.Default != settings.Models[0].Key || settings.SettingsRevision == "" || settings.SettingsRevision == previousRevision {
			t.Fatalf("%s returned stale or incomplete settings: %+v", method, settings)
		}
		if strings.Contains(saved.Body.String(), "private-save-key") || strings.Contains(saved.Body.String(), "private-save-header") || saved.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("save response leaked credentials or allowed caching")
		}
		current := httptest.NewRecorder()
		app.modelSettings(current, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
		if current.Code != http.StatusOK || current.Body.String() != saved.Body.String() {
			t.Fatalf("%s response differs from the authoritative settings", method)
		}
		input.Key, input.Revision, input.APIKey, input.IDs = settings.Models[0].Key, settings.SettingsRevision, "", []string{"second-model"}
		previousRevision = settings.SettingsRevision
	}
}

func TestCatalogConnectionUsesAllModelsWithoutSetupSelection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := modelCatalog{}
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
	if err != nil || loaded.Default != chosen.Key || len(loaded.Public()) != len(ids)*2 {
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
	base := modelCatalog{}
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
	base := modelCatalog{}
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
	if response := call(app.providerSettings, http.MethodPost, input); response.Code != http.StatusOK {
		t.Fatalf("connect without model IDs: %d %s", response.Code, response.Body.String())
	}
	app.refreshCustomModels(context.Background())
	catalog := app.modelsSnapshot()
	if len(catalog.Models) != 2 {
		t.Fatalf("expected automatic inventory, got %d models", len(catalog.Models))
	}
	anchor := app.baseModels.Models[0].Key
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
	if response := call(app.providerSettings, http.MethodPut, input); response.Code != http.StatusOK {
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
	if catalog := app.modelsSnapshot(); len(catalog.Models) != 0 || catalog.Default != "" {
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
	base := modelCatalog{}
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
		if response := call(app.providerSettings, http.MethodPost, input); response.Code != http.StatusOK {
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
	if response := call(app.providerSettings, http.MethodPut, input); response.Code != http.StatusOK {
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
	base := modelCatalog{}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: base, models: base}
	for _, name := range []string{"Work", "Personal"} {
		input := providerSettingsInput{Name: name, Provider: "catalog", ProviderID: "opencode-go", APIKey: "zen-key"}
		if err := app.changeProviderSettings(context.Background(), http.MethodPost, input); err != nil {
			t.Fatalf("connect Zen account: %v", err)
		}
	}
	app.refreshCustomModels(context.Background())
	counts := map[string]int{}
	for _, view := range app.modelsSnapshot().Public() {
		counts[view.ConnectionID]++
	}
	if len(counts) != 2 || counts[app.baseModels.Models[0].ConnectionID] != 2 || counts[app.baseModels.Models[1].ConnectionID] != 2 {
		t.Fatal("Zen accounts or their models were merged")
	}
	first := app.baseModels.Models[0]
	if err := app.changeProviderSettings(context.Background(), http.MethodDelete, providerSettingsInput{Key: first.Key}); err != nil {
		t.Fatal(err)
	}
	if models := app.modelsSnapshot().Models; len(models) != 2 || models[0].Name != "Personal" || models[1].Name != "Personal" {
		t.Fatalf("disconnecting one Zen account affected the other: %+v", app.modelsSnapshot().Public())
	}
}

func TestZenConnectionAutomaticallyIncludesSupportedModels(t *testing.T) {
	stubZenModelList(t, "opencode-go", "zen-key", "deepseek-v4-flash-vision-exp", "deepseek-v4-flash", "unknown-model")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{}
	app := &App{dir: filepath.Join(dir, "state"), configDir: dir, baseModels: base, models: base}
	response := httptest.NewRecorder()
	app.providerSettings(response, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"provider":"catalog","providerId":"opencode-go","apiKey":"zen-key"}`)))
	if response.Code != http.StatusOK {
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

func TestProviderAvailabilityPersistsAndIsScopedToConnection(t *testing.T) {
	a := selectionApp(t)
	a.configDir = t.TempDir()
	if err := os.Chmod(a.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	showAll := true
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{ShowUnrecognizedModels: &showAll}); err != nil {
		t.Fatal(err)
	}
	connection := configuredModel{Key: "primary", ID: "private-1", ConnectionID: "account-a", Name: "First", Provider: "openai-chat", ProviderID: "custom",
		BaseURL: "https://example.test/v1", Authentication: "bearer", APIKey: "private-test-key", Headers: map[string]string{"X-Test": "private-header-value"}}
	sibling := connection
	sibling.Key, sibling.ID = "sibling", "private-2"
	other := connection
	other.Key, other.ConnectionID, other.Name = "other", "account-b", "Second"
	raw := modelCatalog{Default: connection.Key, Models: []configuredModel{connection, sibling, other}}
	resolved, err := modelcatalog.Save(a.configDir, raw)
	if err != nil {
		t.Fatal(err)
	}
	a.baseModels = resolved
	source, ok := customSourceFor(resolved.Models[0])
	if !ok {
		t.Fatal("expected a custom discovery source")
	}
	a.customInventories = map[string][]modelcatalog.InventoryModel{source.identity(): {{ID: connection.ID}, {ID: sibling.ID}, {ID: "discovered"}}}
	ticketKey := "custom:" + source.identity()
	ticket := a.beginModelDiscoveryLocked(ticketKey)
	a.rebuildModelsLocked()
	before := a.updates.snapshot()
	revision := a.modelSettingsVersion(a.modelRevision)
	patch := func(disabled bool, revision string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"key": connection.Key, "disabled": disabled, "revision": revision})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		a.providerSettings(w, httptest.NewRequest(http.MethodPatch, "/api/provider-config", strings.NewReader(string(body))))
		return w
	}
	w := patch(true, revision)
	var settings struct {
		Models           []modelSetting `json:"models"`
		SettingsRevision string         `json:"settingsRevision"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &settings) != nil || len(settings.Models) != 3 || settings.SettingsRevision == revision {
		t.Fatalf("disable did not return accepted settings: %d %s", w.Code, w.Body)
	}
	for _, model := range settings.Models {
		if model.Disabled != (model.ConnectionID == connection.ConnectionID) || !model.HasAPIKey || !model.Editable || model.ConnectionKey == "" {
			t.Fatalf("unexpected saved row: %+v", model)
		}
	}
	if strings.Contains(w.Body.String(), "private-test-key") || strings.Contains(w.Body.String(), "private-header-value") {
		t.Fatal("patch returned credentials")
	}
	if stale := patch(false, revision); stale.Code != 409 {
		t.Fatalf("stale edit: %d %s", stale.Code, stale.Body)
	}
	if !a.currentModelDiscoveryLocked(ticketKey, ticket) {
		t.Fatal("visibility-only edit invalidated discovery")
	}
	point := a.updates.snapshot()
	if err := a.addStateSnapshots(ctx, "", &point, before); err != nil {
		t.Fatal(err)
	}
	if point.Revisions["models"] != before.Revisions["models"]+1 || point.Models == nil || point.Models.Default != "other" || point.Models.SettingsRevision != settings.SettingsRevision || len(point.Models.Models) != 4 {
		t.Fatalf("unexpected models publication: %+v", point.Models)
	}
	for _, model := range point.Models.Models {
		if model.Disabled != (model.ConnectionID == connection.ConnectionID) {
			t.Fatalf("unexpected model availability: %+v", model)
		}
	}
	reloaded, err := modelcatalog.Load(a.configDir)
	if err != nil || len(reloaded.Models) != 3 || !reloaded.Models[0].Disabled || !reloaded.Models[1].Disabled || reloaded.Models[2].Disabled || reloaded.Models[0].APIKey != connection.APIKey || reloaded.Models[0].Headers["X-Test"] != connection.Headers["X-Test"] {
		t.Fatal("disable did not persist independently of credentials")
	}
	if _, err := a.store.Create(ctx, "session", "availability"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "session", selectionInput{ModelKey: connection.Key}); err == nil {
		t.Fatal("disabled connection accepted for a new run")
	}
	if err := a.selectDefaultModel(ctx, sibling.Key); err == nil {
		t.Fatal("disabled connection accepted as a new default")
	}
	w = httptest.NewRecorder()
	a.sessionSelection(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/session/selection", strings.NewReader(`{"manualModelKey":"sibling"}`)), "session")
	if w.Code != 400 {
		t.Fatalf("disabled session choice: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	a.connectionStatistics(w, httptest.NewRequest(http.MethodGet, "/api/connection-statistics?key=primary&days=30", nil))
	if w.Code != 200 {
		t.Fatalf("disabled connection lost statistics: %d %s", w.Code, w.Body)
	}
	if enabled := patch(false, settings.SettingsRevision); enabled.Code != 200 {
		t.Fatalf("enable: %d %s", enabled.Code, enabled.Body)
	}
	for _, model := range a.modelsSnapshot().Models {
		if model.Disabled {
			t.Fatalf("model %s did not reappear", model.Key)
		}
	}
}
