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
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/network"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestDiscoveredResponsesModelUsesSelectedProxyAndConnectionOptions(t *testing.T) {
	var discoveries, requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != "gateway.invalid" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("request did not preserve its upstream address and authentication through the proxy")
		}
		switch r.URL.Path {
		case "/v1/models":
			discoveries.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"existing"},{"id":"new"}]}`)
		case "/v1/responses":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["model"] != "new" || r.Header.Get("session-id") != "fixture-session" || r.Header.Get("x-client-request-id") == "" {
				t.Error("discovered model lost its model or routing identity")
			}
			if _, exists := body["prompt_cache_key"]; exists {
				t.Error("discovered model lost the explicit no-cache-hints setting")
			}
			wantState := ""
			if requests.Add(1) > 1 {
				wantState = "fixture-turn-state"
			}
			if r.Header.Get("x-codex-turn-state") != wantState {
				t.Error("discovered model lost the per-turn routing state")
			}
			w.Header().Set("x-codex-turn-state", "fixture-turn-state")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
		default:
			http.Error(w, "unexpected proxy request", http.StatusNotFound)
		}
	}))
	defer proxy.Close()
	address, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	profile := network.Profile{ID: "selected-proxy", Name: "Selected proxy", Protocol: "http", Server: address.Hostname(), Port: port, Authentication: "none"}
	ctx, cancel := context.WithTimeout(network.WithSettings(context.Background(), network.Settings{Profiles: []network.Profile{profile}}), 5*time.Second)
	defer cancel()
	ctx = provider.WithRequestState(ctx, new(provider.RequestState))
	anchor := configuredModel{Key: "anchor", ConnectionID: "connection", Provider: "openai-responses", ProviderID: "custom", ID: "existing", BaseURL: "http://gateway.invalid/v1", APIKey: "fixture-key", Authentication: "bearer", ProxyID: profile.ID, ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible, PromptCaching: &modelcfg.PromptCaching{Format: "none"}}
	base := modelCatalog{Default: anchor.Key, Models: []configuredModel{anchor}}
	app := &App{baseModels: base, models: base}
	app.refreshCustomModels(ctx)
	catalog := app.modelsSnapshot()
	if len(catalog.Models) != 2 {
		t.Fatalf("discovered inventory=%d, want 2", len(catalog.Models))
	}
	model, _, err := catalog.ResolveForRun(ctx, catalog.Models[1].Key)
	if err != nil {
		t.Fatal(err)
	}
	model.SessionID = "fixture-session"
	for range 2 {
		if _, err := provider.ModelTurn(ctx, model, []provider.ChatMessage{{Role: "user", Content: "fixture"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if discoveries.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("proxy received discovery=%d inference=%d, want 1 and 2", discoveries.Load(), requests.Load())
	}
}

func TestDiscoveredModelsPreserveConnectionSettings(t *testing.T) {
	for _, kind := range []string{"custom", "opencode", "opencode-go", "lmstudio"} {
		t.Run(kind, func(t *testing.T) {
			profile := network.Profile{ID: "selected-proxy", Name: "Selected proxy", Protocol: "http", Server: "proxy.invalid", Port: 8080, Authentication: "none"}
			ctx := network.WithSettings(context.Background(), network.Settings{Profiles: []network.Profile{profile}})
			calls := 0
			previous := http.DefaultTransport
			http.DefaultTransport = modelListTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				policy, err := network.FromContext(request.Context())
				if err != nil || policy.Profile == nil || policy.Profile.ID != profile.ID {
					t.Errorf("discovery lost selected proxy: policy=%+v error=%v", policy, err)
				}
				if request.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("discovery lost authentication")
				}
				body := `{"data":[{"id":"deepseek-v4-flash","context_length":8192},{"id":"deepseek-v4-pro","context_length":16384}]}`
				if kind == "lmstudio" {
					body = `{"models":[{"type":"llm","key":"deepseek-v4-flash","max_context_length":8192},{"type":"llm","key":"deepseek-v4-pro","max_context_length":16384}]}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			cache := &modelcfg.PromptCaching{Format: "none"}
			anchor := configuredModel{Key: "anchor", ConnectionID: "connection", Name: "Fixture", Provider: "openai-responses", ProviderID: kind, ID: "deepseek-v4-flash", BaseURL: "https://upstream.invalid/v1", APIKey: "fixture-key", Authentication: "bearer", ProxyID: profile.ID, PromptCaching: cache, Context: 4096}
			if kind == "custom" {
				anchor.ResponsesRoute = modelcfg.ResponsesRouteCodexCompatible
			} else if modelcatalog.ZenProvider(kind) {
				anchor.Provider, anchor.BaseURL, anchor.Authentication, anchor.Context = "catalog", "", "", 0
			} else {
				anchor.Provider = "openai-chat"
			}
			base := modelCatalog{Default: anchor.Key, Models: []configuredModel{anchor}}
			if err := modelcatalog.ResolveCatalog(&base); err != nil {
				t.Fatal(err)
			}
			app := &App{baseModels: base, models: base}
			app.refreshCustomModels(ctx)
			catalog := app.modelsSnapshot()
			if len(catalog.Models) != 2 || calls != 1 {
				t.Fatalf("models=%d requests=%d, want two models and one discovery request", len(catalog.Models), calls)
			}
			for _, configured := range catalog.Models {
				model, _, err := catalog.ResolveForRun(ctx, configured.Key)
				if err != nil {
					t.Fatal(err)
				}
				if model.ProxyID != profile.ID || model.Network == nil || model.Network.Profile == nil || model.Network.Profile.ID != profile.ID {
					t.Errorf("%s lost proxy before execution", model.ID)
				}
				if model.ResponsesRoute != anchor.ResponsesRoute || !reflect.DeepEqual(model.PromptCaching, cache) {
					t.Errorf("%s lost routing or cache settings: route=%q cache=%+v", model.ID, model.ResponsesRoute, model.PromptCaching)
				}
				if kind == "custom" && model.ID == "deepseek-v4-pro" && model.Context != 16384 {
					t.Errorf("new model inherited anchor context instead of its own capabilities: %d", model.Context)
				}
			}
		})
	}
}

