package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"vista/internal/modelcatalog"
	"vista/internal/workerproto"
)

func TestCustomModelsAppearWithoutSavingEachID(t *testing.T) {
	var mu sync.Mutex
	ids := []string{"existing", "new"}
	fail := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer private-key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		current, unavailable := append([]string(nil), ids...), fail
		mu.Unlock()
		if unavailable {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		data := make([]map[string]string, 0, len(current))
		for _, id := range current {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer upstream.Close()

	baseURL := upstream.URL + "/v1"
	base := modelCatalog{Default: "saved", Models: []configuredModel{{Key: "saved", Name: "Herdex", Provider: "openai-responses", ProviderID: "custom", Authentication: "bearer", ID: "existing", BaseURL: baseURL, APIKey: "private-key"}}}
	app := &App{models: base, baseModels: base, customInventories: map[string][]configuredModel{}}
	app.refreshCustomModels(context.Background())
	first := app.modelsSnapshot()
	if len(first.Models) != 2 || first.Models[0].Key != "saved" || first.Models[1].ID != "new" || first.Models[1].Name != "Herdex" || first.Models[1].APIKey != "private-key" {
		t.Fatalf("unexpected initial inventory: %d models", len(first.Models))
	}
	key := first.Models[1].Key
	if selected, _, err := first.Resolve(key); err != nil || selected.ID != "new" {
		t.Fatalf("discovered model cannot be selected: %+v, %v", selected, err)
	}
	mu.Lock()
	ids = []string{"existing", "new", "later"}
	mu.Unlock()
	app.refreshCustomModels(context.Background())
	second := app.modelsSnapshot()
	foundNew, foundLater := false, false
	for _, model := range second.Models {
		foundNew = foundNew || model.ID == "new" && model.Key == key
		foundLater = foundLater || model.ID == "later"
	}
	if len(second.Models) != 3 || !foundNew || !foundLater {
		t.Fatalf("new upstream model did not appear with a stable key: %d models, stable=%t, later=%t", len(second.Models), foundNew, foundLater)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	app.refreshCustomModels(context.Background())
	if len(app.modelsSnapshot().Models) != 3 {
		t.Fatal("temporary upstream failure removed the last good inventory")
	}
}

func TestCustomModelsWithSameIDKeepTheirProvider(t *testing.T) {
	endpoint := func(key string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, "wrong key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"shared"}]}`))
		}))
	}
	first := endpoint("first-key")
	defer first.Close()
	second := endpoint("second-key")
	defer second.Close()
	base := modelCatalog{Default: "first", Models: []configuredModel{
		{Key: "first", Name: "First", Provider: "openai-chat", ProviderID: "custom", Authentication: "bearer", ID: "saved", BaseURL: first.URL + "/v1", APIKey: "first-key"},
		{Key: "second", Name: "Second", Provider: "openai-chat", ProviderID: "custom", Authentication: "bearer", ID: "saved", BaseURL: second.URL + "/v1", APIKey: "second-key"},
	}}
	app := &App{models: base, baseModels: base}
	app.refreshCustomModels(context.Background())
	models := app.modelsSnapshot().Models
	if len(models) != 2 || models[0].ID != "shared" || models[1].ID != "shared" || models[0].Key == models[1].Key || models[0].BaseURL == models[1].BaseURL {
		t.Fatalf("providers were mixed: %d models", len(models))
	}
}

