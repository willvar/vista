package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestManualCompactionExcludesOtherRunsAndInterruptsWithoutReply(t *testing.T) {
	for _, stop := range []string{"finish", "interrupt"} {
		t.Run(stop, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if _, err := db.Create(ctx, "session", "test"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "session", "run", "u", "question"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.FinishRun(ctx, "session", "run", "a", "succeeded", "answer"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginCompaction(ctx, "session", "compact"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "session", "other", "other-u", "busy"); !errors.Is(err, ErrBusy) {
				t.Fatalf("new run: %v", err)
			}
			if _, _, err := db.BeginCompaction(ctx, "session", "twice"); !errors.Is(err, ErrBusy) {
				t.Fatalf("duplicate compact: %v", err)
			}
			if _, _, err := db.QueueInputWithImages(ctx, "session", "queued", "now", "draft", nil); !errors.Is(err, ErrBusy) {
				t.Fatalf("interjection: %v", err)
			}
			if stop == "finish" {
				_, err = db.FinishRun(ctx, "session", "compact", "error-message", "failed", "fixture error")
			} else {
				_, err = db.InterruptRunning(ctx, "session", "Worker exited")
			}
			if err != nil {
				t.Fatal(err)
			}
			messages, err := db.MessagesBefore(ctx, "session", 0, 100)
			if err != nil || len(messages) != 2 {
				t.Fatalf("maintenance inserted reply: %+v %v", messages, err)
			}
			status, err := db.CompactionStatus(ctx, "session")
			if err != nil || status.Phase != "failed" || !status.Manual || status.Error == "" {
				t.Fatalf("status=%+v %v", status, err)
			}
			if _, _, err := db.BeginRun(ctx, "session", "next", "next-u", "continue"); err != nil {
				t.Fatalf("maintenance lock leaked: %v", err)
			}
		})
	}
}

func TestCompactionCommitIsDurableAcrossCancelAndDuplicateFinish(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	db.Create(ctx, "session", "test")
	u, _, err := db.BeginRun(ctx, "session", "run", "u", "task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishCompaction(ctx, "session", "run", "failed", "wrong kind"); err == nil {
		t.Fatal("ordinary run accepted as compaction")
	}
	db.FinishRun(ctx, "session", "run", "a", "succeeded", "done")
	through, _, err := db.BeginCompaction(ctx, "session", "compact")
	if err != nil {
		t.Fatal(err)
	}
	if through <= u.Sequence {
		t.Fatal("manual cutoff must include latest reply")
	}
	cp := workerproto.ContextCheckpoint{Path: "checkpoint.gob", ThroughSequence: through, InputTokens: 100}
	checkpointEvent, err := db.AppendRunEvent(ctx, "session", "compact", "context_notice", map[string]any{"runId": "compact", "depth": 0, "checkpoint": cp, "context": workerproto.CompactionStatus{RunID: "compact", Phase: "compacted", Manual: true}})
	if err != nil {
		t.Fatal(err)
	}
	cp.Sequence = checkpointEvent.Sequence
	if _, err := db.InterruptRunning(ctx, "session", "canceled after commit"); err != nil {
		t.Fatal(err)
	}
	if e, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil || e != nil {
		t.Fatalf("finish not idempotent: %+v %v", e, err)
	}
	status, err := db.CompactionStatus(ctx, "session")
	if err != nil || status.Phase != "compacted" || status.Error != "" {
		t.Fatalf("committed context lost: %+v %v", status, err)
	}
	saved, err := db.ContextCheckpoint(ctx, "session")
	if err != nil || *saved != cp {
		t.Fatalf("checkpoint=%+v %v", saved, err)
	}
}

func TestFailureMarkerSurvivesReloadAndDoesNotMarkEarlierReplies(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	db.Create(ctx, "session", "test")
	db.BeginRun(ctx, "session", "run", "u", "task")
	event, err := db.FinishRun(ctx, "session", "run", "failure", "failed", "a useful error")
	if err != nil || event == nil {
		t.Fatal(err)
	}
	for _, forward := range []bool{true, false} {
		var messages []Message
		if forward {
			messages, err = db.MessagesAfter(ctx, "session", 0, 100)
		} else {
			messages, err = db.MessagesBefore(ctx, "session", 0, 100)
		}
		if err != nil || len(messages) != 2 || messages[0].Failure || !messages[1].Failure {
			t.Fatalf("failure marker lost: %+v %v", messages, err)
		}
	}
}

func TestManualCompactionPreservesPausedInputs(t *testing.T) {
	for _, hasPending := range []bool{false, true} {
		t.Run(fmt.Sprint(hasPending), func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if _, err := db.Create(ctx, "session", "test"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "session", "run", "u", "task"); err != nil {
				t.Fatal(err)
			}
			if hasPending {
				if _, _, err := db.QueueInput(ctx, "session", "pending", "later", "another task"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.FinishRun(ctx, "session", "run", "a", "failed", "error"); err != nil {
				t.Fatal(err)
			}
			through, _, err := db.BeginCompaction(ctx, "session", "compact")
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.AppendRunEvent(ctx, "session", "compact", "context_notice", map[string]any{"runId": "compact", "depth": 0, "checkpoint": workerproto.ContextCheckpoint{Path: "checkpoint.gob", ThroughSequence: through}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			session, err := db.Get(ctx, "session")
			if err != nil || session.InputQueuePaused != hasPending {
				t.Fatalf("queue paused=%v want=%v err=%v", session.InputQueuePaused, hasPending, err)
			}
			if session.Executing {
				t.Fatal("manual compaction resumed execution")
			}
		})
	}
}
