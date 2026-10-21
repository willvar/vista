package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestChecklistPersistsPerRunAndRejectsLateUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const session = "00000000-0000-4000-8000-000000000005"
	if _, err := s.Create(ctx, session, "checklist"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-1", "message-1", "plan"); err != nil {
		t.Fatal(err)
	}
	items := []ChecklistItem{{Text: "Inspect project", Status: "done"}, {Text: "Run checks", Status: "in_progress"}}
	event, err := s.ReplaceChecklist(ctx, session, "run-1", items)
	if err != nil || event == nil || event.Kind != "checklist" {
		t.Fatalf("checklist event: %+v, err=%v", event, err)
	}
	var payload struct {
		RunID string          `json:"runId"`
		Items []ChecklistItem `json:"items"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil || payload.RunID != "run-1" || len(payload.Items) != 2 {
		t.Fatalf("checklist event payload: %+v, err=%v", payload, err)
	}
	if _, err := s.FinishRun(ctx, session, "run-1", "reply-1", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	late, err := s.ReplaceChecklist(ctx, session, "run-1", []ChecklistItem{{Text: "Overwritten", Status: "done"}})
	if err != nil || late != nil {
		t.Fatalf("late checklist update: %+v, err=%v", late, err)
	}
	if err := s.SetOpen(ctx, session, false); err != nil {
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
	got, err := s.RunChecklist(ctx, session, "run-1")
	if err != nil || len(got) != 2 || got[0] != items[0] || got[1] != items[1] {
		t.Fatalf("persisted checklist: %+v, err=%v", got, err)
	}
	if _, err := s.ReplaceChecklist(ctx, session, "run-1", []ChecklistItem{{Text: "invalid", Status: "complete"}}); err == nil {
		t.Fatal("invalid status accepted")
	}
	if err := s.SetOpen(ctx, session, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-2", "message-2", "clear"); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunChecklist(ctx, session, "run-2")
	if err != nil || len(got) != 0 {
		t.Fatalf("new run inherited an old checklist: %+v, err=%v", got, err)
	}
	if _, err := s.ReplaceChecklist(ctx, session, "run-2", []ChecklistItem{{Text: "New work", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if event, err := s.ReplaceChecklist(ctx, session, "run-2", []ChecklistItem{}); err != nil || event == nil {
		t.Fatalf("clear checklist: %+v, err=%v", event, err)
	}
	got, err = s.RunChecklist(ctx, session, "run-2")
	if err != nil || len(got) != 0 {
		t.Fatalf("cleared checklist: %+v, err=%v", got, err)
	}
	got, err = s.RunChecklist(ctx, session, "run-1")
	if err != nil || len(got) != 2 || got[0] != items[0] || got[1] != items[1] {
		t.Fatalf("a later run changed the historical checklist: %+v, err=%v", got, err)
	}
	if err := s.Delete(ctx, session); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunChecklist(ctx, session, "run-1")
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted run checklist: %+v, err=%v", got, err)
	}
}