func TestConnectedModelInventoryReplacesSavedModelsAfterSuccessfulRefresh(t *testing.T) {
	for _, kind := range []string{"custom", "opencode", "opencode-go", "lmstudio"} {
		t.Run(kind, func(t *testing.T) {
			const savedID, newID = "deepseek-v4-flash", "deepseek-v4-pro"
			ids := map[string][]string{"first-key": {savedID, newID}, "second-key": {savedID}}
			failed := map[string]bool{"first-key": true, "second-key": true}
			previous := http.DefaultTransport
			http.DefaultTransport = modelListTransport(func(request *http.Request) (*http.Response, error) {
				key := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
				if _, ok := ids[key]; !ok {
					t.Error("unexpected discovery credential")
				}
				status := http.StatusOK
				if failed[key] {
					status = http.StatusServiceUnavailable
				}
				rows := make([]map[string]string, 0, len(ids[key]))
				for _, id := range ids[key] {
					rows = append(rows, map[string]string{"id": id, "type": "llm", "key": id})
				}
				field := "data"
				if kind == "lmstudio" {
					field = "models"
				}
				body, _ := json.Marshal(map[string]any{field: rows})
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })

			first := configuredModel{Key: "first-saved", ConnectionID: "first", Provider: "openai-chat", ProviderID: kind, Authentication: "bearer", ID: savedID, BaseURL: "http://127.0.0.1:1234/v1", APIKey: "first-key"}
			if modelcatalog.ZenProvider(kind) {
				first.Provider, first.BaseURL, first.Authentication = "catalog", "", ""
			}
			second := first
			second.Key, second.ConnectionID, second.APIKey = "second-saved", "second", "second-key"
			base := modelCatalog{Default: first.Key, Models: []configuredModel{first, second}}
			if err := modelcatalog.ResolveCatalog(&base); err != nil {
				t.Fatal(err)
			}
			app := &App{models: base, baseModels: base}
			check := func(wantDefault string, wantKeys ...string) {
				t.Helper()
				catalog := app.modelsSnapshot()
				keys := make([]string, 0, len(catalog.Models))
				for _, model := range catalog.Models {
					keys = append(keys, model.Key)
				}
				slices.Sort(keys)
				slices.Sort(wantKeys)
				if !slices.Equal(keys, wantKeys) || catalog.Default != wantDefault {
					t.Fatalf("inventory keys=%v default=%q, want keys=%v default=%q", keys, catalog.Default, wantKeys, wantDefault)
				}
				response := httptest.NewRecorder()
				app.modelList(response, httptest.NewRequest(http.MethodGet, "/api/models", nil))
				var public struct {
					Models []modelcatalog.Info `json:"models"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil || len(public.Models) != len(wantKeys) {
					t.Fatalf("public inventory count=%d, want %d; error=%v", len(public.Models), len(wantKeys), err)
				}
			}

			// Before any successful discovery, saved models remain usable.
			app.refreshCustomModels(context.Background())
			check(first.Key, first.Key, second.Key)
			failed["first-key"], failed["second-key"] = false, false
			app.refreshCustomModels(context.Background())
			var newKey string
			for _, model := range app.modelsSnapshot().Models {
				if model.ConnectionID == first.ConnectionID && model.ID == newID {
					newKey = model.Key
				}
			}
			if newKey == "" {
				t.Fatal("newly discovered model is missing")
			}
			check(first.Key, first.Key, second.Key, newKey)

			// Removing a saved model affects only that connection, including after restart.
			ids["first-key"] = []string{newID}
			app.refreshCustomModels(context.Background())
			check(second.Key, second.Key, newKey)
			if _, _, err := app.modelsSnapshot().Resolve(first.Key); err == nil {
				t.Fatal("removed model is still selectable")
			}
			app = &App{models: base, baseModels: base}
			app.refreshCustomModels(context.Background())
			check(second.Key, second.Key, newKey)

			// A failed source keeps its last inventory while another successfully becomes empty.
			failed["first-key"] = true
			ids["second-key"] = nil
			app.refreshCustomModels(context.Background())
			check(newKey, newKey)
			failed["first-key"] = false
			ids["first-key"] = nil
			app.refreshCustomModels(context.Background())
			check("")
			if _, _, err := app.modelsSnapshot().Resolve(""); err == nil {
				t.Fatal("empty inventory restored a runnable default")
			}
			failed["first-key"], failed["second-key"] = true, true
			app.refreshCustomModels(context.Background())
			check("")

			// An unchanged saved key can be selected again if its model returns.
			failed["first-key"] = false
			ids["first-key"] = []string{savedID}
			app.refreshCustomModels(context.Background())
			check(first.Key, first.Key)
		})
	}
}

func TestCustomModelDiscoveryImageCapabilitiesAndSubmission(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"},{"id":"gpt-3.5-turbo"},{"id":"unlisted-custom-model"}]}`))
	}))
	defer upstream.Close()

	for _, protocol := range []string{"openai-chat", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			app := selectionApp(t)
			app.baseModels = modelCatalog{Default: "saved-text", Models: []configuredModel{{
				Key: "saved-text", Name: "Custom", Provider: protocol, ProviderID: "custom", Authentication: "bearer", ID: "gpt-3.5-turbo",
				BaseURL: upstream.URL + "/v1", APIKey: "test-key",
			}}}
			app.models = app.baseModels
			app.workers = map[string]*worker{}
			app.refreshCustomModels(ctx)

			response := httptest.NewRecorder()
			app.modelList(response, httptest.NewRequest(http.MethodGet, "/api/models", nil))
			var catalog struct {
				Models []modelcatalog.Info `json:"models"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil || response.Code != http.StatusOK || len(catalog.Models) != 3 {
				t.Fatalf("discovered public catalog: status=%d body=%s error=%v", response.Code, response.Body.String(), err)
			}

			imageData := testPNG(t)
			var imageKey string
			for _, model := range catalog.Models {
				wantImages := model.ID == "gpt-4.1-mini"
				if got := slices.Contains(model.InputModalities, "image"); got != wantImages {
					t.Fatalf("%s public image capability=%t, want %t", model.ID, got, wantImages)
				}
				if wantImages {
					imageKey = model.Key
				}
				if _, err := app.store.Create(ctx, model.Key, "image capability"); err != nil {
					t.Fatal(err)
				}
				sink := &imageCommandSink{}
				app.workers[model.Key] = &worker{input: sink}
				body, _ := json.Marshal(map[string]any{
					"content": "describe this", "modelKey": model.Key,
					"images": []any{seedImageRef(t, app, model.Key, imageData)},
				})
				result := httptest.NewRecorder()
				app.submitMessage(result, httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(body)), model.Key)
				if !wantImages {
					if result.Code != http.StatusBadRequest || sink.Len() != 0 {
						t.Fatalf("%s accepted unsupported image: status=%d workerBytes=%d", model.ID, result.Code, sink.Len())
					}
					continue
				}
				if result.Code != http.StatusAccepted {
					t.Fatalf("discovered image model rejected image: status=%d body=%s", result.Code, result.Body.String())
				}
				var command workerproto.Command
				if err := json.NewDecoder(&sink.Buffer).Decode(&command); err != nil || len(command.Images) != 1 || command.Images[0].Hash != fmt.Sprintf("%x", sha256.Sum256(imageData)) {
					t.Fatalf("image did not reach worker intact: command=%+v error=%v", command, err)
				}
			}

			if _, err := app.store.Create(ctx, "automatic", "image selection"); err != nil {
				t.Fatal(err)
			}
			model, key, err := app.resolveSelection(ctx, "automatic", selectionInput{SelectionMode: "auto", InputTokens: 1, HasImages: true})
			if err != nil || key != imageKey || model.ID != "gpt-4.1-mini" {
				t.Fatalf("automatic image selection: key=%s model=%s error=%v", key, model.ID, err)
			}
		})
	}
}
