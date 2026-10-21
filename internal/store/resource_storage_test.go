package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResourceOwnershipDeduplicatesAndSurvivesUntilLastSessionDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	image := fixtureImage(t)
	for _, id := range []string{"first", "second"} {
		if _, err := s.Create(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		image.Name = id + ".png"
		if _, _, err := s.BeginRunWithImages(ctx, id, id+"-run", id+"-user", "look", []Image{image, image}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FinishRun(ctx, id, id+"-run", id+"-reply", "succeeded", "seen"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.MessageResources(ctx, "first", "first-user")
	if err != nil || len(first) != 2 || first[0].Name != "first.png" {
		t.Fatalf("first attachment references: %+v %v", first, err)
	}
	second, err := s.SessionResources(ctx, "second", "", 20)
	if err != nil || len(second) != 1 || second[0].Hash != first[0].Hash || second[0].Name != "second.png" {
		t.Fatalf("session names/deduplication: %+v %v", second, err)
	}
	assertStats := func(total, referenced, cleanable int) {
		t.Helper()
		stats, err := s.ResourceStorage(ctx)
		if err != nil || stats.TotalCount != total || stats.ReferencedCount != referenced || stats.CleanableCount != cleanable || stats.TotalBytes != int64(total*len(image.Data)) {
			t.Fatalf("resource accounting: %+v %v", stats, err)
		}
	}
	assertStats(1, 1, 0)
	if err := s.SetOpen(ctx, "first", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOpen(ctx, "second", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertStats(1, 1, 0)
	if count, _, err := s.CleanResources(ctx, nil); err != nil || count != 0 {
		t.Fatalf("closed session's original was cleaned: %d %v", count, err)
	}
	if err := s.Delete(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	assertStats(1, 1, 0)
	if count, _, err := s.CleanResources(ctx, nil); err != nil || count != 0 {
		t.Fatalf("shared original was cleaned: %d %v", count, err)
	}
	loaded, err := s.ResolveImages(ctx, second)
	if err != nil || !bytes.Equal(loaded[0].Data, image.Data) {
		t.Fatalf("remaining owner lost original: %v", err)
	}
	if err := s.Delete(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	assertStats(1, 0, 1)
	if count, size, err := s.CleanResources(ctx, nil); err != nil || count != 1 || size != int64(len(image.Data)) {
		t.Fatalf("last reference cleanup: count=%d bytes=%d %v", count, size, err)
	}
	assertStats(0, 0, 0)
	var metadata int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources").Scan(&metadata); err != nil || metadata != 0 {
		t.Fatalf("orphan metadata remained: %d %v", metadata, err)
	}
}

func TestWithdrawnAndDiscardedInputResourcesRemainSessionOwned(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "queue"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "user", "work"); err != nil {
		t.Fatal(err)
	}
	for index, withdraw := range []bool{false, true} {
		id := []string{"discard", "withdraw"}[index]
		image := fixtureImage(t)
		image.Data = append(image.Data, byte(index))
		image.Name = id + ".png"
		if _, _, err := s.QueueInputWithImages(ctx, "session", id, "later", "keep", []Image{image}); err != nil {
			t.Fatal(err)
		}
		_, refs, _, err := s.RemoveInput(ctx, "session", id, withdraw)
		if err != nil {
			t.Fatal(err)
		}
		if withdraw && (len(refs) != 1 || len(refs[0].Data) != 0 || refs[0].Hash == "") {
			t.Fatalf("withdrawal must return references only: %+v", refs)
		}
	}
	if _, err := s.FinishRun(ctx, "session", "run", "reply", "failed", "provider failed"); err != nil {
		t.Fatal(err)
	}
	if count, _, err := s.CleanResources(ctx, nil); err != nil || count != 0 {
		t.Fatalf("removed queue input lost session ownership: %d %v", count, err)
	}
	refs, err := s.SessionResources(ctx, "session", "", 20)
	if err != nil || len(refs) != 2 {
		t.Fatalf("retained queue resources: %+v %v", refs, err)
	}
	if _, err := s.ResolveImages(ctx, refs); err != nil {
		t.Fatal(err)
	}
}

func TestFailedResourceTransactionAndInterruptedUploadAreManuallyCleanable(t *testing.T) {
	s := selectionStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "rollback"); err != nil {
		t.Fatal(err)
	}
	// Publishing succeeds, then the attachment FK fails before references commit.
	image := fixtureImage(t)
	unlock, err := s.lockImages(ctx, []Image{image})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	err = s.writeImages(ctx, tx, "session", "message_resources", "message_id", "absent-message", []Image{image})
	tx.Rollback()
	unlock()
	if err == nil {
		t.Fatal("invalid attachment unexpectedly committed")
	}
	temp := filepath.Join(s.library.StateDir, "resources", ".upload-interrupted")
	if err := os.WriteFile(temp, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	stats, err := s.ResourceStorage(ctx)
	if err != nil || stats.TotalCount != 2 || stats.CleanableCount != 2 || stats.ReferencedCount != 0 {
		t.Fatalf("failed writes were not reclaimable: %+v %v", stats, err)
	}
	if count, freed, err := s.CleanResources(ctx, nil); err != nil || count != 2 || freed != int64(len(image.Data)+len("incomplete")) {
		t.Fatalf("failed write cleanup: %d %d %v", count, freed, err)
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged upload remained: %v", err)
	}
}

func TestResourceCleanupSerializesWithReferenceCommit(t *testing.T) {
	s := selectionStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	image := fixtureImage(t)
	if _, err := s.Create(ctx, "session", "concurrent"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "user", "look"); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.lockImages(ctx, []Image{image})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := s.writeImages(ctx, tx, "session", "message_resources", "message_id", "user", []Image{image}); err != nil {
		tx.Rollback()
		unlock()
		t.Fatal(err)
	}
	cleaned := make(chan error, 1)
	go func() { _, _, err := s.CleanResources(ctx, nil); cleaned <- err }()
	select {
	case err := <-cleaned:
		tx.Rollback()
		unlock()
		t.Fatalf("cleanup crossed an uncommitted reference: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	err = tx.Commit()
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-cleaned; err != nil {
		t.Fatal(err)
	}
	// Reuse the same hash while repeated cleanups run concurrently.
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, upload := range []bool{false, true} {
		wg.Add(1)
		go func(upload bool) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				var err error
				if upload {
					_, _, err = s.QueueInputWithImages(ctx, "session", strings.Repeat("q", i+1), "later", "", []Image{image})
				} else {
					_, _, err = s.CleanResources(ctx, nil)
				}
				if err != nil {
					errors <- err
					return
				}
			}
		}(upload)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	loaded, err := s.MessageImages(ctx, "session", "user")
	if err != nil || len(loaded) != 1 || !bytes.Equal(loaded[0].Data, image.Data) {
		t.Fatalf("concurrent cleanup left a dangling reference: %+v %v", loaded, err)
	}
}
