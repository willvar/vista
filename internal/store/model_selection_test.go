package store

import (
	"context"
	"path/filepath"
	"testing"
)

func selectionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func stringValue(value string) *string { return &value }

func TestSelectionPreferencesPersistIdentityAndIndependentMemories(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "selection.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.ShowUnrecognizedModels {
		t.Fatal("unknown models must be hidden by default")
	}
	seen := true
	if _, err := s.PatchPreferences(ctx, PreferencesPatch{ManualModelKey: stringValue("manual-model"), ReasoningByModelID: map[string]string{"model-a": "low"}, ShowUnrecognizedModels: &seen}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchPreferences(ctx, PreferencesPatch{ReasoningByModelID: map[string]string{"model-b": "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prefs, err := s.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.UserID != initial.UserID || prefs.ManualModelKey != "manual-model" || prefs.ReasoningByModelID["model-a"] != "low" || prefs.ReasoningByModelID["model-b"] != "max" {
		t.Fatalf("preferences lost: %+v", prefs)
	}
	if !prefs.ShowUnrecognizedModels {
		t.Fatal("visibility preference lost on reopen")
	}
	hide := false
	if prefs, err := s.PatchPreferences(ctx, PreferencesPatch{ShowUnrecognizedModels: &hide}); err != nil || prefs.ShowUnrecognizedModels {
		t.Fatalf("explicit false ignored: %+v %v", prefs, err)
	}
	if _, err := s.Create(ctx, "session", "title"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchSessionSelection(ctx, "session", SessionSelectionPatch{ManualModelKey: stringValue("first"), ManualReasoningEffort: stringValue("low"), ProcessingTier: stringValue("fast")}); err != nil {
		t.Fatal(err)
	}
	item, err := s.PatchSessionSelection(ctx, "session", SessionSelectionPatch{ManualModelKey: stringValue("second")})
	if err != nil || item.ManualModelKey != "second" || item.ProcessingTier != "" || item.ManualReasoningEffort != "" {
		t.Fatalf("selection carried over to another model: %+v %v", item, err)
	}
}
