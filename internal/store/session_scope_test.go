package store

import (
	"context"
	"fmt"
	"testing"
)

func TestSessionScopeFiltersBeforeLimitAndKeepsBoundaries(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	_, event, err := s.BeginRun(ctx, "s", "run", "user", "Keep open")
	if err != nil {
		t.Fatal(err)
	}
	// More recent archives must not crowd open conversations out of the list.
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET created_at='2000-01-01T00:00:00Z' WHERE id='s'"); err != nil {
		t.Fatal(err)
	}
	for i := range 1001 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO sessions(id,title,is_open,created_at) VALUES(?,?,0,'2026-01-01T00:00:00Z')", fmt.Sprintf("archive-%04d", i), "Archived conversation"); err != nil {
			t.Fatal(err)
		}
	}
	point, err := s.ListSnapshot(ctx, true)
	if err != nil || len(point.Items) != 1 || point.Items[0].ID != "s" || !point.Items[0].Executing || point.ThroughSequences["s"] != event.Sequence || len(point.ThroughSequences) != 1 {
		t.Fatalf("open snapshot lost scope or boundary: %+v %v", point, err)
	}
	closed, err := s.List(ctx, false)
	if err != nil || len(closed) != 1000 {
		t.Fatalf("closed list: %d %v", len(closed), err)
	}
	for _, item := range closed {
		if item.Open {
			t.Fatalf("open entry in archive: %+v", item)
		}
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 1000 {
		t.Fatalf("unfiltered compatibility: %d %v", len(all), err)
	}
	if err := s.SetOpen(ctx, "s", false); err != nil {
		t.Fatal(err)
	}
	point, err = s.ListSnapshot(ctx, true)
	if err != nil || len(point.Items) != 0 || len(point.ThroughSequences) != 0 {
		t.Fatalf("closed entry retained in workspace: %+v %v", point, err)
	}
}
