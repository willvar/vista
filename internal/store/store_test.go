package store

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
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

func TestStoreMixedTransactionRollsBackStateAndEvent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "original"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("abort combined mutation")
	err = s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&sessionRow{}).Where("id=?", "session").Update("title", "uncommitted").Error; err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, "session", "title_changed", map[string]string{"title": "uncommitted"}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "session")
	if err != nil || item.Title != "original" {
		t.Fatalf("state escaped rollback: %+v %v", item, err)
	}
	events, err := s.EventsAfter(ctx, "session", 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("event escaped rollback: %+v %v", events, err)
	}
	if err := s.Rename(ctx, "session", "committed"); err != nil {
		t.Fatal(err)
	}
	item, err = s.Get(ctx, "session")
	events, eventErr := s.EventsAfter(ctx, "session", 0, 100)
	if err != nil || eventErr != nil || item.Title != "committed" || len(events) != 1 {
		t.Fatalf("committed mutation incomplete: %+v %+v %v %v", item, events, err, eventErr)
	}
}

func TestStoreCanceledReadReleasesPoolAndPreservesZeroValues(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "test"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.begin(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = s.Get(deadline, "session")
	tx.Rollback()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost canceled read: %v", err)
	}
	if err := s.SetPinned(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, "session", false); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "session")
	if err != nil || item.Pinned {
		t.Fatalf("zero value update failed: %+v %v", item, err)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ORM not-found identity: %v", err)
	}
	if _, err := s.RunningRunID(ctx, "session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SQL not-found identity: %v", err)
	}
	if s.db.Stats().InUse != 0 {
		t.Fatal("read retained a pooled connection")
	}
}

func TestWorkerOwnershipKeepsExactProcessIdentity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "Worker"); err != nil {
		t.Fatal(err)
	}
	old := WorkerOwner{SessionID: "session", PID: 123, StartTime: ^uint64(0)}
	current := WorkerOwner{SessionID: "session", PID: 456, StartTime: 100}
	for _, owner := range []WorkerOwner{old, current} {
		if err := s.SaveWorkerOwner(ctx, owner); err != nil {
			t.Fatal(err)
		}
		owners, err := s.WorkerOwners(ctx)
		if err != nil || len(owners) != 1 || owners[0] != owner {
			t.Fatalf("saved process identity: %+v %v", owners, err)
		}
	}
	if err := s.ClearWorkerOwner(ctx, old); err != nil {
		t.Fatal(err)
	}
	owners, err := s.WorkerOwners(ctx)
	if err != nil || len(owners) != 1 || owners[0] != current {
		t.Fatalf("stale cleanup removed current owner: %+v %v", owners, err)
	}
	if err := s.ClearWorkerOwner(ctx, current); err != nil {
		t.Fatal(err)
	}
	owners, err = s.WorkerOwners(ctx)
	if err != nil || len(owners) != 0 {
		t.Fatalf("current owner not removed: %+v %v", owners, err)
	}
}
