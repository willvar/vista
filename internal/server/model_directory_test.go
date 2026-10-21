package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/modelcatalog"
)

func directoryTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	demo := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	return &App{configDir: dir, models: demo, baseModels: demo}
}

func TestModelSettingsRemotePreparationAllowsReadsAndRejectsStaleCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			close(entered)
			<-release
			_, _ = w.Write([]byte(`{"data":[{"id":"slow"}]}`))
			return
		}
		w.WriteHeader(405)
	}))
	defer func() { once.Do(func() { close(release) }); upstream.Close() }()
	app := directoryTestApp(t)
	slowDone := make(chan error, 1)
	go func() {
		slowDone <- app.changeProviderSettings(context.Background(), http.MethodPost, providerSettingsInput{Name: "Slow", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: "test-key"})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	// A remote probe may not hold either snapshot/credential readers or a second edit.
	concurrent := make(chan error, 1)
	go func() {
		app.modelsSnapshot()
		app.modelCredentialEnvironment()
		concurrent <- app.changeProviderSettings(context.Background(), http.MethodPost, providerSettingsInput{Name: "Newer", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: "test-key", IDs: []string{"newer"}})
	}()
	select {
	case err := <-concurrent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		once.Do(func() { close(release) })
		t.Fatal("network preparation blocked directory reads or another edit")
	}
	once.Do(func() { close(release) })
	var detail *modelcatalog.SettingsError
	if err := <-slowDone; !errors.As(err, &detail) || detail.Code != "settings_conflict" {
		t.Fatalf("stale edit not rejected: %v", err)
	}
	raw, _, err := modelcatalog.Read(app.configDir)
	if err != nil || len(raw.Models) != 1 || raw.Models[0].ID != "newer" {
		t.Fatalf("lost concurrent edit: %+v %v", raw, err)
	}
	response := httptest.NewRecorder()
	settingsHTTPError(response, detail, 400)
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"settings_conflict"`) {
		t.Fatalf("conflict response: %d %s", response.Code, response.Body)
	}
}

func TestSettingsErrorsHaveStableRecoveryCodes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer upstream.Close()
	app := directoryTestApp(t)
	body, _ := json.Marshal(providerSettingsInput{Name: "Fixture", Provider: "openai-chat", ProviderID: "custom", CredentialMode: "bearer", BaseURL: upstream.URL + "/v1", APIKey: "sensitive-fixture"})
	response := httptest.NewRecorder()
	app.providerSettings(response, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(string(body))))
	var detail modelcatalog.SettingsError
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != 400 || detail.Code != "model_ids_required" || detail.Message == "" {
		t.Fatalf("recovery response: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "sensitive-fixture") {
		t.Fatal("key leaked in error")
	}
}

func TestModelDiscoveryRejectsOlderResponsesAndDisconnectedSources(t *testing.T) {
	for _, kind := range []string{"custom", "copilot"} {
		t.Run(kind, func(t *testing.T) {
			first, third := make(chan struct{}), make(chan struct{})
			releaseFirst, releaseThird := make(chan struct{}), make(chan struct{})
			var closeFirst, closeThird sync.Once
			defer closeFirst.Do(func() { close(releaseFirst) })
			defer closeThird.Do(func() { close(releaseThird) })
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := "newer"
				switch calls.Add(1) {
				case 1:
					close(first)
					<-releaseFirst
					id = "older"
				case 3:
					close(third)
					<-releaseThird
					id = "disconnected"
				}
				item := map[string]any{"id": id}
				if kind == "copilot" {
					item["model_picker_enabled"] = true
					item["supported_endpoints"] = []string{"/chat/completions"}
					item["capabilities"] = map[string]any{"supports": map[string]bool{"tool_calls": true}, "limits": map[string]int{"max_prompt_tokens": 8000, "max_output_tokens": 1000}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{item}})
			}))
			defer func() {
				closeFirst.Do(func() { close(releaseFirst) })
				closeThird.Do(func() { close(releaseThird) })
				upstream.Close()
			}()
			source := configuredModel{Key: "anchor", Provider: "openai-chat", ProviderID: "custom", Authentication: "bearer", ID: "anchor", BaseURL: upstream.URL + "/v1", APIKey: "test-key"}
			if kind == "copilot" {
				source.ProviderID, source.Authentication = "github-copilot", ""
			}
			base := modelCatalog{Default: "anchor", Models: []configuredModel{source}}
			app := &App{models: base, baseModels: base}
			refresh := app.refreshCustomModels
			if kind == "copilot" {
				refresh = app.refreshCopilotModels
			}
			done := make(chan struct{})
			go func() { refresh(context.Background()); close(done) }()
			select {
			case <-first:
			case <-time.After(time.Second):
				t.Fatal("discovery did not start")
			}
			refresh(context.Background())
			closeFirst.Do(func() { close(releaseFirst) })
			<-done
			hasNew := false
			for _, m := range app.modelsSnapshot().Models {
				if m.ID == "older" {
					t.Fatal("old response replaced newer inventory")
				}
				hasNew = hasNew || m.ID == "newer"
			}
			if !hasNew {
				t.Fatal("new inventory missing")
			}
			done = make(chan struct{})
			go func() { refresh(context.Background()); close(done) }()
			select {
			case <-third:
			case <-time.After(time.Second):
				t.Fatal("third discovery did not start")
			}
			app.modelMu.Lock()
			app.publishBaseModelsLocked(modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}})
			app.modelMu.Unlock()
			closeThird.Do(func() { close(releaseThird) })
			<-done
			if got := app.modelsSnapshot(); len(got.Models) != 1 || got.Default != "demo" {
				t.Fatalf("disconnected inventory returned: %+v", got)
			}
			if len(app.copilotInventories) != 0 || len(app.customInventories) != 0 {
				t.Fatal("inactive inventory retained")
			}
		})
	}
}
