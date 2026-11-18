package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAutomaticSessionsMigrateWithoutSelectingAnotherConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`ALTER TABLE sessions ADD COLUMN selection_mode TEXT NOT NULL DEFAULT 'manual'`, `ALTER TABLE sessions ADD COLUMN selection_reasoning_effort TEXT NOT NULL DEFAULT ''`} {
		if err = st.orm.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.Create(ctx, "legacy", "keep title"); err != nil {
		t.Fatal(err)
	}
	if err = st.orm.Exec(`UPDATE sessions SET selection_mode='auto', model_key='last-actual', manual_model_key='stale-hand-pick', selection_reasoning_effort='high' WHERE id='legacy'`).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := st.Get(ctx, "legacy")
	if err != nil || session.Title != "keep title" || session.ManualModelKey != "last-actual" || session.ManualReasoningEffort != "high" || session.ProcessingTier != "" {
		t.Fatalf("migration: %+v %v", session, err)
	}
	if _, err = st.PatchSessionSelection(ctx, "legacy", SessionSelectionPatch{ManualModelKey: stringValue("new-choice")}); err != nil {
		t.Fatal(err)
	}
	if err = st.initModelSelectionSchema(); err != nil {
		t.Fatal(err)
	}
	session, err = st.Get(ctx, "legacy")
	if err != nil || session.ManualModelKey != "new-choice" {
		t.Fatalf("migration reapplied: %+v %v", session, err)
	}
}
