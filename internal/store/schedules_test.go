package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"vista/internal/question"
	"vista/internal/schedule"
)

func newScheduledStore(t *testing.T) (*Store, Schedule) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	item, err := db.SaveSchedule(t.Context(), Schedule{Name: "Report", Prompt: "  Read the report.\n", Rule: schedule.Rule{Kind: "interval", Minutes: 5, Timezone: "UTC"}}, time.Now().Add(-5*time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return db, item
}

func beginScheduledStoreRun(t *testing.T, db *Store, item Schedule) (ScheduleRun, Message) {
	t.Helper()
	ctx, now := t.Context(), time.Now().UnixMilli()
	// A manual request and the timer see the same due slot. The timer wins
	// regardless of which actor reaches the Store transaction first.
	got := make(chan error, 2)
	go func() { got <- db.QueueScheduleNow(ctx, item.ID, "manual-key", now) }()
	go func() { _, err := db.StageSchedule(ctx, item.ID, now); got <- err }()
	busy := 0
	for range 2 {
		if err := <-got; errors.Is(err, ErrScheduleBusy) {
			busy++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if busy != 1 {
		t.Fatalf("manual/timer conflict: %d", busy)
	}
	_, runs, _, err := db.ScheduleWork(ctx, now)
	if err != nil || len(runs) != 1 {
		t.Fatalf("work: %+v %v", runs, err)
	}
	m, err := db.BeginScheduledRun(ctx, item, runs[0], "occurrence-session", "occurrence-run", "first-input", "my-model", now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != "user" || m.Content != item.Prompt || m.Sequence == 0 {
		t.Fatalf("not an ordinary intact user message: %+v", m)
	}
	if _, err := db.BeginScheduledRun(ctx, item, runs[0], "duplicate-session", "duplicate-run", "duplicate-message", "my-model", now); !errors.Is(err, ErrScheduleChanged) {
		t.Fatalf("duplicate launch: %v", err)
	}
	return runs[0], m
}

func TestScheduledReplyRebindsAndClosesOnlyAfterAnswer(t *testing.T) {
	db, item := newScheduledStore(t)
	run, m := beginScheduledStoreRun(t, db, item)
	ctx := t.Context()
	qs := []question.Question{{Question: "Which file?", Options: []question.Option{{Label: "First"}, {Label: "Second"}}}}
	pending := &PendingReply{CallID: "ask", Kind: "question", Content: question.Format(qs), Questions: qs}
	if _, err := db.FinishRunWithPending(ctx, m.SessionID, m.RunID, "question-message", "succeeded", pending.Content, pending); err != nil {
		t.Fatal(err)
	}
	snap, err := db.ScheduleList(ctx)
	if err != nil || len(snap) != 1 || snap[0].Enabled || snap[0].Prompt != "" || snap[0].Latest == nil || snap[0].Latest.State != "waiting_reply" {
		t.Fatalf("scheduled question snapshot: %+v %v", snap, err)
	}
	if err := db.SetScheduleEnabled(ctx, item.ID, snap[0].Revision, true, time.Now().UnixMilli()); !errors.Is(err, ErrScheduleBusy) {
		t.Fatalf("resumed unanswered task: %v", err)
	}
	session, err := db.Get(ctx, m.SessionID)
	if err != nil || !session.Open || session.Pending == nil {
		t.Fatalf("pending session not retained: %+v %v", session, err)
	}
	if _, _, err := db.BeginRun(ctx, m.SessionID, "answer-run", "answer", "First", "my-model"); err != nil {
		t.Fatal(err)
	}
	if got, err := db.ScheduledDeadline(ctx, "answer-run"); err != nil || got <= time.Now().UnixMilli() {
		t.Fatalf("answer deadline %d %v", got, err)
	}
	if _, err := db.FinishRun(ctx, m.SessionID, "answer-run", "answer-output", "succeeded", "Done"); err != nil {
		t.Fatal(err)
	}
	session, err = db.Get(ctx, m.SessionID)
	if err != nil || session.Open {
		t.Fatalf("terminal session is not closed: %+v %v", session, err)
	}
	if err := db.SetOpen(ctx, m.SessionID, true); !errors.Is(err, ErrBusy) {
		t.Fatalf("reopened before drain: %v", err)
	}
	if closed, err := db.BeginScheduleCleanup(ctx, run.ID, time.Now().UnixMilli()); err != nil || !closed {
		t.Fatalf("not draining: %v %v", closed, err)
	}
	if err := db.CompleteScheduleCleanup(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	snap, err = db.ScheduleList(ctx)
	if err != nil || snap[0].Enabled || snap[0].Latest.State != "succeeded" {
		t.Fatalf("answer did not finish the same occurrence: %+v %v", snap, err)
	}
	if err := db.SetScheduleEnabled(ctx, item.ID, snap[0].Revision, true, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledRestartDoesNotResumeUnknownWork(t *testing.T) {
	db, item := newScheduledStore(t)
	run, m := beginScheduledStoreRun(t, db, item)
	ctx := t.Context()
	if _, err := db.Create(ctx, "normal", "Normal"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "normal", "normal-run", "normal-in", "Normal input", "my-model"); err != nil {
		t.Fatal(err)
	}
	if err := db.PrepareRestart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"normal": true, m.SessionID: false} {
		got, err := db.HasPendingRuntimeEvents(ctx, id)
		if err != nil || got != want {
			t.Fatalf("pending %s=%v (%v), want %v", id, got, err, want)
		}
	}
	history, _, err := db.ScheduleHistory(ctx, item.ID, 0)
	if err != nil || len(history) != 1 || history[0].State != "interrupted" || history[0].ID != run.ID {
		t.Fatalf("recovered history: %+v %v", history, err)
	}
	cur, err := db.Schedule(ctx, item.ID)
	if err != nil || cur.Enabled || cur.Blocked != "interrupted" {
		t.Fatalf("recovered schedule: %+v %v", cur, err)
	}
}

func TestScheduledManualAndNaturalHistory(t *testing.T) {
	db, item := newScheduledStore(t)
	ctx := t.Context()
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Minute)
	item.Rule = schedule.Rule{Kind: "once", At: future.Format("2006-01-02T15:04"), Timezone: "UTC"}
	item, err := db.SaveSchedule(ctx, item, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for range 2 {
		if err := db.QueueScheduleNow(ctx, item.ID, "same-request", now); err != nil {
			t.Fatal(err)
		}
	}
	history, _, err := db.ScheduleHistory(ctx, item.ID, 0)
	if err != nil || len(history) != 1 || !history[0].Manual {
		t.Fatalf("manual duplicate: %+v %v", history, err)
	}
	if err := db.StopScheduledRun(ctx, item.ID, history[0].ID, now); err != nil {
		t.Fatal(err)
	}
	cur, err := db.Schedule(ctx, item.ID)
	if err != nil || !cur.Enabled || cur.NextAt != item.NextAt {
		t.Fatalf("manual moved schedule: %+v %v", cur, err)
	}
	changed, err := db.StageSchedule(ctx, item.ID, future.Add(schedule.Grace+time.Millisecond).UnixMilli())
	if err != nil || !changed {
		t.Fatalf("stage expired: %v %v", changed, err)
	}
	history, _, err = db.ScheduleHistory(ctx, item.ID, 0)
	if err != nil || len(history) != 2 || history[0].State != "missed" {
		t.Fatalf("missed one-shot: %+v %v", history, err)
	}
	// A different Profile database never sees either the definitions or runs.
	other, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if rows, err := other.ScheduleList(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("profile leak: %+v %v", rows, err)
	}
}

func TestScheduledDeadlineClosesAndPausesWithoutRetry(t *testing.T) {
	db, item := newScheduledStore(t)
	run, m := beginScheduledStoreRun(t, db, item)
	ctx := t.Context()
	deadline, err := db.ScheduledDeadline(ctx, m.RunID)
	if err != nil || deadline == 0 {
		t.Fatalf("deadline %d: %v", deadline, err)
	}
	if closing, err := db.BeginScheduleCleanup(ctx, run.ID, deadline-1); err != nil || closing {
		t.Fatalf("early timeout %v %v", closing, err)
	}
	if closing, err := db.BeginScheduleCleanup(ctx, run.ID, deadline); err != nil || !closing {
		t.Fatalf("not stopped at deadline %v %v", closing, err)
	}
	items, err := db.ScheduleList(ctx)
	if err != nil || items[0].Enabled || items[0].Latest.State != "interrupted" || items[0].Latest.Reason != "timeout" {
		t.Fatalf("unexpected timed-out schedule %+v %v", items, err)
	}
	if event, err := db.FinishRun(ctx, m.SessionID, m.RunID, "late", "succeeded", "Late result"); err != nil || event != nil {
		t.Fatalf("late result won after timeout %v %v", event, err)
	}
}