func TestDiscoveryConnectionEditsReuseMetadataAndRejectOldResponses(t *testing.T) {
	for _, field := range []string{"proxy", "routing", "cache", "empty-headers"} {
		t.Run(field, func(t *testing.T) {
			profile := network.Profile{ID: "selected-proxy", Name: "Selected proxy", Protocol: "http", Server: "proxy.invalid", Port: 8080, Authentication: "none"}
			ctx := network.WithSettings(context.Background(), network.Settings{Profiles: []network.Profile{profile}})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var calls atomic.Int32
			previous := http.DefaultTransport
			http.DefaultTransport = modelListTransport(func(request *http.Request) (*http.Response, error) {
				switch calls.Add(1) {
				case 2:
					close(entered)
					<-release
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"late-old-result"}]}`))}, nil
				case 3:
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`unavailable`))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"existing"},{"id":"new"}]}`))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			anchor := configuredModel{Key: "anchor", ConnectionID: "connection", Provider: "openai-responses", ProviderID: "custom", ID: "existing", BaseURL: "https://upstream.invalid/v1", Authentication: "none", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible, PromptCaching: &modelcfg.PromptCaching{Format: "none"}}
			base := modelCatalog{Default: anchor.Key, Models: []configuredModel{anchor}}
			app := &App{baseModels: base, models: base}
			app.refreshCustomModels(ctx)
			before := app.modelsSnapshot()
			if len(before.Models) != 2 {
				t.Fatalf("initial inventory=%d, want 2", len(before.Models))
			}
			newKey := before.Models[1].Key
			done := make(chan struct{})
			go func() { app.refreshCustomModels(ctx); close(done) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("pending discovery did not start")
			}
			edited := anchor
			switch field {
			case "proxy":
				edited.ProxyID = profile.ID
			case "routing":
				edited.ResponsesRoute = ""
			case "cache":
				edited.PromptCaching = &modelcfg.PromptCaching{Format: "openai"}
			case "empty-headers":
				edited.Headers = map[string]string{}
			}
			app.modelMu.Lock()
			app.publishBaseModelsLocked(modelCatalog{Default: edited.Key, Models: []configuredModel{edited}})
			app.modelMu.Unlock()
			check := func() {
				t.Helper()
				catalog := app.modelsSnapshot()
				if len(catalog.Models) != 2 || len(app.customInventories) != 1 {
					t.Fatalf("connection preference edit discarded cached metadata: %+v", catalog)
				}
				for _, configured := range catalog.Models {
					model, _, err := catalog.ResolveForRun(ctx, configured.Key)
					if err != nil || model.ProxyID != edited.ProxyID || model.ResponsesRoute != edited.ResponsesRoute || !reflect.DeepEqual(model.PromptCaching, edited.PromptCaching) {
						t.Fatalf("current connection was replaced by cached settings: %+v %v", model, err)
					}
				}
				if catalog.Models[1].Key != newKey || catalog.Models[1].ID != "new" {
					t.Fatalf("late response or edit replaced the selected model: %+v", catalog.Models)
				}
			}
			check()
			once.Do(func() { close(release) })
			<-done
			check()
			app.refreshCustomModels(ctx)
			check()
		})
	}
}

