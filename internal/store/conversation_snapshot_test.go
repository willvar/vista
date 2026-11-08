package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestConversationSnapshotRetainsRootRetryAcrossChildTraffic(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	request, err := s.RecordModelRequest(ctx, "s", "r", "openai-chat", "model")
	if err != nil {
		t.Fatal(err)
	}
	retry := workerproto.Result{RunID: "r", Turn: 1, Status: "waiting", ModelRetry: &workerproto.ModelRetry{Attempt: 1, MaxRetries: 3, RetryAt: "2026-11-07T01:00:00Z", Error: "HTTP 502"}}
	waiting, err := s.AppendRunEvent(ctx, "s", "r", "model_retry", retry)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 110; i++ {
		if _, err := s.AppendModelDelta(ctx, "s", "r", 1, 1, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.ConversationSnapshot(ctx, "s")
	if err != nil || len(view.Events) != 102 || view.Events[0].Sequence != request.Sequence || view.Events[1].Sequence != waiting.Sequence || !view.Session.Executing {
		t.Fatalf("retry progress lost from bounded snapshot: %+v err=%v", view, err)
	}
	overview, err := s.RunOverview(ctx, "s", "r")
	if err != nil || overview.ModelCalls != 1 {
		t.Fatalf("retry overview: %+v err=%v", overview, err)
	}
	for _, event := range overview.Events {
		if event.Kind == "model_retry" {
			var got workerproto.Result
			if err := json.Unmarshal(event.Data, &got); err != nil || got.ModelRetry == nil || *got.ModelRetry != *retry.ModelRetry {
				t.Fatalf("retry payload changed: %+v err=%v", got, err)
			}
			return
		}
	}
	t.Fatal("retry missing from run overview")
}

func TestConversationSnapshotRetainsLatestChecklistOutsideRecentEvents(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "r", []ChecklistItem{{Text: "Old plan", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	items := []ChecklistItem{{Text: "Read source", Status: "done"}, {Text: "Verify result", Status: "in_progress"}}
	checklist, err := s.ReplaceChecklist(ctx, "s", "r", items)
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.RecordModelRequest(ctx, "s", "r", "openai-chat", "model")
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
	if len(view.Events) != 102 || view.Events[0].Sequence != checklist.Sequence || view.Events[1].Sequence != request.Sequence {
		t.Fatalf("latest checklist or model identity lost from bounded snapshot: %+v", view.Events)
	}
	var data struct{ Items []ChecklistItem }
	if err := json.Unmarshal(view.Events[0].Data, &data); err != nil || len(data.Items) != 2 || data.Items[1] != items[1] {
		t.Fatalf("checklist changed: %+v err=%v", data, err)
	}
	if _, err := s.FinishRun(ctx, "s", "r", "a", "succeeded", "answer"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "s", "next", "next-user", "new question"); err != nil {
		t.Fatal(err)
	}
	view, err = s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range view.Events {
		if event.Kind == "checklist" {
			t.Fatal("a new run inherited the old plan outside its recent history")
		}
	}
}

func TestConversationSnapshotClockSurvivesPagingAndIgnoresAuxiliaryTraffic(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	started := "2026-11-08T01:00:00Z"
	if err := s.orm.Exec("UPDATE runs SET created_at=? WHERE id='r'", started).Error; err != nil {
		t.Fatal(err)
	}
	var last *Event
	for i := 0; i < 110; i++ {
		var err error
		last, err = s.AppendModelDelta(ctx, "s", "r", 1, 1, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	activity := "2026-11-08T01:15:00Z"
	if err := s.orm.Exec("UPDATE events SET created_at=? WHERE sequence=?", activity, last.Sequence).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, "s", "r", "model_request", map[string]any{"runId": "r", "purpose": "summary"}); err != nil {
		t.Fatal(err)
	}
	view, err := s.ConversationSnapshot(ctx, "s")
	if err != nil || view.CurrentRun == nil || *view.CurrentRun != (RunClock{ID: "r", StartedAt: started, LastActivityAt: activity}) {
		t.Fatalf("paged execution clock: %+v error=%v", view.CurrentRun, err)
	}
}

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
