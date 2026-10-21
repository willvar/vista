package server

import (
	"context"
	"testing"

	"vista/internal/provider"
	"vista/internal/store"
)

func TestAutomaticTierAffinitySurvivesTierAndManualSwitches(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	if _, err := a.store.Create(ctx, "session", "tier affinity"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "manual", ModelKey: "manual-luna", ReasoningEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	qualityModel, qualityKey, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: "quality", InputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Mark this connection tried so the next newly configured connection is
	// deterministically chosen for speed; no model request is made.
	if err := a.store.FinishModelAttempt(ctx, prefs.UserID, qualityModel.ID, provider.ModelSelectionEffort(qualityModel), "succeeded"); err != nil {
		t.Fatal(err)
	}
	a.models.Models = append(a.models.Models, configuredModel{Key: "new-astra", ID: "gpt-6-astra", Provider: "openai-responses", BaseURL: "https://models.example/v1", Context: 128000})
	mode, speed := "auto", "speed"
	if _, err := a.store.PatchSessionSelection(ctx, "session", store.SessionSelectionPatch{SelectionMode: &mode, SelectionTier: &speed}); err != nil {
		t.Fatal(err)
	}
	speedModel, speedKey, err := a.resolveSelection(ctx, "session", selectionInput{InputTokens: 1})
	if err != nil || speedKey != "new-astra" {
		t.Fatalf("speed did not choose the untried connection: key=%s error=%v", speedKey, err)
	}
	if err := a.store.FinishModelAttempt(ctx, prefs.UserID, speedModel.ID, provider.ModelSelectionEffort(speedModel), "succeeded"); err != nil {
		t.Fatal(err)
	}
	// A fresh candidate would win a new cold-start selection. Tier switching
	// must retain its existing connection instead of starting that selection.
	a.models.Models = append(a.models.Models, configuredModel{Key: "new-review", ID: "codex-auto-review", Provider: "openai-responses", BaseURL: "https://models.example/v1", Context: 128000})
	for _, choice := range []struct{ tier, key string }{{"quality", qualityKey}, {"speed", speedKey}, {"quality", qualityKey}} {
		if _, err := a.store.PatchSessionSelection(ctx, "session", store.SessionSelectionPatch{SelectionTier: &choice.tier}); err != nil {
			t.Fatal(err)
		}
		model, key, err := a.resolveSelection(ctx, "session", selectionInput{InputTokens: 100})
		if err != nil || key != choice.key || model.ReasoningEffort != "max" {
			t.Fatalf("%s affinity changed: key=%s effort=%s error=%v", choice.tier, key, model.ReasoningEffort, err)
		}
		session, err := a.store.Get(ctx, "session")
		if err != nil || session.ModelKey != choice.key || session.SelectionMode != "auto" || session.SelectionTier != choice.tier || session.SelectionReasoningEffort != "max" || session.ManualModelKey != "manual-luna" || session.ManualReasoningEffort != "low" {
			t.Fatalf("active selection or independent manual memory changed: session=%+v error=%v", session, err)
		}
	}
	// An old effort cache must refresh its effort while retaining its model.
	if err := a.store.BindSessionTierSelection(ctx, "session", "quality", qualityKey, "low"); err != nil {
		t.Fatal(err)
	}
	model, key, err := a.resolveSelection(ctx, "session", selectionInput{InputTokens: 1})
	if err != nil || key != qualityKey || model.ReasoningEffort != "max" {
		t.Fatalf("effort refresh changed model: key=%s effort=%s error=%v", key, model.ReasoningEffort, err)
	}
	manual, manualKey, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "manual", ModelKey: speedKey, ReasoningEffort: "low"})
	if err != nil || manualKey != speedKey || manual.ReasoningEffort != "low" {
		t.Fatalf("manual selection failed: key=%s effort=%s error=%v", manualKey, manual.ReasoningEffort, err)
	}
	// Real manual executions replace the session's last-run display model.
	if _, _, err := a.store.BeginRun(ctx, "session", "manual-run", "manual-input", "manual request", manualKey); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.FinishRun(ctx, "session", "manual-run", "manual-answer", "succeeded", "manual result"); err != nil {
		t.Fatal(err)
	}
	for _, choice := range []struct{ tier, key string }{{"quality", qualityKey}, {"speed", speedKey}} {
		model, key, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: choice.tier, InputTokens: 1})
		if err != nil || key != choice.key || model.ReasoningEffort != "max" {
			t.Fatalf("manual run lost %s affinity: key=%s effort=%s error=%v", choice.tier, key, model.ReasoningEffort, err)
		}
	}
	manual, manualKey, err = a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "manual"})
	if err != nil || manualKey != speedKey || manual.ReasoningEffort != "low" {
		t.Fatalf("automatic tiers overwrote manual memory: key=%s effort=%s error=%v", manualKey, manual.ReasoningEffort, err)
	}
	currentPrefs, err := a.store.Preferences(ctx)
	if err != nil || currentPrefs.LastMode != prefs.LastMode || currentPrefs.LastTier != prefs.LastTier || currentPrefs.ManualModelKey != prefs.ManualModelKey {
		t.Fatalf("session affinity changed independent preferences: preferences=%+v error=%v", currentPrefs, err)
	}
}

func TestAutomaticTierAffinityReplacesIneligibleConnection(t *testing.T) {
	for _, reason := range []string{"removed", "tools", "status", "cooldown", "images"} {
		t.Run(reason, func(t *testing.T) {
			a := selectionApp(t)
			ctx := context.Background()
			if _, err := a.store.Create(ctx, "session", "eligibility"); err != nil {
				t.Fatal(err)
			}
			original, _, err := a.resolveSelection(ctx, "session", selectionInput{SelectionMode: "auto", SelectionTier: "quality", InputTokens: 1})
			if err != nil {
				t.Fatal(err)
			}
			a.models.Models = append(a.models.Models, configuredModel{Key: "replacement", ID: "replacement", Provider: "openai-responses", Context: 128000, InputModalities: []string{"text", "image"}})
			switch reason {
			case "removed":
				a.models.Models = a.models.Models[1:]
			case "tools":
				tools := false
				a.models.Models[0].Tools = &tools
			case "status":
				a.models.Models[0] = a.models.Models[0].WithStatus("unavailable")
			case "cooldown":
				if err := a.store.NoteConnectionFailure(ctx, original.ID, provider.ModelEndpointID(original)); err != nil {
					t.Fatal(err)
				}
			case "images":
				a.models.Models[0].InputModalities = []string{"text"}
			}
			_, key, err := a.resolveSelection(ctx, "session", selectionInput{InputTokens: 1, HasImages: reason == "images"})
			if err != nil || key != "replacement" {
				t.Fatalf("%s model was retained: key=%s error=%v", reason, key, err)
			}
			remembered, err := a.store.SessionTierSelection(ctx, "session", "quality")
			if err != nil || remembered == nil || remembered.ModelKey != "replacement" {
				t.Fatalf("replacement was not remembered: selection=%+v error=%v", remembered, err)
			}
		})
	}
}
