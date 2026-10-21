package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

func expectTierSelection(t *testing.T, st *Store, id, tier, key, effort string) {
	t.Helper()
	selected, err := st.SessionTierSelection(context.Background(), id, tier)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		if selected != nil {
			t.Fatalf("unexpected %s affinity: %+v", tier, selected)
		}
		return
	}
	if selected == nil || selected.ModelKey != key || selected.ReasoningEffort != effort {
		t.Fatalf("%s affinity: %+v, want %s/%s", tier, selected, key, effort)
	}
}

func TestSessionTierSelectionsPersistAcrossManualRunsCloseAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tier-selection.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Create(ctx, "session", "tier choices"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchSessionSelection(ctx, "session", SessionSelectionPatch{ManualModelKey: stringValue("manual-model"), ManualReasoningEffort: stringValue("low")}); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []struct{ tier, key, effort string }{{"quality", "quality-model", "max"}, {"speed", "speed-model", "high"}, {"quality", "quality-model", "max"}} {
		if err := st.BindSessionTierSelection(ctx, "session", selected.tier, selected.key, selected.effort); err != nil {
			t.Fatal(err)
		}
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "max")
	expectTierSelection(t, st, "session", "speed", "speed-model", "high")
	if _, err := st.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionMode: stringValue("manual")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, "session", "manual-run", "manual-input", "Hi", "another-manual-model"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRun(ctx, "session", "manual-run", "manual-answer", "succeeded", "Hello"); err != nil {
		t.Fatal(err)
	}
	current, err := st.Get(ctx, "session")
	if err != nil || current.ModelKey != "another-manual-model" {
		t.Fatalf("manual run active model: %+v %v", current, err)
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "max")
	expectTierSelection(t, st, "session", "speed", "speed-model", "high")
	if err := st.SetOpen(ctx, "session", false); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOpen(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "max")
	expectTierSelection(t, st, "session", "speed", "speed-model", "high")
	if err := st.BindSessionTierSelection(ctx, "session", "quality", "quality-model", "xhigh"); err != nil {
		t.Fatal(err)
	}
	current, err = st.Get(ctx, "session")
	if err != nil || current.SelectionMode != "auto" || current.SelectionTier != "quality" || current.ModelKey != "quality-model" || current.SelectionReasoningEffort != "xhigh" || current.ManualModelKey != "manual-model" || current.ManualReasoningEffort != "low" {
		t.Fatalf("restored binding: %+v %v", current, err)
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "xhigh")
	expectTierSelection(t, st, "session", "speed", "speed-model", "high")
}

func TestSessionTierSelectionFallbackOnlyUsesKnownCurrentAutomaticTier(t *testing.T) {
	st := selectionStore(t)
	ctx := context.Background()
	if _, err := st.Create(ctx, "session", "current choice"); err != nil {
		t.Fatal(err)
	}
	if err := st.orm.Model(&sessionRow{}).Where("id=?", "session").Updates(map[string]any{"selection_mode": "auto", "selection_tier": "quality", "model_key": "quality-model", "selection_reasoning_effort": "max"}).Error; err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "max")
	expectTierSelection(t, st, "session", "speed", "", "")
	if _, err := st.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionTier: stringValue("speed")}); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "quality", "quality-model", "max")
	expectTierSelection(t, st, "session", "speed", "", "")
	current, err := st.Get(ctx, "session")
	if err != nil || current.SelectionReasoningEffort != "" {
		t.Fatalf("old binding was not cleared: %+v %v", current, err)
	}
	// A manual-mode active key with leftover effort cannot create automatic affinity.
	if err := st.orm.Model(&sessionRow{}).Where("id=?", "session").Updates(map[string]any{"selection_mode": "manual", "selection_tier": "speed", "model_key": "manual-model", "selection_reasoning_effort": "low"}).Error; err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "speed", "", "")
	if _, err := st.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionMode: stringValue("auto")}); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "speed", "", "")
	// Explicit cache wins even if the active key later changes independently.
	if err := st.BindSessionTierSelection(ctx, "session", "speed", "speed-model", "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.orm.Model(&sessionRow{}).Where("id=?", "session").Update("model_key", "another-active-model").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchSessionSelection(ctx, "session", SessionSelectionPatch{SelectionTier: stringValue("quality")}); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "speed", "speed-model", "high")
}

