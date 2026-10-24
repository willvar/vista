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

	"vista/internal/provider"
	"vista/internal/store"
)

func selectionApp(t *testing.T) *App {
	t.Helper()
	useExplicitReasoningCatalog(t)
	s, err := store.Open(filepath.Join(t.TempDir(), "selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &App{store: s, models: modelCatalog{Default: "manual-luna", Models: []configuredModel{{Key: "manual-luna", ID: "gpt-6-luna", Provider: "openai-responses", ProviderID: "openai", BaseURL: "https://models.example/v1", Context: 128000}}}}
}

func TestModelSelectionPreferencesAPIAndInvitationPersistence(t *testing.T) {
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
	if prefs.UserID == "" || prefs.LastMode != "manual" || prefs.FeedbackInvitationSeen {
		t.Fatalf("bad initial preferences: %+v", prefs)
	}
	if w := call(http.MethodPatch, `{"lastMode":"auto","lastTier":"quality","manualModelKey":"manual-luna","reasoningByModelId":{"gpt-6-luna":"low"},"feedbackInvitationSeen":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(http.MethodPatch, `{"lastTier":"speed","reasoningByModelId":{"other-id":"high"},"feedbackInvitationSeen":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current := call(http.MethodGet, "")
	if err := json.Unmarshal(current.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.LastMode != "auto" || prefs.ManualModelKey != "manual-luna" || prefs.ReasoningByModelID["gpt-6-luna"] != "low" || !prefs.FeedbackInvitationSeen {
		t.Fatalf("preferences overwritten: %+v", prefs)
	}
	for _, body := range []string{`{"lastMode":"other"}`, `{"lastTier":"slow"}`, `{"unknown":true}`, `{"followUpBehavior":"now"}`, `{} {}`} {
		if w := call(http.MethodPatch, body); w.Code != 400 {
			t.Fatalf("invalid preference accepted: %s", body)
		}
	}
}

func TestModelSelectionAutomaticHighestStickyAndManualMemory(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "modes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "manual", ModelKey: "manual-luna", ReasoningEffort: "low", InputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	model, key, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: "speed", InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if key != "manual-luna" || model.ReasoningEffort != "max" {
		t.Fatalf("automatic highest reasoning absent: %+v", model)
	}
	session, _ := a.store.Get(ctx, "session")
	if session.ManualModelKey != "manual-luna" || session.ManualReasoningEffort != "low" {
		t.Fatalf("automatic selection overwrote manual memory: %+v", session)
	}
	a.models.Models = append(a.models.Models, configuredModel{Key: "new-astra", ID: "gpt-6-astra", Provider: "openai-responses", BaseURL: "https://models.example/v1", Context: 128000})
	_, again, err := a.resolveSelection(ctx, "session", selectionInput{InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if again != key {
		t.Fatal("session changed its selected model without a mode/tier change")
	}
	// An outdated effort binding must be replaced by today's maximum.
	if err := a.store.BindSessionSelection(ctx, "session", key, "low"); err != nil {
		t.Fatal(err)
	}
	model, _, err = a.resolveSelection(ctx, "session", selectionInput{InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if model.ReasoningEffort != "max" {
		t.Fatal("outdated effort binding reused")
	}
	model, key, err = a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "manual", InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if key != "manual-luna" || model.ReasoningEffort != "low" {
		t.Fatalf("manual memory not restored: model=%+v key=%s", model, key)
	}
}

func TestModelSelectionQualityRetainsModelAcrossConsecutiveRequests(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "quality continuity"); err != nil {
		t.Fatal(err)
	}
	first, qualityKey, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: "quality", InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.FinishModelAttempt(ctx, prefs.UserID, first.ID, provider.ModelSelectionEffort(first), "succeeded"); err != nil {
		t.Fatal(err)
	}
	a.models.Models = append(a.models.Models, configuredModel{Key: "new-astra", ID: "gpt-6-astra", Provider: "openai-responses", BaseURL: "https://models.example/v1", Context: 128000})
	for _, input := range []selectionInput{
		{InputTokens: 2877},
		{SelectionMode: "auto", SelectionTier: "quality", InputTokens: 5000},
	} {
		model, key, err := a.resolveSelection(ctx, "session", input)
		if err != nil {
			t.Fatal(err)
		}
		if key != qualityKey || model.ID != first.ID || model.ReasoningEffort != first.ReasoningEffort {
			t.Fatalf("quality selection changed across consecutive requests: model=%s key=%s effort=%s", model.ID, key, model.ReasoningEffort)
		}
	}
}

func TestModelSelectionOnlyEligibleConnectedModels(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "eligibility"); err != nil {
		t.Fatal(err)
	}
	noTools := false
	a.models.Models = []configuredModel{
		{Key: "no-tools", ID: "no-tools", Provider: "openai-responses", Tools: &noTools, Context: 128000, InputModalities: []string{"text", "image"}},
		{Key: "text-only", ID: "text-only", Provider: "openai-responses", Context: 128000, InputModalities: []string{"text"}},
		{Key: "eligible", ID: "eligible", Provider: "openai-responses", Context: 3000, InputModalities: []string{"text", "image"}},
	}
	model, key, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", InputTokens: 4000, HasImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if key != "eligible" || model.ID != "eligible" {
		t.Fatalf("ineligible model selected: %+v", model)
	}
	a.finishUnobservedSelection(ctx, model, "admission_rejected")
	// The cleanup must release the reservation immediately, without inventing
	// a latency sample or blocking the only usable model for 30 minutes.
	prefs, _ := a.store.Preferences(ctx)
	speed, _ := a.store.ModelSpeed(ctx, model.ID, provider.ModelSelectionEffort(model), provider.ModelEndpointID(model), 4000)
	if speed.Known {
		t.Fatal("cleanup fabricated a latency sample")
	}
	decision, err := a.store.SelectModel(ctx, prefs.UserID, "speed", []store.SelectionCandidate{{ModelID: model.ID, ModelKey: key, ReasoningEffort: provider.ModelSelectionEffort(model)}}, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reason == "first_use" {
		t.Fatal("cleanup made the attempted model unknown again")
	}
}

func TestModelSelectionSessionInitializationUsesUserMemory(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	mode, tier, key := "manual", "quality", "manual-luna"
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{LastMode: &mode, LastTier: &tier, ManualModelKey: &key, ReasoningByModelID: map[string]string{"gpt-6-luna": "low"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Create(ctx, "session", "new"); err != nil {
		t.Fatal(err)
	}
	session, err := a.initializeSessionSelection(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if session.SelectionTier != "quality" || session.ManualModelKey != key || session.ManualReasoningEffort != "low" {
		t.Fatalf("new session lost user memory: %+v", session)
	}
	w := httptest.NewRecorder()
	a.sessionSelection(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/session/selection", strings.NewReader(`{"selectionMode":"auto","selectionTier":"speed"}`)), "session")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	prefs, _ := a.store.Preferences(ctx)
	if prefs.LastMode != "manual" || prefs.LastTier != "quality" {
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

func TestModelSelectionFeedbackAPIRequiresActualAssistantReply(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "feedback"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.store.BeginRun(ctx, "session", "run", "user-message", "Hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.RecordModelRequest(ctx, "session", "run", "openai-responses", "gpt-6-luna"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RecordRunSelection(ctx, "run", "gpt-6-luna", "max", "endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.FinishRun(ctx, "session", "run", "reply", "succeeded", "Hello"); err != nil {
		t.Fatal(err)
	}
	call := func(messageID, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.messageFeedback(w, httptest.NewRequest(http.MethodPut, "/feedback", strings.NewReader(body)), "session", messageID)
		return w
	}
	if w := call("reply", `{"value":"up"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"reasoningEffort":"max"`) {
		t.Fatalf("vote failed: %d %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	a.sessionFeedback(w, httptest.NewRequest(http.MethodGet, "/feedback", nil), "session")
	if !strings.Contains(w.Body.String(), `"reply":"up"`) {
		t.Fatal("feedback missing after refresh")
	}
	if w := call("reply", `{"value":null}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, body := range []string{`{}`, `{"value":"yes"}`, `{"value":true}`, `{"value":"up","modelId":"spoofed"}`} {
		if w := call("reply", body); w.Code != 400 {
			t.Fatalf("invalid vote accepted: %s", body)
		}
	}
	if w := call("user-message", `{"value":"up"}`); w.Code != 400 {
		t.Fatal("user-message vote accepted")
	}
	if w := call("not-a-message", `{"value":"up"}`); w.Code != 404 {
		t.Fatal("missing reply vote accepted")
	}
}

func TestCatalogModelsRemainAutomaticCandidatesAcrossProviders(t *testing.T) {
	count := 0
	for _, setup := range modelcatalog.ProviderSetups() {
		for _, id := range provider.CatalogTextModelIDs(setup.ID) {
			config := provider.CatalogConfig{Provider: "catalog", ProviderID: setup.ID, ID: id, APIKey: "fixture"}
			switch setup.ID {
			case "google-vertex":
				config.APIKey = ""
				config.AccessToken = "fixture"
				config.Project = "fixture-project"
			case "amazon-bedrock":
				config.Region = "us-east-1"
			case "azure":
				config.ResourceName = "fixture"
				config.DeploymentID = "deployment"
			}
			resolved, err := provider.ResolveCatalogConfig(config)
			if err != nil {
				continue
			} // Routes that require extra settings are covered by their setup tests.
			model := provider.CatalogModelRequest(resolved.Config, resolved.Metadata)
			if err := highestSelectionReasoning(&model); err != nil {
				t.Fatalf("%s/%s excluded by reasoning control: %v", setup.ID, id, err)
			}
			count++
		}
	}
	if count < 500 {
		t.Fatalf("catalog coverage unexpectedly small: %d", count)
	}
	t.Logf("checked automatic reasoning eligibility for %d catalog models", count)
}
