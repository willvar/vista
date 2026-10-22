package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/provider"
	"vista/internal/store"
)

func TestModelSelectionStatisticsAPIHighestConfigurationAndReset(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	a.models.Models[0].Name = "First provider"
	a.models.Models = append(a.models.Models,
		configuredModel{Key: "duplicate", ID: "gpt-6-luna", Provider: "openai-responses", Name: "Second provider", BaseURL: "https://other.example/v1"},
		configuredModel{Key: "unknown", ID: "unknown-model", Provider: "openai-chat", Name: "Third provider"},
	)
	noTools := false
	a.models.Models = append(a.models.Models, configuredModel{Key: "no-tools", ID: "not-eligible", Provider: "openai-chat", Tools: &noTools})
	model, _, _ := a.models.Resolve("manual-luna")
	if err := highestSelectionReasoning(&model); err != nil {
		t.Fatal(err)
	}
	prefs, _ := a.store.Preferences(ctx)
	ms := 100.0
	if err := a.store.RecordModelObservation(ctx, store.ModelObservation{RequestID: "request", UserID: prefs.UserID, ModelID: model.ID, ReasoningEffort: provider.ModelSelectionEffort(model), Endpoint: provider.ModelEndpointID(model), FirstTextMS: &ms}); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.modelSelectionStats(w, httptest.NewRequest(method, "/api/model-selection/stats", strings.NewReader(body)))
		return w
	}
	var result struct {
		SpeedScope, QualityScope string
		Models                   []modelSelectionStatistics
	}
	w := call(http.MethodGet, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("bad GET: %d %s", w.Code, w.Body.String())
	}
	if result.SpeedScope != "global" || result.QualityScope != "user" || len(result.Models) != 2 || len(result.Models[0].Providers) != 2 || result.Models[0].ReasoningEffort != "max" || result.Models[0].Speed.Count != 1 || result.Models[1].Speed.MedianMS != nil {
		t.Fatalf("bad configured stats: %+v", result)
	}
	for _, body := range []string{`{}`, `{"scope":"unknown"}`, `{"scope":"speed","userId":"someone"}`, `{"scope":"speed"} {}`} {
		if w := call(http.MethodDelete, body); w.Code != 400 {
			t.Fatalf("invalid reset accepted: %d %s", w.Code, body)
		}
	}
	w = call(http.MethodDelete, `{"scope":"speed"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Models[0].Speed.Count != 0 {
		t.Fatalf("bad reset response: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "{}"); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("unsupported method accepted")
	}
}

func TestModelSelectionWithoutProviderHasNoCandidatesOrStatistics(t *testing.T) {
	a := selectionApp(t)
	a.models = modelCatalog{}
	w := httptest.NewRecorder()
	a.modelSelectionStats(w, httptest.NewRequest(http.MethodGet, "/api/model-selection/stats", nil))
	var result struct {
		Models []modelSelectionStatistics
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Models == nil || len(result.Models) != 0 {
		t.Fatalf("placeholder included in statistics: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "no providers"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: "speed"}); err == nil {
		t.Fatal("automatic selection accepted a placeholder")
	}
}