func TestSessionTierBindingSeedsPreviousTierAndKeepsSessionsIndependent(t *testing.T) {
	st := selectionStore(t)
	ctx := context.Background()
	for _, id := range []string{"session-a", "session-b"} {
		if _, err := st.Create(ctx, id, "choices"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.orm.Model(&sessionRow{}).Where("id=?", "session-a").Updates(map[string]any{"selection_mode": "auto", "selection_tier": "quality", "model_key": "quality-a", "selection_reasoning_effort": "max"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionTierSelection(ctx, "session-a", "speed", "speed-a", "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionTierSelection(ctx, "session-b", "quality", "quality-b", "max"); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session-a", "quality", "quality-a", "max")
	expectTierSelection(t, st, "session-a", "speed", "speed-a", "high")
	expectTierSelection(t, st, "session-b", "quality", "quality-b", "max")
	expectTierSelection(t, st, "session-b", "speed", "", "")
	if err := st.BindSessionSelection(ctx, "session-a", "speed-a", "low"); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session-a", "speed", "speed-a", "low")
}

func TestSessionTierBindingRollsBackAffinityIfActiveUpdateFails(t *testing.T) {
	st := selectionStore(t)
	ctx := context.Background()
	if _, err := st.Create(ctx, "session", "atomic choice"); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("active binding update failed")
	const callback = "fail_active_tier_binding"
	if err := st.orm.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sessions" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := st.BindSessionTierSelection(ctx, "session", "quality", "quality-model", "max")
	st.orm.Callback().Update().Remove(callback)
	if !errors.Is(err, injected) {
		t.Fatalf("binding error: %v", err)
	}
	expectTierSelection(t, st, "session", "quality", "", "")
	current, err := st.Get(ctx, "session")
	if err != nil || current.SelectionMode != "manual" || current.ModelKey != "" || current.SelectionReasoningEffort != "" {
		t.Fatalf("failed binding changed session: %+v %v", current, err)
	}
}

func TestSessionTierBindingRejectsMissingSessionsAndInvalidTiers(t *testing.T) {
	st := selectionStore(t)
	ctx := context.Background()
	if _, err := st.SessionTierSelection(ctx, "missing", "quality"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session read: %v", err)
	}
	if err := st.BindSessionTierSelection(ctx, "missing", "quality", "model", "max"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session bind: %v", err)
	}
	if _, err := st.Create(ctx, "session", "validate"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionTierSelection(ctx, "session", "bogus"); err == nil {
		t.Fatal("invalid tier read accepted")
	}
	if err := st.BindSessionTierSelection(ctx, "session", "bogus", "model", "max"); err == nil {
		t.Fatal("invalid tier bind accepted")
	}
	if err := st.BindSessionTierSelection(ctx, "session", "quality", "", "max"); err == nil {
		t.Fatal("empty model key accepted")
	}
	var count int64
	if err := st.orm.Model(&sessionTierSelectionRow{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid bindings left affinities: %d %v", count, err)
	}
}

func TestSessionDeleteRemovesTierSelectionsBeforeIDCanBeReused(t *testing.T) {
	st := selectionStore(t)
	ctx := context.Background()
	if _, err := st.Create(ctx, "session", "choices"); err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionTierSelection(ctx, "session", "quality", "quality-model", "max"); err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionTierSelection(ctx, "session", "speed", "speed-model", "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := st.orm.Model(&sessionTierSelectionRow{}).Where("session_id=?", "session").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted session retained affinities: %d %v", count, err)
	}
	if _, err := st.Create(ctx, "session", "new choices"); err != nil {
		t.Fatal(err)
	}
	expectTierSelection(t, st, "session", "quality", "", "")
	expectTierSelection(t, st, "session", "speed", "", "")
}
