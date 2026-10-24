package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"vista/internal/modelcatalog"
	"vista/internal/provider"
)

type catalogTestTransport func(*http.Request) (*http.Response, error)

func (f catalogTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const updatedCatalogSource = `{"openai":{
 "gpt-6-luna":{"id":"gpt-6-luna","provider":"openai","name":"Updated Luna","api":"openai-responses","input":["text","image"],"contextWindow":2000000,"maxTokens":128000,"reasoning":true,"thinking":{"mode":"effort","efforts":["low","max"]}},
 "vista-new-model":{"id":"vista-new-model","provider":"openai","name":"New Model","api":"openai-responses","input":["text"],"contextWindow":100000,"maxTokens":8192}
}}`

const updatedModelsDevSource = `{"openai":{"id":"openai","models":{
 "vista-models-dev-model":{"id":"vista-models-dev-model","name":"models.dev model","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high","max"]}],"tool_call":true,"limit":{"context":200000,"output":32000},"modalities":{"input":["text","image"],"output":["text"]}},
	"gpt-6-luna":{"id":"gpt-6-luna","name":"Updated Luna","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","max"]}],"tool_call":true,"limit":{"context":2000000,"output":128000},"modalities":{"input":["text","image"],"output":["text"]}}
}}}`

func catalogTestClient(source string) *http.Client {
	return catalogTestClientWithModelsDev(source, updatedModelsDevSource)
}

func catalogTestClientWithModelsDev(source, modelsDev string) *http.Client {
	return &http.Client{Transport: catalogTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			return nil, fmt.Errorf("catalog fetch included a credential")
		}
		if r.URL.Host == "models.dev" || r.URL.Host == "models.opencode.ai" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(modelsDev)), Header: make(http.Header)}, nil
		}
		if r.URL.Host == "catalog.stencil.so" {
			encoder, err := zstd.NewWriter(nil)
			if err != nil {
				return nil, err
			}
			defer encoder.Close()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(encoder.EncodeAll([]byte(modelsDev), nil))), Header: make(http.Header)}, nil
		}
		data := source
		if r.URL.Host == "api.github.com" {
			hash := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(source), source)))
			data = fmt.Sprintf(`{"sha":"%x"}`, hash)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data)), Header: make(http.Header)}, nil
	})}
}

