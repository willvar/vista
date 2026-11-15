package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestServiceRestartRecoveryKeepsOneDurableNoticeAndQueuedInput(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash", true: "graceful"}[graceful], func(t *testing.T) {
			s, ctx := runtimeStoreFixture(t)
			if _, _, err := s.QueueInput(ctx, "session", "follow-up", "new direction"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.RecordRuntimeEvent(ctx, "session", notice("completed-tool")); err != nil {
				t.Fatal(err)
			}
			if graceful {
				if err := s.PrepareRestart(ctx); err != nil {
					t.Fatal(err)
				}
				if pending, _ := s.HasPendingRuntimeEvents(ctx, "session"); !pending {
					t.Fatal("lost previously completed tool")
				}
				event, err := s.FinishRun(ctx, "session", "source", "", "interrupted", "Cancelled by user")
				if err != nil {
					t.Fatal(err)
				}
				var finished struct{ Reason string }
				if event == nil || json.Unmarshal(event.Data, &finished) != nil || finished.Reason != "Execution interrupted by service shutdown" {
					t.Fatalf("shutdown mislabeled as user cancellation: %+v", event)
				}
				if _, err := s.InterruptRunning(ctx, "session"); err != nil {
					t.Fatal(err)
				}
			}
			// Another restart while waiting for admission must retain the work.
			for i := 0; i < 2; i++ {
				if err := s.RecoverExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				item, err := s.Get(ctx, "session")
				if err != nil || item.InputQueuePaused {
					t.Fatalf("service restart paused the input queue: %+v %v", item, err)
				}
			}
			input, err := s.NextQueuedInput(ctx, "session")
			if err != nil || input == nil || input.ID != "follow-up" {
				t.Fatalf("pending user input lost: %+v %v", input, err)
			}
			if event, err := s.BeginRuntimeRun(ctx, "session", "resume", "model"); err != nil || event != nil {
				t.Fatalf("recovery took priority over user input: %+v %v", event, err)
			}
			// The normal input run can consume both the retained completion and
			// restart notice. No additional user message is invented for recovery.
			if _, _, err := s.BeginRun(ctx, "session", "input-run", "input-user", "new direction"); err != nil {
				t.Fatal(err)
			}
			events, err := s.PendingRuntimeEvents(ctx, "session", "input-run", "main")
			if err != nil || len(events) != 2 || events[0].ID != "completed-tool" || events[1].Kind != "service_restart" {
				t.Fatalf("restart inbox: %+v %v", events, err)
			}
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind='runtime_event' AND json_extract(data,'$.kind')='service_restart'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate recovery: %d %v", count, err)
			}
		})
	}
}

func TestRestartRecoveryExcludesExplicitStopsAndCompletedWork(t *testing.T) {
	for _, mode := range []string{"cancel-before-stage", "cancel-after-stage", "closed", "completed", "failed", "question", "manual-compaction", "worker-crash"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx := runtimeStoreFixture(t)
			if mode == "cancel-before-stage" {
				if err := s.RequestRunCancellation(ctx, "session", "source"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "manual-compaction" {
				if _, err := s.AppendRunEvent(ctx, "session", "source", "context_status", map[string]any{"runId": "source", "manual": true, "phase": "compacting"}); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "worker-crash" {
				if err := s.PrepareRestart(ctx); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "cancel-after-stage":
				if err := s.RequestRunCancellation(ctx, "session", "source"); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := s.SetOpen(ctx, "session", false); err != nil {
					t.Fatal(err)
				}
			case "completed", "failed":
				status := "succeeded"
				if mode == "failed" {
					status = "failed"
				}
				if _, err := s.FinishRun(ctx, "session", "source", "answer", status, "finished"); err != nil {
					t.Fatal(err)
				}
			case "question":
				if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET pending_kind='question',pending_run_id='source',pending_content='{}' WHERE id='session'`); err != nil {
					t.Fatal(err)
				}
			case "worker-crash":
				if _, err := s.InterruptRunning(ctx, "session"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecoverExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			if pending, err := s.HasPendingRuntimeEvents(ctx, "session"); err != nil || pending {
				t.Fatalf("ineligible %s resumed: %v", mode, err)
			}
		})
	}
}

func TestRestartRecoveryKeepsOldInputsPausedWithoutBlockingNewTask(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash", true: "graceful"}[graceful], func(t *testing.T) {
			s, ctx := runtimeStoreFixture(t)
			if _, _, err := s.QueueInput(ctx, "session", "old-input", "retained old message"); err != nil {
				t.Fatal(err)
			}
			if err := s.RequestRunCancellation(ctx, "session", "source"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinishRun(ctx, "session", "source", "", "interrupted", "Cancelled by user"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.BeginRun(ctx, "session", "new-task", "new-input", "new task"); err != nil {
				t.Fatal(err)
			}
			if graceful {
				if err := s.PrepareRestart(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := s.FinishRun(ctx, "session", "new-task", "", "interrupted", "shutdown"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecoverExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			item, err := s.Get(ctx, "session")
			if err != nil || !item.InputQueuePaused {
				t.Fatalf("old input lost its pause: %+v %v", item, err)
			}
			resumed, err := s.BeginRuntimeRun(ctx, "session", "resumed-new-task", "model")
			if err != nil || resumed == nil {
				t.Fatalf("old paused input blocked new task recovery: %+v %v", resumed, err)
			}
			notices, err := s.PendingRuntimeEvents(ctx, "session", "resumed-new-task", "main")
			if err != nil || len(notices) != 1 || notices[0].RunID != "new-task" {
				t.Fatalf("wrong recovery: %+v %v", notices, err)
			}
			inputs, err := s.RecentInputs(ctx, "session")
			if err != nil || len(inputs) != 1 || inputs[0].Status != "paused" {
				t.Fatalf("old message was delivered: %+v %v", inputs, err)
			}
		})
	}
}

func TestRestartNoticeCommitsBeforeDeliveryAndIsNotRecreated(t *testing.T) {
	s, ctx := runtimeStoreFixture(t)
	if err := s.RecoverExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	event, err := s.BeginRuntimeRun(ctx, "session", "resumed", "model")
	if err != nil || event == nil {
		t.Fatalf("no resume: %+v %v", event, err)
	}
	items, err := s.PendingRuntimeEvents(ctx, "session", "resumed", "main")
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: %+v %v", items, err)
	}
	r := workerproto.Result{RunID: "resumed", Kind: "model_history", Turn: 1, Status: "runtime", History: &workerproto.ModelHistory{Path: "history.gob", EventID: items[0].ID}}
	if _, err := s.RecordModelHistory(ctx, "session", r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "session", "resumed", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.HasPendingRuntimeEvents(ctx, "session"); pending {
		t.Fatal("delivered recovery replayed")
	}
	messages, err := s.MessagesBefore(ctx, "session", 0, 20)
	if err != nil || len(messages) != 2 {
		t.Fatalf("recovery invented a formal message: %+v %v", messages, err)
	}
	var stored workerproto.Result
	events, _ := s.EventsAfter(ctx, "session", 0, 100)
	for _, e := range events {
		if e.Kind == "model_history" {
			json.Unmarshal(e.Data, &stored)
		}
	}
	if stored.History == nil || stored.History.EventID != "restart/source" {
		t.Fatal("delivery was not retained")
	}
}

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
