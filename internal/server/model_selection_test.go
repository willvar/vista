package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"vista/internal/modelcatalog"

	"vista/internal/store"
)

func selectionApp(t *testing.T) *App {
	t.Helper()
	useExplicitReasoningCatalog(t)
	stateDir := t.TempDir()
	s, err := store.Open(filepath.Join(stateDir, "selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &App{dir: stateDir, store: s, models: modelCatalog{Default: "manual-luna", Models: []configuredModel{{Key: "manual-luna", ID: "gpt-6-luna", Provider: "openai-responses", ProviderID: "openai", BaseURL: "https://models.example/v1", Context: 128000}}}}
}

func TestModelSelectionPreferencesAPI(t *testing.T) {
	a := selectionApp(t)
	call := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.preferences(w, httptest.NewRequest(method, "/api/preferences", strings.NewReader(body)))
		return w
	}
	initial := call(http.MethodGet, "")
	if initial.Code != 200 {
		t.Fatal(initial.Body.String())
	}
	var prefs store.Preferences
	if err := json.Unmarshal(initial.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.UserID == "" {
		t.Fatalf("bad initial preferences: %+v", prefs)
	}
	if w := call(http.MethodPatch, `{"manualModelKey":"manual-luna","reasoningByModelId":{"gpt-6-luna":"low"}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(http.MethodPatch, `{"reasoningByModelId":{"other-id":"high"}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current := call(http.MethodGet, "")
	if err := json.Unmarshal(current.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.ManualModelKey != "manual-luna" || prefs.ReasoningByModelID["gpt-6-luna"] != "low" {
		t.Fatalf("preferences overwritten: %+v", prefs)
	}
	for _, body := range []string{`{"lastMode":"other"}`, `{"lastTier":"slow"}`, `{"unknown":true}`, `{"followUpBehavior":"now"}`, `{} {}`} {
		if w := call(http.MethodPatch, body); w.Code != 400 {
			t.Fatalf("invalid preference accepted: %s", body)
		}
	}
}

func TestModelSelectionSessionInitializationUsesUserMemory(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	key := "manual-luna"
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{ManualModelKey: &key, ReasoningByModelID: map[string]string{"gpt-6-luna": "low"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Create(ctx, "session", "new"); err != nil {
		t.Fatal(err)
	}
	session, err := a.initializeSessionSelection(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if session.ManualModelKey != key || session.ManualReasoningEffort != "low" {
		t.Fatalf("new session lost user memory: %+v", session)
	}
	w := httptest.NewRecorder()
	a.sessionSelection(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/session/selection", strings.NewReader(`{"manualReasoningEffort":"high"}`)), "session")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	prefs, _ := a.store.Preferences(ctx)
	if prefs.ManualModelKey != key || prefs.ReasoningByModelID["gpt-6-luna"] != "low" {
		t.Fatal("opening or changing a session implicitly overwrote user preferences")
	}
}

func TestModelSelectionRestoresDefaultModelAndValidEffort(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	oldKey := "removed-connection"
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{ManualModelKey: &oldKey, ReasoningByModelID: map[string]string{"gpt-6-luna": "removed-effort"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Create(ctx, "session", "new"); err != nil {
		t.Fatal(err)
	}
	firstEffort := modelcatalog.ReasoningOptionsFor("openai", "gpt-6-luna")[0]
	session, err := a.initializeSessionSelection(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if session.ManualModelKey != a.models.Default || session.ManualReasoningEffort != firstEffort {
		t.Fatalf("stale preferences did not fall back: %+v", session)
	}
	// Saved session state can outlive a change in supported effort options.
	invalid := "removed-effort"
	if _, err := a.store.PatchSessionSelection(ctx, "session", store.SessionSelectionPatch{ManualReasoningEffort: &invalid}); err != nil {
		t.Fatal(err)
	}
	model, key, err := a.resolveSelection(ctx, "session", selectionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if key != a.models.Default || model.ReasoningEffort != firstEffort {
		t.Fatalf("restored session effort did not fall back: key=%s effort=%s", key, model.ReasoningEffort)
	}
	if _, _, err := a.resolveSelection(ctx, "session", selectionInput{ReasoningEffort: invalid}); err == nil {
		t.Fatal("explicit invalid effort was silently accepted")
	}
}
