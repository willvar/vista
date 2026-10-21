package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"vista/internal/question"
	"vista/internal/workerproto"
)

func revertStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Create(context.Background(), "s", "New session"); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestRevertRemovesFutureContextAndPreservesSequence(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	_, _, err := s.BeginRun(ctx, "s", "r1", "m1", "keep")
	if err != nil {
		t.Fatal(err)
	}
	s.AppendModelDelta(ctx, "s", "r1", 1, 0, "kept answer")
	s.RecordModelHistory(ctx, "s", workerproto.Result{RunID: "r1", Turn: 1, Status: "assistant", History: &workerproto.ModelHistory{Path: "keep.gob"}})
	s.FinishRun(ctx, "s", "r1", "a1", "succeeded", "kept answer", ModelOutputRef{Turn: 1, Bytes: 11})
	target, _, err := s.BeginRun(ctx, "s", "r2", "m2", "discard")
	if err != nil {
		t.Fatal(err)
	}
	s.RecordModelHistory(ctx, "s", workerproto.Result{RunID: "r2", Turn: 1, Status: "assistant", History: &workerproto.ModelHistory{Path: "discard.gob"}})
	s.AppendRunEvent(ctx, "s", "r2", "context_notice", map[string]any{"depth": 0, "checkpoint": workerproto.ContextCheckpoint{Path: "future-checkpoint.gob", ThroughSequence: target.Sequence}})
	s.ReplaceChecklist(ctx, "s", "r2", []ChecklistItem{{Text: "future checklist", Status: "done"}})
	s.QueueInput(ctx, "s", "queue", "later", "discard queued input")
	s.FinishRun(ctx, "s", "r2", "a2", "succeeded", "future answer")
	before, _ := s.EventsAfter(ctx, "s", 0, 200)
	event, err := s.Revert(ctx, "s", target.ID, target.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence <= before[len(before)-1].Sequence {
		t.Fatal("SSE cursor moved backwards")
	}
	messages, _ := s.MessagesAfter(ctx, "s", 0, 100)
	if len(messages) != 2 || messages[0].ID != "m1" || messages[1].ID != "a1" {
		t.Fatalf("wrong effective history: %+v", messages)
	}
	entries, err := s.ContextEntries(ctx, "s", nil, 0)
	if err != nil || len(entries) != 2 || entries[1].History.History.Path != "keep.gob" {
		t.Fatalf("future context survived: %+v %v", entries, err)
	}
	checkpoint, err := s.ContextCheckpoint(ctx, "s")
	if err != nil || checkpoint != nil {
		t.Fatal("future checkpoint survived", checkpoint, err)
	}
	list, _ := s.RunChecklist(ctx, "s", "r2")
	if len(list) != 0 {
		t.Fatal("future checklist survived")
	}
	queue, _ := s.RecentInputs(ctx, "s")
	if len(queue) != 0 {
		t.Fatal("discarded queue survived")
	}
	if _, err := s.RevertTarget(ctx, "s", target.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleted input still readable", err)
	}
	if _, err := s.HistoryMessage(ctx, "s", target.Sequence); !errors.Is(err, ErrNotFound) {
		t.Fatal("history tool can resurrect withdrawn input", err)
	}
	if event, err := s.RecordModelHistory(ctx, "s", workerproto.Result{RunID: "r2", Turn: 2, Status: "assistant", History: &workerproto.ModelHistory{Path: "late.gob"}}); err != nil || event != nil {
		t.Fatal("late result resurrected discarded run", event, err)
	}
	next, _, err := s.BeginRun(ctx, "s", "r3", "m3", "replacement")
	if err != nil || next.Sequence <= event.Sequence {
		t.Fatal("replacement reused event sequence", next, err)
	}
}
func TestRevertInterjectionKeepsEarlierReplyAndOutput(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	s.BeginRun(ctx, "s", "run", "original", "first")
	s.AppendModelDelta(ctx, "s", "run", 1, 0, "first answer")
	s.RecordModelHistory(ctx, "s", workerproto.Result{RunID: "run", Turn: 1, Status: "assistant", History: &workerproto.ModelHistory{Path: "first.gob"}})
	s.QueueInput(ctx, "s", "input", "now", "second")
	target, err := s.DeliverInput(ctx, "s", "run", "input", "interjection", CompletedReply{ID: "first-reply", Content: "first answer", Output: ModelOutputRef{Turn: 1, Bytes: 12}})
	if err != nil || target == nil {
		t.Fatal(err)
	}
	s.AppendModelDelta(ctx, "s", "run", 2, 0, "second answer")
	s.RecordModelHistory(ctx, "s", workerproto.Result{RunID: "run", Turn: 2, Status: "assistant", History: &workerproto.ModelHistory{Path: "second.gob"}})
	s.FinishRun(ctx, "s", "run", "second-reply", "succeeded", "second answer", ModelOutputRef{Turn: 2, Bytes: 13})
	if _, err := s.Revert(ctx, "s", target.ID, target.Sequence); err != nil {
		t.Fatal(err)
	}
	messages, _ := s.MessagesAfter(ctx, "s", 0, 100)
	if len(messages) != 2 || messages[1].ID != "first-reply" {
		t.Fatalf("interjection removed earlier reply: %+v", messages)
	}
	output, err := s.ReadFullModelOutput(ctx, "s", "run", 1)
	if err != nil || output.Content != "first answer" {
		t.Fatal(output, err)
	}
	if _, err := s.ReadFullModelOutput(ctx, "s", "run", 2); err == nil {
		t.Fatal("discarded output readable")
	}
	entries, err := s.ContextEntries(ctx, "s", nil, 0)
	if err != nil || len(entries) != 2 || entries[1].History.History.Path != "first.gob" {
		t.Fatal("wrong retained tool/model history", entries, err)
	}
}
func TestRevertRequiresStoppedWorkAndUserMessage(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	message, _, _ := s.BeginRun(ctx, "s", "r", "m", "text")
	if _, err := s.Revert(ctx, "s", "m", message.Sequence); !errors.Is(err, ErrBusy) {
		t.Fatal("active run reverted", err)
	}
	s.FinishRun(ctx, "s", "r", "a", "succeeded", "answer")
	if target, err := s.RevertTarget(ctx, "s", "m"); err != nil || target.ID != message.ID {
		t.Fatal("user message unavailable for conversation rollback", target, err)
	}
	for _, target := range []struct{ session, message string }{{"s", "a"}, {"other-session", "m"}} {
		if _, err := s.RevertTarget(ctx, target.session, target.message); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("invalid rollback target accepted", target, err)
		}
	}
	if _, err := s.Revert(ctx, "s", "m", message.Sequence); err != nil {
		t.Fatal("stopped conversation could not be rolled back", err)
	}
}

