package modelcatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDiscoverOllamaUsesShowCapabilitiesAndContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"model":"vision:latest"},{"model":"chat:latest"}]}`))
		case "/api/show":
			if r.Header.Get("Authorization") != "Bearer local-key" {
				t.Errorf("missing local authorization")
			}
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"model_info":{"llama.context_length":16384}}`))
		default:
			t.Errorf("unexpected discovery path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	models, err := DiscoverLocalModels(context.Background(), LocalSource{kind: "ollama", baseURL: server.URL + "/v1", apiKey: "local-key"})
	if err != nil || len(models) != 2 {
		t.Fatalf("Ollama discovery: %+v %v", models, err)
	}
	if models[0].ID != "chat:latest" || models[0].ProviderID != "ollama" || models[0].Context != 16384 || models[0].Tools == nil || !*models[0].Tools || !strings.HasPrefix(models[0].Key, "ollama-") || models[0].Info().DiscoverySource != "ollama" {
		t.Fatalf("Ollama model metadata: %+v", models[0])
	}
}

func TestDiscoverOllamaRejectsPartialInventory(t *testing.T) {
	var failed atomic.Bool
	failed.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"model":"ready"},{"model":"unavailable"}]}`))
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode Ollama show request: %v", err)
			}
			if failed.Load() && body.Model == "unavailable" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		default:
			t.Errorf("unexpected discovery path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	source := LocalSource{kind: "ollama", baseURL: server.URL + "/v1"}
	if models, err := DiscoverLocalModels(context.Background(), source); err == nil || models != nil {
		t.Fatalf("partial Ollama inventory accepted: %+v %v", models, err)
	}
	failed.Store(false)
	if models, err := DiscoverLocalModels(context.Background(), source); err != nil || len(models) != 2 {
		t.Fatalf("recovered Ollama inventory: %+v %v", models, err)
	}
}

func TestDiscoverLMStudioFiltersEmbeddingsAndMarksToolLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected discovery path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"type":"embedding","key":"embed"},{"type":"llm","key":"local-chat","max_context_length":32768,"loaded_instances":[{"config":{"context_length":8192}}],"capabilities":{"trained_for_tool_use":false}}]}`))
	}))
	defer server.Close()
	models, err := DiscoverLocalModels(context.Background(), LocalSource{kind: "lmstudio", baseURL: server.URL + "/v1"})
	if err != nil || len(models) != 1 {
		t.Fatalf("LM Studio discovery: %+v %v", models, err)
	}
	if models[0].ID != "local-chat" || models[0].Context != 8192 || models[0].Tools == nil || *models[0].Tools || models[0].APIKey != "-" {
		t.Fatalf("LM Studio model metadata: %+v", models[0])
	}
}

func TestDiscoverVLLMChecksHealthAndFiltersModels(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer local-key" {
			t.Errorf("missing vLLM authorization on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/prefix/health":
			w.WriteHeader(http.StatusOK)
		case "/prefix/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"z-chat","owned_by":"vllm","max_model_len":32768},{"id":"other","owned_by":"other","max_model_len":8192},{"id":"a-chat","owned_by":"vllm","max_model_len":null},{"id":"z-chat","owned_by":"vllm","max_model_len":32768}]}`))
		default:
			t.Errorf("unexpected vLLM path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	models, err := DiscoverLocalModels(context.Background(), LocalSource{kind: "vllm", baseURL: server.URL + "/prefix/v1", apiKey: "local-key"})
	if err != nil || len(models) != 2 {
		t.Fatalf("vLLM discovery: %+v %v", models, err)
	}
	if len(calls) != 2 || calls[0] != "/prefix/health" || calls[1] != "/prefix/v1/models" {
		t.Fatalf("vLLM discovery order: %v", calls)
	}
	if models[0].ID != "a-chat" || models[0].Context != 0 || models[1].ID != "z-chat" || models[1].Context != 32768 || models[1].Provider != "openai-chat" || models[1].ProviderID != "vllm" || models[1].Tools != nil || models[1].APIKey != "local-key" || models[1].BaseURL != server.URL+"/prefix/v1" {
		t.Fatalf("vLLM model metadata: %+v", models)
	}
}

func TestDiscoverVLLMRejectsUnhealthyAndOversizedInventory(t *testing.T) {
	var unhealthy atomic.Bool
	var modelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if unhealthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		modelCalls.Add(1)
		_, _ = w.Write([]byte(`{"data":[` + strings.Repeat(`{"id":"x","owned_by":"vllm"},`, 64) + `{"id":"x","owned_by":"vllm"}]}`))
	}))
	defer server.Close()
	source := LocalSource{kind: "vllm", baseURL: server.URL + "/v1"}
	unhealthy.Store(true)
	if _, err := DiscoverLocalModels(context.Background(), source); err == nil || modelCalls.Load() != 0 {
		t.Fatalf("unhealthy vLLM queried models: %v", err)
	}
	unhealthy.Store(false)
	if _, err := DiscoverLocalModels(context.Background(), source); err == nil || modelCalls.Load() != 1 {
		t.Fatalf("oversized vLLM inventory accepted: %v", err)
	}
}

func TestLocalDiscoveryRejectsExternalHTTP(t *testing.T) {
	_, _, err := localEndpoint(LocalSource{kind: "ollama", baseURL: "http://example.com/v1"})
	if err == nil {
		t.Fatal("external HTTP discovery endpoint accepted")
	}
}
