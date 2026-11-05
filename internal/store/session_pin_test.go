package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionPinsPersistAndSortBeforePagination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	for i := range 13 {
		id := fmt.Sprintf("s%02d", i)
		if _, err := s.Create(ctx, id, "Session "+id); err != nil {
			t.Fatal(err)
		}
		// Equal timestamps also exercise the existing descending ID tie-break.
		if _, err := s.db.Exec("UPDATE sessions SET created_at='2026-01-01T00:00:00Z' WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOpen(ctx, "s00", false); err != nil {
		t.Fatal(err)
	}
	// Pin in reverse order: pin time must not replace the original ordering.
	for _, id := range []string{"s00", "s02", "s01"} {
		if err := s.SetPinned(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(items []Session) []string {
		result := make([]string, len(items))
		for i, item := range items {
			result[i] = item.ID
		}
		return result
	}
	page, err := s.SessionPage(ctx, Pagination{Page: 1, PageSize: 10}, SessionSearch{})
	want := []string{"s02", "s01", "s00", "s12", "s11", "s10", "s09", "s08", "s07", "s06"}
	if err != nil || page.Total != 13 || !reflect.DeepEqual(ids(page.Items), want) {
		t.Fatalf("first page: %v %+v, want %v", err, page, want)
	}
	second, err := s.SessionPage(ctx, Pagination{Page: 2, PageSize: 10}, SessionSearch{})
	if err != nil || !reflect.DeepEqual(ids(second.Items), []string{"s05", "s04", "s03"}) {
		t.Fatalf("second page: %v %+v", err, second)
	}
	open, err := s.List(ctx, true)
	if err != nil || len(open) != 12 || !reflect.DeepEqual(ids(open[:2]), []string{"s02", "s01"}) {
		t.Fatalf("open list: %v %+v", err, open)
	}
	closed, err := s.SessionPage(ctx, Pagination{}, SessionSearch{Text: "s00"}, false)
	if err != nil || closed.Total != 1 || !closed.Items[0].Pinned {
		t.Fatalf("closed search: %v %+v", err, closed)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "s00")
	if err != nil || !item.Pinned || item.Open || item.Title != "Session s00" || item.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("persistent metadata: %v %+v", err, item)
	}
	if err := s.SetOpen(ctx, "s00", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, "s02", false); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ListSnapshot(ctx, true)
	if err != nil || !reflect.DeepEqual(ids(snapshot.Items[:3]), []string{"s01", "s00", "s12"}) {
		t.Fatalf("unpin/reopen snapshot: %v %+v", err, snapshot)
	}
	if err := s.SetPinned(ctx, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing session: %v", err)
	}
}

func TestSessionPinMigrationDefaultsExistingSessionsToUnpinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, is_open INTEGER NOT NULL, model_key TEXT NOT NULL DEFAULT '',
 pending_kind TEXT NOT NULL DEFAULT '', pending_content TEXT NOT NULL DEFAULT '', pending_run_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL);
 INSERT INTO sessions(id,title,is_open,created_at) VALUES('legacy','Existing session',1,'2026-01-01T00:00:00Z')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, err := s.Get(context.Background(), "legacy")
	if err != nil || item.Pinned || item.Title != "Existing session" || !item.Open {
		t.Fatalf("migrated session: %v %+v", err, item)
	}
}
