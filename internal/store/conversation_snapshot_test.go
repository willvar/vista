package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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

func TestConversationSnapshotLeavesChecklistInModelContextOnly(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "question"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "r", []ChecklistItem{{Text: "Old plan", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	items := []ChecklistItem{{Text: "Read source", Status: "done"}, {Text: "Verify result", Status: "in_progress"}}
	_, err := s.ReplaceChecklist(ctx, "s", "r", items)
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
	if len(view.Events) != 101 || view.Events[0].Sequence != request.Sequence {
		t.Fatalf("internal plan was projected or model identity lost: %+v", view.Events)
	}
	retained, err := s.RunChecklist(ctx, "s", "r")
	if err != nil || len(retained) != 2 || retained[1] != items[1] {
		t.Fatalf("internal checklist changed: %+v %v", retained, err)
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

func TestConversationSnapshotOwnsCompletePendingQueue(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "r", "u", "Start"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, "s", "delivered", "Not pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeliverInput(ctx, "s", "r", "delivered", "delivered-message"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if _, _, err := s.QueueInput(ctx, "s", fmt.Sprintf("pending-%02d", i), "Keep pending"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkQueuedInputs(ctx, "s", "r", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, "s", "fresh", "New deliverable input"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 110; i++ {
		if _, err := s.AppendModelDelta(ctx, "s", "r", 1, 1, "child"); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.QueuedInputs) != 33 || view.Session.PendingInputCount != 33 || !view.Session.InputQueuePaused {
		t.Fatalf("incomplete pending snapshot: %+v", view)
	}
	paused, queued := 0, 0
	for _, input := range view.QueuedInputs {
		switch input.Status {
		case "paused":
			paused++
		case "queued":
			queued++
		default:
			t.Fatalf("retained history in pending snapshot: %+v", input)
		}
	}
	if paused != 32 || queued != 1 {
		t.Fatalf("pause/delivery distinction lost: %d/%d", paused, queued)
	}
	data, err := json.Marshal(view.QueuedInputs)
	if err != nil {
		t.Fatal(err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	for _, input := range payload {
		for key := range input {
			if key != "id" && key != "status" && key != "content" && key != "imageCount" {
				t.Fatalf("unnecessary queue field: %s", key)
			}
		}
	}
}

func TestPendingQueueSharesSnapshotReadTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err := reader.Create(ctx, "s", "Queue"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.BeginRun(ctx, "s", "r", "u", "Start"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.QueueInput(ctx, "s", "pending", "Keep"); err != nil {
		t.Fatal(err)
	}
	err = reader.readView(ctx, func(view *Store) error {
		point, err := view.checkpoint(ctx, "s")
		if err != nil {
			return err
		}
		if _, err := writer.DeliverInput(ctx, "s", "r", "pending", "follow-up"); err != nil {
			return err
		}
		inputs, err := view.PendingInputs(ctx, "s")
		if err != nil {
			return err
		}
		if point.Session.PendingInputCount != 1 || len(inputs) != 1 || inputs[0].ID != "pending" {
			t.Fatalf("mixed input views: %+v %+v", point, inputs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := reader.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if after.Session.PendingInputCount != 0 || len(after.QueuedInputs) != 0 {
		t.Fatalf("delivery not visible in next snapshot: %+v", after)
	}
	data, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["queuedInputs"]) != "[]" {
		t.Fatalf("empty queue is not an explicit array: %s", fields["queuedInputs"])
	}
}

// Both views keep their latest page fixed while unrelated earlier events grow.
func TestConversationViewsWithUnrelatedOlderEvents(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := execSQL(ctx, s.orm, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	older := func(first, last int) {
		seed(`WITH RECURSIVE n(i) AS (VALUES(?) UNION ALL SELECT i+1 FROM n WHERE i<?)
 INSERT INTO events(session_id,sequence,kind,data,created_at)
 SELECT 's',i,CASE WHEN i%5=0 THEN 'model_result' ELSE 'tool' END,
 CAST('{"runId":"unrelated"}' AS BLOB),'older' FROM n`, first, last)
	}
	seed(`INSERT INTO events(session_id,sequence,kind,data,created_at) VALUES('s',100000,'internal',x'7b7d','boundary')`)
	older(1, 999)
	if _, _, err := s.BeginRun(ctx, "s", "recent", "question", "Read the latest reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordModelRequest(ctx, "s", "recent", "openai-chat", "model"); err != nil {
		t.Fatal(err)
	}
	seed(`WITH RECURSIVE n(i) AS (VALUES(100003) UNION ALL SELECT i+1 FROM n WHERE i<100112)
 INSERT INTO events(session_id,sequence,kind,data,created_at)
 SELECT 's',i,'model_result',CAST('{"runId":"recent","turn":1}' AS BLOB),'recent' FROM n`)
	if _, err := s.FinishRun(ctx, "s", "recent", "reply", "succeeded", "Saved answer"); err != nil {
		t.Fatal(err)
	}
	// A detached task can have a result after the reply. It belongs to the
	// run but must not be loaded as model usage for the message page.
	seed(`INSERT INTO events(session_id,sequence,kind,data,created_at)
 SELECT 's',MAX(sequence)+1,'model_result',CAST('{"runId":"recent","depth":1}' AS BLOB),'detached' FROM events WHERE session_id='s'`)
	read := func() (ConversationSnapshot, RunOverview) {
		t.Helper()
		snapshot, err := s.ConversationSnapshot(ctx, "s")
		if err != nil || len(snapshot.Results) != 110 || len(snapshot.Messages) != 2 || snapshot.CurrentRun == nil || snapshot.CurrentRun.ID != "recent" {
			t.Fatalf("unexpected snapshot: %d results, %d messages, run %+v, err %v", len(snapshot.Results), len(snapshot.Messages), snapshot.CurrentRun, err)
		}
		overview, err := s.RunOverview(ctx, "s", "recent")
		if err != nil || overview.ModelCalls != 1 || overview.FirstSequence <= 100000 || overview.ThroughSequence != snapshot.ThroughSequence {
			t.Fatalf("unexpected overview: %+v %v", overview, err)
		}
		return snapshot, overview
	}
	small, smallRun := read()
	older(1000, 99999)
	large, largeRun := read()
	if !reflect.DeepEqual(small, large) || !reflect.DeepEqual(smallRun, largeRun) {
		t.Fatal("unrelated history changed the latest conversation page or execution")
	}
}
