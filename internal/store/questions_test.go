package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"vista/internal/question"
)

func TestQuestionPersistenceAndAtomicAnswers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Create(ctx, "session", "question"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "session", "ask", "input", "start"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.QueueInput(ctx, "session", "queued", "later", "Follow-up after the question"); err != nil {
		t.Fatal(err)
	}
	questions := []question.Question{{Question: "Which path?", Options: []question.Option{{Label: "src", Description: "Source files"}, {Label: "web"}}}}
	pending := &PendingReply{CallID: "ask", Kind: "question", Content: question.Format(questions), Questions: questions}
	event, err := db.FinishRunWithPending(ctx, "session", "ask", "question", "succeeded", pending.Content, pending)
	if err != nil {
		t.Fatal(err)
	}
	var finished struct {
		Pending *PendingReply `json:"pendingReply"`
		Message Message       `json:"message"`
	}
	if err := json.Unmarshal(event.Data, &finished); err != nil || finished.Pending == nil || len(finished.Pending.Questions) != 1 || finished.Message.Content != pending.Content {
		t.Fatalf("bad question event: %+v %v", finished, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item, err := db.Get(ctx, "session")
	if err != nil || item.Pending == nil || item.Pending.CallID != pending.CallID || item.Pending.Content != pending.Content || item.Pending.RunID != "ask" || len(item.Pending.Questions) != 1 {
		t.Fatalf("pending did not reload: %+v %v", item, err)
	}
	if input, err := db.NextQueuedInput(ctx, "session"); err != nil || input != nil {
		t.Fatalf("pending question let a queued input bypass it: %+v %v", input, err)
	}
	if _, err := db.BeginQueuedRun(ctx, "session", "queued", "queue-run", "queue-message"); !errors.Is(err, ErrBusy) {
		t.Fatalf("queued input bypassed question transaction guard: %v", err)
	}
	answer := question.Reply{RunID: "ask", Answers: []question.Answer{{Options: []string{"src"}}}}
	invalid := answer
	invalid.Answers = []question.Answer{{Options: []string{"missing"}}}
	if _, _, err := db.BeginQuestionRun(ctx, "session", "bad", "bad", invalid); !errors.Is(err, ErrInvalidQuestionReply) {
		t.Fatalf("invalid: %v", err)
	}
	stale := answer
	stale.RunID = "old"
	if _, _, err := db.BeginQuestionRun(ctx, "session", "stale", "stale", stale); !errors.Is(err, ErrNoPendingQuestion) {
		t.Fatalf("stale: %v", err)
	}
	// A persistence failure after clearing pending must roll the whole transaction back.
	if _, _, err := db.BeginQuestionRun(ctx, "session", "ask", "duplicate-id", answer); err == nil {
		t.Fatal("duplicate run unexpectedly succeeded")
	}
	item, err = db.Get(ctx, "session")
	if err != nil || item.Pending == nil || item.Pending.RunID != "ask" {
		t.Fatalf("failed answers consumed pending: %+v %v", item, err)
	}
	message, answerEvent, err := db.BeginQuestionRun(ctx, "session", "answer", "answer-input", answer)
	if err != nil || message.QuestionCallID != pending.CallID || message.Content != "1. Which path?\n\nsrc" {
		t.Fatalf("answer: %+v %v", message, err)
	}
	var saved Message
	if err := json.Unmarshal(answerEvent.Data, &saved); err != nil || saved.QuestionCallID != pending.CallID {
		t.Fatalf("answer event lost tool identity: %+v %v", saved, err)
	}
	entries, err := db.ContextEntries(ctx, "session", nil, 0)
	if err != nil || len(entries) != 3 || entries[2].Message.QuestionCallID != pending.CallID {
		t.Fatalf("restored answer lost tool identity: %+v %v", entries, err)
	}
	if _, _, err := db.BeginQuestionRun(ctx, "session", "again", "again", answer); !errors.Is(err, ErrNoPendingQuestion) {
		t.Fatalf("double answer: %v", err)
	}
	item, err = db.Get(ctx, "session")
	if err != nil || item.Pending != nil {
		t.Fatalf("accepted pending remains: %+v %v", item, err)
	}
}
