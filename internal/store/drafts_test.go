package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDraftCASAndSendRetainNewerVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.SaveDraft(ctx, HomeDraft, Draft{Text: "first"})
	if err != nil || d.Revision != 1 {
		t.Fatal(d, err)
	}
	if c, err := s.SessionCounts(ctx); err != nil || c.Total != 0 {
		t.Fatal(c, err)
	}
	if _, err := s.SaveDraft(ctx, HomeDraft, Draft{Text: "late"}); !errors.Is(err, ErrDraftConflict) {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "first", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "second", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDraft(ctx, HomeDraft, Draft{Revision: 1, Text: "more recent"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginDraftRun(ctx, "first", "run-1", "input-1", "first", nil, &DraftVersion{HomeDraft, 1}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Draft(ctx, HomeDraft)
	if err != nil || after.Revision != 2 || after.Text != "more recent" {
		t.Fatal(after, err)
	}
	if _, _, err := s.BeginDraftRun(ctx, "second", "run-1", "dupe", "not accepted", nil, &DraftVersion{HomeDraft, 2}); err == nil {
		t.Fatal("duplicate run accepted")
	}
	after, err = s.Draft(ctx, HomeDraft)
	if err != nil || after.Revision != 2 || after.Text != "more recent" {
		t.Fatal(after, err)
	}
	if _, _, err := s.BeginDraftRun(ctx, "second", "run-2", "input-2", "more recent", nil, &DraftVersion{HomeDraft, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err = reopened.Draft(ctx, HomeDraft)
	if err != nil || after.Revision != 3 || after.Text != "" {
		t.Fatal(after, err)
	}
	if _, err := reopened.SaveDraft(ctx, HomeDraft, Draft{Revision: 2, Text: "late"}); !errors.Is(err, ErrDraftConflict) {
		t.Fatal(err)
	}
}

func TestDraftOriginalIsOwnedBeforeSendAndReclaimedAfterRemoval(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0}
	d, err := s.UploadDraftImage(ctx, HomeDraft, 0, "test.png", "image/png", bytes.NewReader(original))
	if err != nil || len(d.Images) != 1 {
		t.Fatal(d, err)
	}
	ref := d.Images[0]
	if _, err := s.DraftImage(ctx, "unknown", ref.Hash); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if stats, err := s.ResourceStorage(ctx); err != nil || stats.ReferencedCount != 1 {
		t.Fatal(stats, err)
	}
	if _, err := s.Create(ctx, "s", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionResource(ctx, "s", ref.Hash); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft leaked into history", err)
	}
	if _, _, err := s.BeginDraftRun(ctx, "s", "run", "msg", "", []Image{{Hash: ref.Hash, MediaType: ref.MediaType, Bytes: ref.Bytes, Name: ref.Name}}, &DraftVersion{HomeDraft, d.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DraftImage(ctx, HomeDraft, ref.Hash); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	images, err := s.MessageImages(ctx, "s", "msg")
	if err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, original) {
		t.Fatal(images, err)
	}
	if _, err := s.UploadDraftImage(ctx, HomeDraft, d.Revision+1, "new.png", "image/png", bytes.NewReader(append(original, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDraft(ctx, HomeDraft, Draft{Revision: d.Revision + 2}); err != nil {
		t.Fatal(err)
	}
	if removed, _, err := s.CleanResources(ctx, nil); err != nil || removed != 1 {
		t.Fatal(removed, err)
	}
}
