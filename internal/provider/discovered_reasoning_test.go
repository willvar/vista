package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modelcfg "vista/internal/model"
)

func TestDiscoveredReasoningFalseSuppressesCatalogSummary(t *testing.T) {
	useExplicitReasoningCatalog(t)
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		http.Error(w, "fixture ends after encoding", http.StatusBadRequest)
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-responses", ProviderID: "custom", ID: "gpt-6-luna", BaseURL: upstream.URL, APIKey: "fixture", DiscoveredReasoning: &modelcfg.ReasoningCapabilities{Supported: false}}
	if err := ApplyReasoningEffort(&model, "high"); err == nil {
		t.Fatal("catalog reasoning survived explicit false")
	}
	_, _ = ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if body == nil {
		t.Fatal("no captured request")
	}
	if _, exists := body["reasoning"]; exists {
		t.Fatalf("catalog summary survived explicit false: %+v", body)
	}
}
