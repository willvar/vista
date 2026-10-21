package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSessionLifecyclePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "00000000-0000-4000-8000-000000000001"
	if _, err = s.Create(ctx, id, "test"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetOpen(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Open || item.Title != "test" {
		t.Fatalf("unexpected persisted session: %+v", item)
	}
	if err = s.SetOpen(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	items, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no sessions, got %+v", items)
	}
}
