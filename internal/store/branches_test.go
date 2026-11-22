package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"vista/internal/workerproto"
)

func TestForkKeepsSourceAndUsesItsOwnCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "vista.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	root := uuid.NewString()
	if _, e = s.Create(ctx, root, "Original"); e != nil {
		t.Fatal(e)
	}
	first, _, e := s.BeginRun(ctx, root, "prefix-run", "prefix-input", "common")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AppendRunEvent(ctx, root, "prefix-run", "context_notice", map[string]any{"depth": 0, "checkpoint": workerproto.ContextCheckpoint{Path: "common-only", ThroughSequence: first.Sequence}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FinishRun(ctx, root, "prefix-run", "prefix-answer", "succeeded", "common reply"); e != nil {
		t.Fatal(e)
	}
	removed, _, e := s.BeginRun(ctx, root, "old-run", "fork-target", "private suffix")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AppendRunEvent(ctx, root, "old-run", "context_notice", map[string]any{"depth": 0, "checkpoint": workerproto.ContextCheckpoint{Path: "source-only", ThroughSequence: removed.Sequence}}); e != nil {
		t.Fatal(e)
	}
	child := uuid.NewString()
	branch, e := s.Fork(ctx, root, child, removed.ID)
	if e != nil {
		t.Fatal(e)
	}
	if branch.ForkMessageID != removed.ID || branch.ParentID != root || branch.RootID != root || branch.ForkBefore != removed.Sequence {
		t.Fatal(branch)
	}
	if list, err := s.Branches(ctx, child); err != nil || len(list) != 2 || list[1].FirstMessageID != "" || list[1].FirstMessageSequence != 0 {
		t.Fatalf("an inherited message is not the branch's first message: %v, %v", list, err)
	}
	// The branch can only reference the old prefix, not own copies of it.
	for _, table := range []string{"messages", "runs", "model_output_chunks"} {
		var count int64
		err := s.orm.Table(table).Where("session_id=?", child).Count(&count).Error
		if err != nil || count != 0 {
			t.Fatalf("copied %s into branch (%d): %v", table, count, err)
		}
	}
	var copied int
	if err := s.orm.Raw(`SELECT count(*) FROM events WHERE session_id=? AND sequence<?`, child, removed.Sequence).Row().Scan(&copied); err != nil || copied != 0 {
		t.Fatalf("copied prefix events (%d): %v", copied, err)
	}

	after, e := s.ContextCheckpoint(ctx, child)
	if e != nil || after == nil || after.Path != "common-only" {
		t.Fatal(after, e)
	}
	after, e = s.ContextCheckpoint(ctx, root)
	if e != nil || after == nil || after.Path != "source-only" {
		t.Fatal(after, e)
	}
	history, e := s.MessagesAfter(ctx, child, 0, 100)
	if e != nil || len(history) != 2 || history[0].Content != "common" || history[1].Content != "common reply" {
		t.Fatal(history, e)
	}
	if _, _, e = s.BeginRun(ctx, child, "blocked-run", "blocked-input", "blocked"); !errors.Is(e, ErrBranchRunning) {
		t.Fatal(e)
	}
	if _, e = s.FinishRun(ctx, root, "old-run", "old-answer", "succeeded", "original reply"); e != nil {
		t.Fatal(e)
	}
	replacement, _, err := s.BeginRun(ctx, child, "child-run", "child-input", "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if _, e = s.AppendRunEvent(ctx, child, "child-run", "context_notice", map[string]any{"depth": 0, "checkpoint": workerproto.ContextCheckpoint{Path: "new-only", ThroughSequence: 10}}); e != nil {
		t.Fatal(e)
	}
	if after, e = s.ContextCheckpoint(ctx, root); e != nil || after.Path != "source-only" {
		t.Fatal(after, e)
	}
	if history, e = s.MessagesAfter(ctx, root, 0, 100); e != nil || len(history) != 4 || history[3].Content != "original reply" {
		t.Fatal(history, e)
	}
	if items, e := s.List(ctx, true); e != nil || len(items) != 1 || items[0].ID != root {
		t.Fatal(items, e)
	}
	if counts, e := s.SessionCounts(ctx); e != nil || counts.Total != 1 || counts.Open != 1 || counts.Running != 1 {
		t.Fatal(counts, e)
	}
	if list, e := s.Branches(ctx, child); e != nil || len(list) != 2 || list[1].ID != child || list[1].FirstMessageID != replacement.ID || list[1].FirstMessageSequence != replacement.Sequence {
		t.Fatal(list, e)
	}
}

func TestNestedForkReferencesTheSameMessagesAndBoundsOutputs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root, child, grandchild, earlier := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err = s.Create(ctx, root, "Original")
	check(err)
	image := fixtureImage(t)
	image.Name = "shared.png"
	first, originalEvent, err := s.BeginRunWithImages(ctx, root, "r1", "m1", "shared input", []Image{image})
	check(err)
	answer := "prefix output 🐱"
	_, err = s.AppendModelDelta(ctx, root, "r1", 1, 0, answer)
	check(err)
	job := Job{ID: "j1", SessionID: root, RunID: "r1", OutputPath: filepath.Join(dir, "job.txt"), Status: "starting"}
	_, err = s.RecordJob(ctx, job)
	check(err)
	job.Status = "succeeded"
	job.Complete = true
	_, err = s.RecordJob(ctx, job)
	check(err)
	artifact := Artifact{ID: "a1", SessionID: root, RunID: "r1", OutputPath: filepath.Join(dir, "artifact.txt"), Status: "started"}
	_, err = s.RecordArtifact(ctx, artifact)
	check(err)
	artifact.Status = "succeeded"
	artifact.Complete = true
	_, err = s.RecordArtifact(ctx, artifact)
	check(err)
	_, err = s.FinishRun(ctx, root, "r1", "reply1", "succeeded", answer, ModelOutputRef{Turn: 1, Bytes: len(answer)})
	check(err)
	partial, _, err := s.BeginRun(ctx, root, "r2", "m2", "another retained input")
	check(err)
	_, err = s.AppendModelDelta(ctx, root, "r2", 1, 0, "retained output")
	check(err)
	late := Job{ID: "late", SessionID: root, RunID: "r2", OutputPath: filepath.Join(dir, "late.txt"), Status: "starting"}
	_, err = s.RecordJob(ctx, late)
	check(err)
	_, _, err = s.QueueInput(ctx, root, "interjection", "private source input")
	check(err)
	cut, err := s.DeliverInput(ctx, root, "r2", "interjection", "private")
	check(err)
	_, err = s.AppendModelDelta(ctx, root, "r2", 1, 0, " PRIVATE SUFFIX")
	check(err)
	late.Status = "succeeded"
	_, err = s.RecordJob(ctx, late)
	check(err)
	_, err = s.FinishRun(ctx, root, "r2", "source-private-reply", "succeeded", "private source reply")
	check(err)
	_, err = s.Fork(ctx, root, child, cut.ID)
	check(err)
	if _, err = s.Revert(ctx, root, first.ID, first.Sequence); !errors.Is(err, ErrBranchHasChildren) {
		t.Fatalf("destructive rewrite of a borrowed prefix: %v", err)
	}
	childMessage, _, err := s.BeginRun(ctx, child, "r3", "child-input", "child only")
	check(err)
	if childMessage.Sequence < cut.Sequence {
		t.Fatalf("new event overlaps borrowed range: %+v", childMessage)
	}
	_, err = s.FinishRun(ctx, child, "r3", "child-reply", "succeeded", "child reply")
	check(err)
	childCut, _, err := s.BeginRun(ctx, child, "r4", "next-input", "child private tail")
	check(err)
	_, err = s.FinishRun(ctx, child, "r4", "next-reply", "succeeded", "child private reply")
	check(err)
	_, err = s.Fork(ctx, child, grandchild, childCut.ID)
	check(err)
	// The cutoff is itself borrowed. This derives a shorter ancestor range,
	// not an empty immediate-parent timeline or a second copy of the ancestor.
	_, err = s.Fork(ctx, grandchild, earlier, partial.ID)
	check(err)
	for _, tc := range []struct {
		id  string
		ids []string
	}{
		{child, []string{first.ID, "reply1", partial.ID, "child-input", "child-reply", "next-input", "next-reply"}},
		{grandchild, []string{first.ID, "reply1", partial.ID, "child-input", "child-reply"}},
		{earlier, []string{first.ID, "reply1"}},
	} {
		messages, err := s.MessagesAfter(ctx, tc.id, 0, 100)
		check(err)
		if len(messages) != len(tc.ids) {
			t.Fatalf("wrong visible history: %+v", messages)
		}
		for i, msg := range messages {
			if msg.ID != tc.ids[i] || msg.SessionID != tc.id {
				t.Fatalf("wrong branch or noncanonical ID: %+v", msg)
			}
		}
		event, err := s.RunEvent(ctx, tc.id, "r1", first.Sequence)
		check(err)
		if event.SessionID != tc.id || string(event.Data) != string(originalEvent.Data) {
			t.Fatalf("prefix event was not shared: %+v", event)
		}
		page, err := s.ReadFullModelOutput(ctx, tc.id, "r1", 1)
		check(err)
		if !page.Complete || page.Content != answer {
			t.Fatalf("completed output changed: %+v", page)
		}
		images, err := s.MessageImages(ctx, tc.id, first.ID)
		check(err)
		if len(images) != 1 || images[0].Name != image.Name || string(images[0].Data) != string(image.Data) {
			t.Fatalf("shared resource inaccessible: %+v", images)
		}
		j, err := s.GetJob(ctx, tc.id, job.ID)
		check(err)
		if j.OutputPath != job.OutputPath || j.Status != "succeeded" {
			t.Fatalf("wrong inherited job: %+v", j)
		}
		a, err := s.GetArtifact(ctx, tc.id, artifact.ID)
		check(err)
		if a.OutputPath != artifact.OutputPath || a.Status != "succeeded" {
			t.Fatalf("wrong inherited artifact: %+v", a)
		}
		if _, err := s.GetJob(ctx, tc.id, late.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("future job output was readable: %v", err)
		}
		if _, err := s.RunEvent(ctx, tc.id, "r2", cut.Sequence); !errors.Is(err, ErrNotFound) {
			t.Fatalf("private event was readable: %v", err)
		}
		if tc.id != earlier {
			bounded, err := s.ReadFullModelOutput(ctx, tc.id, "r2", 1)
			check(err)
			if bounded.Content != "retained output" || bounded.Complete {
				t.Fatalf("discarded output leaked across cutoff: %+v", bounded)
			}
		} else if _, err := s.ReadFullModelOutput(ctx, tc.id, "r2", 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("run outside shortened ancestor visible: %v", err)
		}
	}
	// Snapshots use the same projection even when opened through a fresh,
	// read-only handle, as the native Worker does when restoring context.
	reader, err := OpenReadOnly(path)
	check(err)
	defer reader.Close()
	point, err := reader.ConversationSnapshot(ctx, grandchild)
	check(err)
	if len(point.Messages) != 5 || point.Messages[0].ID != first.ID {
		t.Fatalf("snapshot discarded inherited records: %+v", point)
	}
	if err = s.Delete(ctx, root); !errors.Is(err, ErrBranchHasChildren) {
		t.Fatalf("could delete an ancestor that still owns shared records: %v", err)
	}
	check(s.Delete(ctx, earlier))
	check(s.Delete(ctx, grandchild))
	check(s.Delete(ctx, child))
	if _, err = reader.ReadFullModelOutput(ctx, root, "r1", 1); err != nil {
		t.Fatalf("deleting the branches removed canonical bytes: %v", err)
	}
}
