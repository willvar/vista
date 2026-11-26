package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestHistoryVersionsKeepCanonicalHistoryAndFrozenBranches(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close()
		}
	})
	root := uuid.NewString()
	if _, err := s.Create(ctx, root, "History"); err != nil {
		t.Fatal(err)
	}
	if mode, err := s.Branching(ctx); err != nil || mode {
		t.Fatalf("missing preference should default off: %v, %v", mode, err)
	}
	turn := func(text string) Message {
		t.Helper()
		run := uuid.NewString()
		m, _, err := s.BeginRun(ctx, root, run, uuid.NewString(), text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.FinishRun(ctx, root, run, uuid.NewString(), "succeeded", "reply: "+text); err != nil {
			t.Fatal(err)
		}
		return m
	}
	visible := func(id, want, unwanted string) {
		t.Helper()
		messages, err := s.MessagesAfter(ctx, id, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(messages)
		if !strings.Contains(string(data), want) || strings.Contains(string(data), unwanted) {
			t.Fatalf("wrong visible history: %s", data)
		}
		entries, err := s.ContextEntries(ctx, id, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		data, _ = json.Marshal(entries)
		if !strings.Contains(string(data), want) || strings.Contains(string(data), unwanted) {
			t.Fatalf("wrong model history: %s", data)
		}
	}
	turn("shared prefix")
	old := turn("original future")
	third := turn("other turn")
	child := uuid.NewString()
	if _, err := s.Fork(ctx, root, child, third.ID); err != nil {
		t.Fatal(err)
	}
	_, _, change, err := s.HistoryDraft(ctx, root, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForkForChange(ctx, root, uuid.NewString(), old.ID, HistoryChange{}); !errors.Is(err, ErrHistoryChanged) {
		t.Fatalf("legacy mode was silently changed: %v", err)
	}
	// A duplicate run ID fails after the cut would have been computed. The
	// write must not leave behind the cut or a saved version.
	if _, _, err := s.BeginReplacementRun(ctx, root, old.RunID, uuid.NewString(), old.ID, "failed replacement", nil, change, "", "en"); err == nil {
		t.Fatal("expected duplicate run to fail")
	}
	if versions, err := s.HistoryVersions(ctx, root, 0); err != nil || len(versions.Items) != 0 {
		t.Fatalf("failed send saved a version: %+v %v", versions, err)
	}
	visible(root, old.Content, "failed replacement")
	run := uuid.NewString()
	m, event, err := s.BeginReplacementRun(ctx, root, run, uuid.NewString(), old.ID, "new future", nil, change, "", "en")
	if err != nil || m.SessionID != root || event.Sequence <= *change.ThroughSequence {
		t.Fatalf("replacement send: %v, %+v", err, m)
	}
	if _, err := s.FinishRun(ctx, root, run, uuid.NewString(), "succeeded", "new answer"); err != nil {
		t.Fatal(err)
	}
	visible(root, "new future", "original future")
	visible(child, "original future", "new future")
	if branches, err := s.Branches(ctx, root); err != nil || len(branches) != 2 {
		t.Fatalf("replacement created a branch: %+v %v", branches, err)
	}
	if _, err := s.WithdrawInPlace(ctx, root, m.ID, change); !errors.Is(err, ErrHistoryChanged) {
		t.Fatalf("stale cursor allowed: %v", err)
	}
	latest := turn("later turn")
	after := uuid.NewString()
	if _, err := s.Fork(ctx, root, after, latest.ID); err != nil {
		t.Fatal(err)
	}
	visible(after, "new future", "original future")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.HistoryVersions(ctx, root, 0)
	if err != nil || len(versions.Items) != 1 || versions.Items[0].Reason != "edit" {
		t.Fatalf("saved version missing after reopen: %+v %v", versions, err)
	}
	preview, err := s.PreviewHistoryVersion(ctx, root, versions.Items[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreHistoryVersion(ctx, root, versions.Items[0].Version, &preview.ThroughSequence); err != nil {
		t.Fatal(err)
	}
	visible(root, "original future", "new future")
	if code, err := s.ReplyLanguage(ctx, root); err != nil || code != "en" {
		t.Fatalf("restore changed reply language: %q %v", code, err)
	}
	visible(after, "new future", "original future")
	point, err := s.Checkpoint(ctx, root)
	if err != nil || !point.HasHistoryVersions || point.Session.Executing || point.RevertedSequence <= preview.ThroughSequence {
		t.Fatalf("invalid restore checkpoint: %+v %v", point, err)
	}
	versions, err = s.HistoryVersions(ctx, root, 0)
	if err != nil || len(versions.Items) != 2 || versions.Items[0].Reason != "restore" {
		t.Fatalf("restore displaced an unsaved version: %+v %v", versions, err)
	}
}
