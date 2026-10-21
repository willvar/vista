package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLocalInventoryRefreshAndTransientFailure(t *testing.T) {
	var ollamaEmpty atomic.Bool
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			if ollamaEmpty.Load() {
				_, _ = w.Write([]byte(`{"models":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"models":[{"model":"ollama-test"}]}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
	}))
	defer ollama.Close()
	var studioFails atomic.Bool
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if studioFails.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"type":"llm","key":"studio-test"}]}`))
	}))
	defer studio.Close()
	var vllmFails atomic.Bool
	vllm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vllmFails.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"vllm-test","owned_by":"vllm","max_model_len":8192}]}`))
		}
	}))
	defer vllm.Close()
	t.Setenv("VISTA_OLLAMA_BASE_URL", ollama.URL+"/v1")
	t.Setenv("VISTA_LMSTUDIO_BASE_URL", studio.URL+"/v1")
	t.Setenv("VISTA_VLLM_BASE_URL", vllm.URL+"/v1")
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{models: base, baseModels: base, localModels: map[string][]configuredModel{}}
	app.refreshLocalModels(context.Background())
	first := app.modelsSnapshot()
	if len(first.Models) != 3 || first.Models[0].ProviderID != "ollama" || first.Models[1].ProviderID != "lmstudio" || first.Models[2].ProviderID != "vllm" {
		t.Fatalf("initial inventory: %+v", first)
	}
	ollamaEmpty.Store(true)
	studioFails.Store(true)
	vllmFails.Store(true)
	app.refreshLocalModels(context.Background())
	next := app.modelsSnapshot()
	if len(next.Models) != 2 || next.Models[0].ProviderID != "lmstudio" || next.Models[1].ProviderID != "vllm" || next.Default != next.Models[0].Key {
		t.Fatalf("removed Ollama and retained Studio/vLLM after outage: %+v", next)
	}
}