func TestRevertRestoresPendingQuestionChecklistAndRetainedTaskBoundary(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginRun(ctx, "s", "first", "first-user", "investigate"); err != nil {
		t.Fatal(err)
	}
	task := workerproto.Result{RunID: "first", Kind: "agent_task", TaskID: "child", ParentTaskID: "main", Depth: 1,
		Task: &workerproto.AgentTask{ID: "child", ParentID: "main", Join: "detached", Status: "started"}}
	if event, err := s.RecordAgentTask(ctx, "s", task); err != nil || event == nil {
		t.Fatal("task registration", event, err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "first", []ChecklistItem{{Text: "retained step", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	questions := []question.Question{{Question: "Which path?", Options: []question.Option{{Label: "src"}, {Label: "web"}}}}
	pending := &PendingReply{CallID: "ask", Kind: "question", Questions: questions, Content: question.Format(questions)}
	if _, err := s.FinishRunWithPending(ctx, "s", "first", "question", "succeeded", pending.Content, pending); err != nil {
		t.Fatal(err)
	}
	target, _, err := s.BeginRun(ctx, "s", "second", "second-user", "inspect src")
	if err != nil {
		t.Fatal(err)
	}
	task.Task.Status, task.Task.Report = "succeeded", &workerproto.OutputRef{RunID: "first", Turn: 2, Bytes: 12}
	if event, err := s.RecordAgentTask(ctx, "s", task); err != nil || event == nil {
		t.Fatal("late task report", event, err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "second", []ChecklistItem{{Text: "discarded step", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "s", "second", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx, "s", target.ID, target.Sequence); err != nil {
		t.Fatal(err)
	}
	session, err := s.Get(ctx, "s")
	if err != nil || session.Pending == nil || session.Pending.CallID != pending.CallID || session.Pending.RunID != "first" || session.Pending.Content != pending.Content || session.Running {
		t.Fatal("pending boundary was not restored", session, err)
	}
	list, err := s.RunChecklist(ctx, "s", "first")
	if err != nil || len(list) != 1 || list[0].Text != "retained step" || list[0].Status != "in_progress" {
		t.Fatal("retained checklist lost", list, err)
	}
	if list, err := s.RunChecklist(ctx, "s", "second"); err != nil || len(list) != 0 {
		t.Fatal("discarded checklist survived", list, err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT CAST(data AS BLOB) FROM agent_tasks WHERE run_id='first' AND task_id='child'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var restored workerproto.Result
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Task == nil || restored.Task.Status != "interrupted" || restored.Task.Report != nil {
		t.Fatal("discarded report survived", restored, err)
	}
	if event, err := s.RecordAgentTask(ctx, "s", task); err != nil || event != nil {
		t.Fatal("late task result resurrected discarded report", event, err)
	}
}

func TestRevertRejectsLateTitleFromWithdrawnFirstMessage(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	target, _, err := s.BeginRun(ctx, "s", "first", "first-user", "old topic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "s", "first", "first-answer", "succeeded", "old answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx, "s", target.ID, target.Sequence); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "s", "replacement", "replacement-user", "new topic"); err != nil {
		t.Fatal(err)
	}
	if event, err := s.SetGeneratedTitle(ctx, "s", "first-user", "Old topic"); err != nil || event != nil {
		t.Fatal("late title was accepted", event, err)
	}
	if event, err := s.SetGeneratedTitle(ctx, "s", "replacement-user", "New topic"); err != nil || event == nil {
		t.Fatal("replacement title was rejected", event, err)
	}
}
