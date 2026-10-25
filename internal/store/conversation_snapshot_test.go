package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestConversationSnapshotBoundariesAndRootProgress(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	request, err := s.RecordModelRequest(ctx, "s", "r", "openai-chat", "model", "key", "", "high")
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.AppendRunEvent(ctx, "s", "r", "tool", map[string]any{"runId": "r", "status": "started", "tool": map[string]any{"name": "delegate"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 110; i++ {
		if _, err := s.AppendModelDelta(ctx, "s", "r", 1, 1, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Session.Executing || !view.Session.Running || !view.HasOlderEvents || view.HasOlder || len(view.Messages) != 1 {
		t.Fatalf("bad snapshot: %+v", view)
	}
	if len(view.Events) != 102 || view.Events[0].Sequence != request.Sequence || view.Events[1].Sequence != root.Sequence {
		t.Fatalf("root progress lost: %+v", view.Events[:2])
	}
	if view.ThroughSequence != view.Events[len(view.Events)-1].Sequence {
		t.Fatal("boundary does not cover events")
	}
	result, err := s.AppendRunEvent(ctx, "s", "r", "model_result", workerproto.Result{RunID: "r", Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "model", FinishReason: "stop"}})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := s.FinishRun(ctx, "s", "r", "a", "succeeded", "answer")
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if view.Session.Executing || view.Session.LatestRunStatus != "succeeded" || view.Session.LatestReplySequence != finished.Sequence || view.ThroughSequence != finished.Sequence {
		t.Fatalf("completion state disagrees: %+v", view.SyncCheckpoint)
	}
	if len(view.Results) != 1 || view.Results[0].Sequence != result.Sequence {
		t.Fatal("result metadata lost")
	}
	page, err := s.EventsThrough(ctx, "s", root.Sequence, result.Sequence, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 111 || page[len(page)-1].Sequence != result.Sequence {
		t.Fatalf("unbounded replay: %d", len(page))
	}
	if _, err = s.Revert(ctx, "s", "u", 1); err != nil {
		t.Fatal(err)
	}
	view, err = s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 0 || view.RevertedSequence != view.ThroughSequence || view.ThroughSequence <= finished.Sequence || view.Session.LatestReplySequence != 0 {
		t.Fatalf("rollback not reflected: %+v", view)
	}
}

func TestSnapshotReadViewDoesNotMixConcurrentCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err = s.Create(ctx, "s", "Snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	err = s.readView(ctx, func(view *Store) error {
		before, err := view.checkpoint(ctx, "s")
		if err != nil {
			return err
		}
		// A separate connection commits after this read transaction has begun.
		if _, err := writer.FinishRun(ctx, "s", "r", "a", "succeeded", "answer"); err != nil {
			return err
		}
		messages, err := view.MessagesBefore(ctx, "s", 0, 100)
		if err != nil {
			return err
		}
		events, err := view.EventsBefore(ctx, "s", 0, 100)
		if err != nil {
			return err
		}
		if len(messages) != 1 || messages[0].Sequence > before.ThroughSequence || len(events) != 1 || !before.Session.Executing {
			t.Fatal("snapshot mixed database views")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Messages) != 2 || after.Session.Executing || after.ThroughSequence <= 1 {
		t.Fatalf("new commit not visible: %+v", after)
	}
}