func catalogUpdateTestApp(t *testing.T) *App {
	t.Helper()
	app := directoryTestApp(t)
	raw := []byte(`{"default":"saved","models":[{"key":"saved","connectionId":"openai-connection","name":"My OpenAI","provider":"catalog","providerId":"openai","id":"gpt-6-luna","apiKey":"private-model-credential"}]}`)
	if err := os.WriteFile(filepath.Join(app.configDir, "models.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := modelcatalog.Load(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	app.baseModels, app.models = resolved, resolved
	app.ompCatalogClient = catalogTestClient(updatedCatalogSource)
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	return app
}

func TestOMPCatalogAPIUpdatesLiveModelsAndPersistsWithoutChangingConnections(t *testing.T) {
	app := catalogUpdateTestApp(t)
	before, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	response := httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	var result struct {
		provider.OMPCatalogInfo
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || !result.Changed || result.ModelCount != 3 || result.UpdatedAt == "" || result.ModelsDevRevision == "" {
		t.Fatalf("update result: %d %s (%v)", response.Code, response.Body, err)
	}
	models := app.modelsSnapshot().Public()
	if len(models) != 3 {
		t.Fatalf("new built-in model missing after update: %+v", models)
	}
	for _, model := range models {
		if model.Key == "saved" && (model.ModelName != "Updated Luna" || model.Context != 2000000 || strings.Join(model.ReasoningOptions, ",") != "low,max") {
			t.Fatalf("existing connection retained stale metadata: %+v", model)
		}
	}
	after, _ := os.ReadFile(filepath.Join(app.configDir, "models.json"))
	if string(before) != string(after) {
		t.Fatal("catalog update rewrote model connections")
	}
	cache, err := os.ReadFile(provider.OMPCatalogPath(app.configDir))
	if err != nil || strings.Contains(string(cache), "private-model-credential") {
		t.Fatalf("invalid persisted metadata: %v", err)
	}
	response = httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodGet, "/api/model-catalog", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), result.Revision) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog status: %d %s", response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"changed":false`) {
		t.Fatalf("unchanged update: %d %s", response.Code, response.Body)
	}
	// models.dev can change independently of OMP. Its version must participate in
	// activation, persistence and refreshing an existing connection's inventory.
	app.ompCatalogClient = catalogTestClientWithModelsDev(updatedCatalogSource, strings.Replace(updatedModelsDevSource, `"context":200000`, `"context":300000`, 1))
	response = httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"changed":true`) || provider.CurrentOMPCatalog().Info().Revision != result.Revision || provider.CurrentOMPCatalog().Info().ModelsDevRevision == result.ModelsDevRevision {
		t.Fatalf("models.dev-only update: %d %s", response.Code, response.Body)
	}
	for _, model := range app.modelsSnapshot().Public() {
		if model.ID == "vista-models-dev-model" && model.Context != 300000 {
			t.Fatalf("models.dev-only update left stale model metadata: %+v", model)
		}
	}
}

func TestOMPCatalogFailurePreservesActiveDirectoryAndCache(t *testing.T) {
	for _, failure := range []string{"download", "models-dev-download", "models-dev-compression", "models-dev-metadata", "invalid-metadata", "missing-configured-model", "save"} {
		t.Run(failure, func(t *testing.T) {
			app := catalogUpdateTestApp(t)
			original := provider.CurrentOMPCatalog()
			if err := original.Save(app.configDir); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(provider.OMPCatalogPath(app.configDir))
			switch failure {
			case "download":
				app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("offline"))}, nil
				})}
			case "invalid-metadata":
				app.ompCatalogClient = catalogTestClient(`{"openai":`)
			case "models-dev-metadata":
				app.ompCatalogClient = catalogTestClientWithModelsDev(updatedCatalogSource, `{}`)
			case "models-dev-download", "models-dev-compression":
				good := app.ompCatalogClient.Transport
				app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "catalog.stencil.so" && r.URL.Host != "models.dev" && r.URL.Host != "models.opencode.ai" {
						return good.RoundTrip(r)
					}
					status := http.StatusOK
					if failure == "models-dev-download" {
						status = http.StatusServiceUnavailable
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("bad response"))}, nil
				})}
			case "missing-configured-model":
				app.ompCatalogClient = catalogTestClientWithModelsDev(strings.ReplaceAll(updatedCatalogSource, "gpt-6-luna", "removed-model"), strings.ReplaceAll(updatedModelsDevSource, "gpt-6-luna", "removed-model"))
			case "save":
				if err := os.Remove(provider.OMPCatalogPath(app.configDir)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(provider.OMPCatalogPath(app.configDir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			response := httptest.NewRecorder()
			app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
			if response.Code < 400 || provider.CurrentOMPCatalog() != original || app.modelRevision != 0 {
				t.Fatalf("failed update was published: %d %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "private-model-credential") {
				t.Fatal("update error exposed a credential")
			}
			if failure != "save" {
				after, _ := os.ReadFile(provider.OMPCatalogPath(app.configDir))
				if string(after) != string(before) {
					t.Fatal("failed update replaced the saved catalog")
				}
			}
		})
	}
}

func TestOMPCatalogConcurrentUpdateAndCancellation(t *testing.T) {
	app := catalogUpdateTestApp(t)
	entered := make(chan struct{})
	var enter sync.Once
	app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(r *http.Request) (*http.Response, error) {
		enter.Do(func() { close(entered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ompCatalog(first, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil).WithContext(ctx))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("update did not start")
	}
	second := httptest.NewRecorder()
	app.ompCatalog(second, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if second.Code != 409 {
		t.Fatalf("overlapping update accepted: %d", second.Code)
	}
	status := httptest.NewRecorder()
	app.ompCatalog(status, httptest.NewRequest(http.MethodGet, "/api/model-catalog", nil))
	if status.Code != 200 || len(app.modelsSnapshot().Models) == 0 {
		t.Fatal("download blocked catalog reads")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("update ignored cancellation")
	}
	if app.modelRevision != 0 {
		t.Fatal("cancelled download was installed")
	}
}

func TestModelsDevZenDiscoverySelectionAndRestart(t *testing.T) {
	const freeID = "deepseek-v4.1-flash-free"
	const anchor = "deepseek-v4-flash"
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	transport := http.DefaultTransport
	http.DefaultTransport = catalogTestTransport(func(r *http.Request) (*http.Response, error) {
		ids := []map[string]string{{"id": anchor}}
		if !strings.Contains(r.URL.Path, "/go/") {
			ids = append(ids, map[string]string{"id": freeID})
		}
		data, _ := json.Marshal(map[string]any{"data": ids})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	app := directoryTestApp(t)
	raw := modelCatalog{Default: "zen", Models: []configuredModel{
		{Key: "zen", ConnectionID: "zen", Provider: "catalog", ProviderID: "opencode", ID: anchor, APIKey: "fixture-key"},
		{Key: "go", ConnectionID: "go", Provider: "catalog", ProviderID: "opencode-go", ID: anchor, APIKey: "fixture-key"},
	}}
	write := func(catalog modelCatalog) {
		t.Helper()
		data, _ := json.Marshal(catalog)
		if err := os.WriteFile(filepath.Join(app.configDir, "models.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(raw)
	base, err := modelcatalog.Load(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	app.models, app.baseModels = base, base
	zenSource := fmt.Sprintf(`{"opencode-zen":{%q:{"id":%q,"provider":"opencode-zen","name":"Anchor","api":"openai-completions","input":["text"],"contextWindow":1000000,"maxTokens":128000}},"opencode-go":{%q:{"id":%q,"provider":"opencode-go","name":"Anchor","api":"openai-completions","input":["text"],"contextWindow":1000000,"maxTokens":128000}}}`, anchor, anchor, anchor, anchor)
	modelsDevSource := strings.ReplaceAll(strings.ReplaceAll(updatedModelsDevSource, `"openai"`, `"opencode"`), "vista-models-dev-model", freeID)
	app.ompCatalogClient = catalogTestClientWithModelsDev(zenSource, modelsDevSource)
	response := httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Zen catalog update: %d %s", response.Code, response.Body)
	}
	var selected string
	for _, model := range app.modelsSnapshot().Public() {
		if model.ID != freeID {
			continue
		}
		if model.ProviderID != "opencode" || model.Context != 200000 || model.Provider != "openai-chat" || strings.Join(model.ReasoningOptions, ",") != "low,high,max" {
			t.Fatalf("Free model acquired the wrong account, route or metadata: %+v", model)
		}
		selected = model.Key
	}
	if selected == "" {
		t.Fatal("models.dev model was not discovered without reconnecting Zen")
	}
	saved, err := modelcatalog.SelectDefault(raw, app.modelsSnapshot(), selected)
	if err != nil {
		t.Fatal(err)
	}
	write(saved)
	original.Activate()
	if err := provider.LoadOMPCatalog(app.configDir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := modelcatalog.Load(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := reloaded.Resolve("")
	if err != nil || request.ID != freeID || request.Provider != "openai-chat" || request.OutputLimit != 32000 {
		t.Fatalf("saved models.dev-only selection cannot run after restart: id=%s provider=%s output=%d error=%v", request.ID, request.Provider, request.OutputLimit, err)
	}
	_, err = modelcatalog.ChangeConnection(context.Background(), modelCatalog{}, false, http.MethodPost,
		modelcatalog.ConnectionInput{Provider: "catalog", ProviderID: "opencode", APIKey: "new-fixture-key", IDs: []string{freeID}})
	if err != nil {
		t.Fatalf("a new Zen connection cannot select a models.dev-only model: %v", err)
	}
}
