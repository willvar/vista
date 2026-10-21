package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"vista/internal/workerproto"
)

func TestModelHistoryCommitsBeforeAckAndDeduplicatesFrames(t *testing.T) {
	consumer, sequence := newResultConsumerTest(t)
	result := workerproto.Result{RunID: "run", Turn: 1, TaskID: "main", Kind: "model_history", Status: "assistant", AckID: "history-ack",
		History: &workerproto.ModelHistory{Path: filepath.Join(consumer.stateDir, "outputs", "history.gob"), Tokens: 123}}
	acks := 0
	consumer.send = func(command workerproto.Command) error {
		acks++
		if command.Action != "ack" || command.AckID != "history-ack" {
			t.Fatalf("incorrect history ACK: %+v", command)
		}
		entries, err := consumer.store.ContextEntries(consumer.ctx, consumer.sessionID, nil, 0)
		if err != nil || len(entries) != 2 || entries[1].History == nil || entries[1].History.History.Path != result.History.Path || entries[1].History.AckID != "" {
			t.Fatalf("history acknowledged before durable commit: %+v %v", entries, err)
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := consumeTestResult(t, consumer, result); err != nil {
			t.Fatal(err)
		}
	}
	events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
	if err != nil || len(events) != 1 || acks != 2 {
		t.Fatalf("duplicate model history: events=%d ACKs=%d error=%v", len(events), acks, err)
	}
	paths, err := consumer.store.ArtifactPaths(consumer.ctx, consumer.sessionID)
	if err != nil || len(paths) != 1 || paths[0] != result.History.Path {
		t.Fatalf("session deletion would leak history files: %v %v", paths, err)
	}
	conflict := result
	conflict.History = &workerproto.ModelHistory{Path: filepath.Join(consumer.stateDir, "outputs", "other.gob"), Tokens: 123}
	if err := consumeTestResult(t, consumer, conflict); err == nil || acks != 2 {
		t.Fatalf("conflicting history was acknowledged: ACKs=%d error=%v", acks, err)
	}
	if _, err := consumer.store.FinishRun(consumer.ctx, consumer.sessionID, "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	result.Turn = 2
	if err := consumeTestResult(t, consumer, result); err == nil || acks != 2 {
		t.Fatalf("late model history was acknowledged: ACKs=%d error=%v", acks, err)
	}
}

func TestInvalidModelHistoryCannotChangeConversation(t *testing.T) {
	for _, name := range []string{"missing reference", "outside outputs", "missing ACK", "child", "missing turn", "negative index", "invalid part"} {
		t.Run(name, func(t *testing.T) {
			consumer, sequence := newResultConsumerTest(t)
			result := workerproto.Result{RunID: "run", Turn: 1, Kind: "model_history", Status: "assistant", AckID: "ack",
				History: &workerproto.ModelHistory{Path: filepath.Join(consumer.stateDir, "outputs", "history.gob"), Tokens: 123}}
			switch name {
			case "missing reference":
				result.History = nil
			case "outside outputs":
				result.History.Path = filepath.Join(consumer.stateDir, "vista.db")
			case "missing ACK":
				result.AckID = ""
			case "child":
				result.Depth, result.TaskID, result.ParentTaskID = 1, "main/child", "main"
			case "missing turn":
				result.Turn = 0
			case "negative index":
				result.History.Index = -1
			case "invalid part":
				result.Status = "succeeded"
			}
			consumer.send = func(workerproto.Command) error { t.Fatal("invalid history acknowledged"); return nil }
			frame, _ := json.Marshal(result)
			if err := consumer.consumeFrame(frame); err == nil {
				t.Fatal("invalid history accepted")
			}
			events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("invalid history persisted: events=%d error=%v", len(events), err)
			}
		})
	}
}
