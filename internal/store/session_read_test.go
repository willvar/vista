package store

import (
	"context"
	"path/filepath"
	"testing"

	"vista/internal/question"
)

func TestSessionListSummarizesRepliesQueueAndLatestRun(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"question", "failed", "empty"} {
		if _, err := s.Create(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.BeginRun(ctx, id, id+"-run", id+"-user", "work"); err != nil {
			t.Fatal(err)
		}
	}
	questions := []question.Question{{Question: "Which?", Options: []question.Option{{Label: "A"}}}}
	pending := &PendingReply{CallID: "ask", Kind: "question", Content: question.Format(questions), Questions: questions}
	if _, err := s.FinishRunWithPending(ctx, "question", "question-run", "question-reply", "succeeded", pending.Content, pending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, "failed", "queued", "later", "follow-up"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"failed", "empty"} {
		if _, err := s.FinishRun(ctx, id, id+"-run", id+"-reply", "failed", "provider error"); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Session)
	for _, item := range items {
		byID[item.ID] = item
	}
	if q := byID["question"]; q.Pending == nil || q.LatestReplySequence == 0 || q.LatestRunStatus != "succeeded" || q.ReadSequence != 0 {
		t.Fatalf("missing pending question/reply: %+v", q)
	}
	if f := byID["failed"]; f.LatestRunStatus != "failed" || !f.InputQueuePaused || f.PendingInputCount != 1 {
		t.Fatalf("missing failed run/paused input: %+v", f)
	}
	if e := byID["empty"]; e.InputQueuePaused || e.PendingInputCount != 0 {
		t.Fatalf("empty queue should not be paused: %+v", e)
	}
	// Viewing a question does not answer it or resume its queue.
	if _, err := s.MarkRead(ctx, "question", byID["question"].LatestReplySequence); err != nil {
		t.Fatal(err)
	}
	q, err := s.Get(ctx, "question")
	if err != nil || q.Pending == nil {
		t.Fatalf("read consumed question: %+v %v", q, err)
	}
	if _, _, err := s.BeginRun(ctx, "failed", "retry", "retry-user", "try again"); err != nil {
		t.Fatal(err)
	}
	items, err = s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "failed" && (item.LatestRunStatus != "running" || !item.Executing) {
			t.Fatalf("historical failure survived a new run: %+v", item)
		}
	}
}

func TestReadPositionIsBoundedMonotonicAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	ctx := context.Background()
	if _, err := s.Create(ctx, "session", "work"); err != nil {
		t.Fatal(err)
	}
	finish := func(id string) int64 {
		t.Helper()
		if _, _, err := s.BeginRun(ctx, "session", id, id+"-user", "work"); err != nil {
			t.Fatal(err)
		}
		event, err := s.FinishRun(ctx, "session", id, id+"-reply", "succeeded", "answer")
		if err != nil {
			t.Fatal(err)
		}
		return event.Sequence
	}
	first := finish("first")
	// A guessed future sequence cannot pre-acknowledge later replies.
	if sequence, err := s.MarkRead(ctx, "session", first+1000); err != nil || sequence != first {
		t.Fatalf("unbounded read: %d %v", sequence, err)
	}
	second := finish("second")
	if sequence, err := s.MarkRead(ctx, "session", first); err != nil || sequence != first {
		t.Fatalf("older viewport consumed new reply: %d %v", sequence, err)
	}
	items, err := s.List(ctx)
	if err != nil || items[0].LatestReplySequence != second || items[0].ReadSequence != first {
		t.Fatalf("new reply should remain unread: %+v %v", items, err)
	}
	if _, err := s.MarkRead(ctx, "session", second); err != nil {
		t.Fatal(err)
	}
	if sequence, err := s.MarkRead(ctx, "session", first); err != nil || sequence != second {
		t.Fatalf("out-of-order read moved backward: %d %v", sequence, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx, "session")
	if err != nil || item.ReadSequence != second {
		t.Fatalf("read position lost on reopen: %+v %v", item, err)
	}
}