func TestCustomMetadataRefreshUpdatesCapabilitiesWithoutChangingConnection(t *testing.T) {
	ctx := context.Background()
	var inventory atomic.Value
	inventory.Store(`{"data":[{"id":"gpt-6-luna","reasoning":false,"supports_vision":false,"supports_tools":false,"context_length":8192,"max_output_tokens":2048},{"id":"private-alias","name":"Private Vision","input_modalities":["text","image"],"supported_reasoning_levels":["low","high"]}]}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Access") != "saved" || r.Header.Get("Authorization") != "" {
			t.Error("discovery replaced saved authentication")
		}
		fmt.Fprint(w, inventory.Load().(string))
	}))
	defer upstream.Close()
	a := selectionApp(t)
	a.baseModels = modelCatalog{Default: "anchor", Models: []configuredModel{{Key: "anchor", ConnectionID: "custom", Name: "Test", Provider: "openai-chat", ProviderID: "custom", ID: "gpt-6-luna", BaseURL: upstream.URL, Authentication: "none", Context: 4096, Headers: map[string]string{"X-Access": "saved"}}}}
	a.refreshCustomModels(ctx)
	catalog := a.modelsSnapshot()
	known, _, err := catalog.Resolve("anchor")
	if err != nil || known.Context != 4096 || known.OutputLimit != 2048 || known.ImageInput == nil || *known.ImageInput || known.Tools == nil || *known.Tools || len(provider.SessionReasoningOptions(known)) != 0 {
		t.Fatalf("reference or explicit limits overrode discovery: %+v %v", known, err)
	}
	var aliasKey string
	for _, info := range catalog.Public() {
		if info.ID == "private-alias" {
			aliasKey = info.Key
			if !info.Recognized || info.ModelName != "Private Vision" || !slices.Equal(info.InputModalities, []string{"text", "image"}) || !slices.Equal(info.ReasoningOptions, []string{"low", "high"}) {
				t.Fatalf("derived metadata lost during rebuild: %+v", info)
			}
		}
	}
	if aliasKey == "" {
		t.Fatal("discovered alias missing")
	}
	inventory.Store(`{"data":[{"id":"gpt-6-luna"},{"id":"private-alias","reasoning":false,"supports_vision":false,"api":"anthropic","headers":{"Authorization":"wrong"}}]}`)
	a.refreshCustomModels(ctx)
	updated, key, err := a.modelsSnapshot().Resolve(aliasKey)
	if err != nil || key != aliasKey || updated.Provider != "openai-chat" || updated.Authentication != "none" || updated.Headers["X-Access"] != "saved" || updated.BaseURL != upstream.URL || updated.ImageInput == nil || *updated.ImageInput || len(provider.SessionReasoningOptions(updated)) != 0 {
		t.Fatalf("refresh left stale metadata or changed the connection: %+v %v", updated, err)
	}
	if !a.modelsSnapshot().Models[0].Recognized() || a.baseModels.Models[0].Tools != nil || a.baseModels.Models[0].Context != 4096 {
		t.Fatal("refresh mutated persisted restrictions or lost catalog recognition")
	}
}

func TestDiscoveryCacheDoesNotCrossServiceOrAccountEdits(t *testing.T) {
	for _, field := range []string{"address", "key", "headers", "authentication", "connection", "format"} {
		t.Run(field, func(t *testing.T) {
			previous := http.DefaultTransport
			http.DefaultTransport = modelListTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"anchor"},{"id":"account-only"}]}`))}, nil
			})
			defer func() { http.DefaultTransport = previous }()
			source := configuredModel{Key: "saved", ConnectionID: "account-a", ID: "anchor", Provider: "openai-chat", ProviderID: "custom", Authentication: "none", BaseURL: "https://a.invalid"}
			base := modelCatalog{Default: source.Key, Models: []configuredModel{source}}
			app := &App{baseModels: base, models: base}
			app.refreshCustomModels(context.Background())
			if len(app.modelsSnapshot().Models) != 2 {
				t.Fatal("initial discovery did not execute")
			}
			switch field {
			case "address":
				source.BaseURL = "https://b.invalid"
			case "key":
				source.APIKey = "account-b-key"
			case "headers":
				source.Headers = map[string]string{"Authorization": "account-b"}
			case "authentication":
				source.Authentication, source.APIKey = "bearer", "account-b-key"
			case "connection":
				source.ConnectionID = "account-b"
			case "format":
				source.Provider = "openai-responses"
			}
			app.publishBaseModelsLocked(modelCatalog{Default: source.Key, Models: []configuredModel{source}})
			if got := app.modelsSnapshot(); len(got.Models) != 1 || got.Models[0].ID != "anchor" || len(app.customInventories) != 0 {
				t.Fatalf("old service/account inventory survived a connection edit: %+v", got)
			}
		})
	}
}

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
	app := &App{models: base, baseModels: base, customInventories: map[string][]modelcatalog.InventoryModel{}}
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
			app := selectionApp(t)
			app.models, app.baseModels = base, base
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
			check(first.Key, second.Key, newKey)
			if _, _, err := app.modelsSnapshot().Resolve(first.Key); err == nil {
				t.Fatal("removed model is still selectable")
			}
			app = &App{store: app.store, models: base, baseModels: base}
			app.refreshCustomModels(context.Background())
			check(first.Key, second.Key, newKey)

			// A failed source keeps its last inventory while another successfully becomes empty.
			failed["first-key"] = true
			ids["second-key"] = nil
			app.refreshCustomModels(context.Background())
			check(first.Key, newKey)
			failed["first-key"] = false
			ids["first-key"] = nil
			app.refreshCustomModels(context.Background())
			check(first.Key)
			if _, _, err := app.modelsSnapshot().Resolve(""); err == nil {
				t.Fatal("empty inventory restored a runnable default")
			}
			failed["first-key"], failed["second-key"] = true, true
			app.refreshCustomModels(context.Background())
			check(first.Key)

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
			model, key, err := app.resolveSelection(ctx, "automatic", selectionInput{ModelKey: imageKey})
			if err != nil || key != imageKey || model.ID != "gpt-4.1-mini" {
				t.Fatalf("explicit image selection: key=%s model=%s error=%v", key, model.ID, err)
			}
		})
	}
}
