package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/store"
)

func TestModelVisibilityPublishesCommittedPreferenceWithoutUnrelatedState(t *testing.T) {
	a := selectionApp(t)
	a.models.Models = append(a.models.Models, configuredModel{Key: "unknown", Provider: "openai-chat", ProviderID: "custom", ID: "private-unknown", APIKey: "secret"})
	ctx := context.Background()
	before := a.updates.snapshot()
	initial, err := a.publicModelsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.modelSettings(w, httptest.NewRequest("GET", "/api/model-config", nil))
	var settings struct {
		SettingsRevision string `json:"settingsRevision"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &settings) != nil || settings.SettingsRevision != initial.SettingsRevision {
		t.Fatalf("initial HTTP snapshots disagree on settings version: %s", w.Body.String())
	}
	for _, show := range []bool{true, false} {
		body, _ := json.Marshal(map[string]bool{"showUnrecognizedModels": show})
		w := httptest.NewRecorder()
		a.preferences(w, httptest.NewRequest("PATCH", "/api/preferences", strings.NewReader(string(body))))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		point, err := a.stateUpdate(ctx, "localhost", before)
		if err != nil {
			t.Fatal(err)
		}
		if point.Models == nil || point.Models.ShowUnrecognizedModels != show || len(point.Models.Models) != 2 || point.Sessions != nil || point.Status != nil || point.Catalog != nil {
			t.Fatalf("visibility did not push only committed models: %+v", point)
		}
		if !point.Models.Models[0].Recognized || point.Models.Models[1].Recognized {
			t.Fatalf("recognition mismatch: %+v", point.Models.Models)
		}
		if point.Models.SettingsRevision != initial.SettingsRevision {
			t.Fatal("visibility change invalidated connection settings")
		}
		data, _ := json.Marshal(point)
		if strings.Contains(string(data), "secret") {
			t.Fatal("credential in public snapshot")
		}
		before = point
	}
	a.modelRevision++
	changed, err := a.publicModelsSnapshot(ctx)
	if err != nil || changed.SettingsRevision == initial.SettingsRevision {
		t.Fatalf("connection edit reused settings version: %+v %v", changed, err)
	}
	restarted := &App{}
	if restarted.modelSettingsVersion(0) == initial.SettingsRevision {
		t.Fatal("server restart reused settings version")
	}
}

func TestHiddenModelsExcludeExistingManualDefaultAndTierChoices(t *testing.T) {
	a := selectionApp(t)
	known := a.models.Models[0]
	a.models = modelCatalog{Default: "unknown", Models: []configuredModel{{Key: "unknown", Provider: "openai-chat", ProviderID: "custom", ID: "private-unknown", BaseURL: "https://fixture.test/v1", Context: 32000}}}
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "fresh", "new"); err != nil {
		t.Fatal(err)
	}
	if session, err := a.initializeSessionSelection(ctx, "fresh"); err != nil || session.ManualModelKey != "" {
		t.Fatalf("hidden anchor became a default: %+v %v", session, err)
	}
	if _, _, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "auto", SelectionTier: "speed", InputTokens: 1}); err == nil {
		t.Fatal("hidden model entered automatic candidates")
	}
	w := httptest.NewRecorder()
	a.compactSession(w, httptest.NewRequest("POST", "/api/sessions/fresh/compact", strings.NewReader(`{"modelKey":"unknown"}`)), "fresh")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "no visible model") {
		t.Fatalf("hidden model entered manual compaction: %d %s", w.Code, w.Body.String())
	}
	show := true
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{ShowUnrecognizedModels: &show}); err != nil {
		t.Fatal(err)
	}
	if _, key, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "auto", SelectionTier: "speed", InputTokens: 1}); err != nil || key != "unknown" {
		t.Fatalf("revealed model is unusable: %q %v", key, err)
	}
	show = false
	key := "unknown"
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{ShowUnrecognizedModels: &show, ManualModelKey: &key}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "auto", SelectionTier: "speed", InputTokens: 1}); err == nil {
		t.Fatal("existing tier choice bypassed visibility")
	}
	if _, _, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "manual", ModelKey: "unknown"}); err == nil {
		t.Fatal("explicit earlier choice bypassed visibility")
	}
	if _, err := a.store.Create(ctx, "later", "new after hiding"); err != nil {
		t.Fatal(err)
	}
	if session, err := a.initializeSessionSelection(ctx, "later"); err != nil || session.ManualModelKey != "" {
		t.Fatalf("hidden earlier preference restored: %+v %v", session, err)
	}
	if snapshot, err := a.publicModelsSnapshot(ctx); err != nil || snapshot.Default != "" || len(snapshot.Models) != 1 {
		t.Fatalf("hidden default remained selectable or inventory was lost: %+v %v", snapshot, err)
	}

	a.models.Models = append(a.models.Models, known)
	if snapshot, err := a.publicModelsSnapshot(ctx); err != nil || snapshot.Default != known.Key || a.models.Default != "unknown" {
		t.Fatalf("visible default did not fall back independently of connection settings: %+v %v", snapshot, err)
	}
	if session, err := a.initializeSessionSelection(ctx, "fresh"); err != nil || session.ManualModelKey != known.Key {
		t.Fatalf("hidden preference did not fall back: %+v %v", session, err)
	}
	if model, selected, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "manual", ModelKey: "unknown", ReasoningEffort: "invalid-old-control"}); err != nil || selected != known.Key || model.ID != known.ID {
		t.Fatalf("hidden manual choice or its effort did not fall back: key=%s model=%s err=%v", selected, model.ID, err)
	}
	if _, _, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "manual", ModelKey: "removed-connection"}); err == nil {
		t.Fatal("missing explicit model silently switched connections")
	}
	for _, tier := range []string{"speed", "quality"} {
		if err := a.store.BindSessionTierSelection(ctx, "fresh", tier, "unknown", ""); err != nil {
			t.Fatal(err)
		}
		if _, selected, err := a.resolveSelection(ctx, "fresh", selectionInput{SelectionMode: "auto", SelectionTier: tier, InputTokens: 1}); err != nil || selected != known.Key {
			t.Fatalf("hidden %s binding did not fall back: key=%s err=%v", tier, selected, err)
		}
		binding, err := a.store.SessionTierSelection(ctx, "fresh", tier)
		if err != nil || binding == nil || binding.ModelKey != known.Key {
			t.Fatalf("hidden %s binding retained: %+v %v", tier, binding, err)
		}
	}
}
