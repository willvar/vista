package store

import (
	"context"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func runtimeStoreFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "events"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "source", "user", "work"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}
func notice(id string) workerproto.RuntimeEvent {
	return workerproto.RuntimeEvent{ID: id, RunID: "source", Recipient: "main", Kind: "job", SourceID: "job", Status: "succeeded", Content: "completed"}
}

func TestRuntimeInboxDeliveryIsAtomicWithRetainedContext(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	first, ready, err := s.RecordRuntimeEvent(ctx, "session", notice("done"))
	if err != nil || !ready || first == nil {
		t.Fatalf("record: %+v %v", first, err)
	}
	duplicate, _, err := s.RecordRuntimeEvent(ctx, "session", notice("done"))
	if err != nil || duplicate.Sequence != first.Sequence {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	pending, err := s.PendingRuntimeEvents(ctx, "session", "source", "main")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	// An invalid history must not acknowledge the result.
	bad := workerproto.Result{RunID: "source", Kind: "model_history", Turn: 1, Status: "runtime", History: &workerproto.ModelHistory{EventID: "done"}}
	if _, err := s.RecordModelHistory(ctx, "session", bad); err == nil {
		t.Fatal("invalid retained history accepted")
	}
	bad.History.Path = "history.gob"
	if event, err := s.RecordModelHistory(ctx, "session", bad); err != nil || event == nil {
		t.Fatalf("commit delivery: %+v %v", event, err)
	}
	if event, err := s.RecordModelHistory(ctx, "session", bad); err != nil || event == nil {
		t.Fatalf("lost ACK retry: %+v %v", event, err)
	}
	if pending, err := s.PendingRuntimeEvents(ctx, "session", "source", "main"); err != nil || len(pending) != 0 {
		t.Fatalf("redelivery: %+v %v", pending, err)
	}
}

func TestCompletionStartsRunWithoutInventingUserMessageAndStopsAfterFailure(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	if _, err := s.FinishRun(ctx, "session", "source", "answer", "succeeded", "started background work"); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := s.RecordRuntimeEvent(ctx, "session", notice("first")); err != nil || !ready {
		t.Fatalf("late completion: %v", err)
	}
	event, err := s.BeginRuntimeRun(ctx, "session", "resume", "model")
	if err != nil || event == nil {
		t.Fatalf("continuation: %+v %v", event, err)
	}
	var users int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id='session' AND role='user'`).Scan(&users); err != nil || users != 1 {
		t.Fatal("runtime created synthetic user message")
	}
	if next, err := s.BeginRuntimeRun(ctx, "session", "duplicate", "model"); err != nil || next != nil {
		t.Fatalf("overlapping run: %+v %v", next, err)
	}
	if _, err := s.FinishRun(ctx, "session", "resume", "failure", "failed", "upstream failed"); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := s.RecordRuntimeEvent(ctx, "session", notice("after-failure")); err != nil || ready {
		t.Fatalf("late event restarted failed work: %v", err)
	}
	if pending, err := s.HasPendingRuntimeEvents(ctx, "session"); err != nil || pending {
		t.Fatal("stopped inbox still pending")
	}
}

func TestWorkerRestartAndMessageRollbackDiscardPendingWakeups(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	if _, _, err := s.RecordRuntimeEvent(ctx, "session", notice("pending")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InterruptRunning(ctx, "session", "Worker exited"); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.HasPendingRuntimeEvents(ctx, "session"); err != nil || pending {
		t.Fatal("restart resumed interrupted operation")
	}
}

func TestClosedSessionSuppressesDetachedResultsAndReopeningDoesNotResume(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	task := workerproto.AgentTask{ID: "main/background", ParentID: "main", Join: "detached", Status: "started", Title: "independent"}
	if _, err := s.RecordAgentTask(ctx, "session", workerproto.Result{RunID: "source", Kind: "agent_task", TaskID: task.ID, ParentTaskID: "main", Depth: 1, Task: &task}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordRuntimeEvent(ctx, "session", notice("before-close")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOpen(ctx, "session", false); err != nil {
		t.Fatal(err)
	}
	late := notice("closed-child")
	late.Recipient = task.ID
	if _, ready, err := s.RecordRuntimeEvent(ctx, "session", late); err != nil || ready {
		t.Fatalf("closed detached task accepted event: %v", err)
	}
	if err := s.SetOpen(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.HasPendingRuntimeEvents(ctx, "session"); err != nil || pending {
		t.Fatal("closing preserved pending continuation")
	}
}

func TestChildCompletionAcknowledgmentIsIdempotent(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	task := workerproto.AgentTask{ID: "main/child", ParentID: "main", Join: "required", Status: "started", Title: "work"}
	if _, err := s.RecordAgentTask(ctx, "session", workerproto.Result{RunID: "source", Kind: "agent_task", TaskID: task.ID, ParentTaskID: "main", Depth: 1, Task: &task}); err != nil {
		t.Fatal(err)
	}
	completion := notice("child-tool")
	completion.Recipient = task.ID
	if _, _, err := s.RecordRuntimeEvent(ctx, "session", completion); err != nil {
		t.Fatal(err)
	}
	first, err := s.DeliverChildRuntimeEvent(ctx, "session", "source", task.ID, completion.ID)
	if err != nil || first == nil {
		t.Fatalf("delivery: %+v %v", first, err)
	}
	retry, err := s.DeliverChildRuntimeEvent(ctx, "session", "source", task.ID, completion.ID)
	if err != nil || retry == nil || retry.Sequence != first.Sequence {
		t.Fatalf("retry: %+v %v", retry, err)
	}
}

func TestRuntimeContinuationSnapshotUsesActualRunClock(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	if _, err := s.FinishRun(ctx, "session", "source", "answer", "succeeded", "started"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordRuntimeEvent(ctx, "session", notice("wake")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginRuntimeRun(ctx, "session", "continuation", "model"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ConversationSnapshot(ctx, "session")
	if err != nil || snapshot.CurrentRun == nil || snapshot.CurrentRun.ID != "continuation" {
		t.Fatalf("wrong execution clock: %+v %v", snapshot.CurrentRun, err)
	}
}
