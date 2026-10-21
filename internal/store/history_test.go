package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestHistoryIndexAndReadStayWithinSession(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const first = "00000000-0000-4000-8000-000000000008"
	const second = "00000000-0000-4000-8000-000000000009"
	for _, id := range []string{first, second} {
		if _, err := s.Create(ctx, id, "history"); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct{ session, run, message, text string }{{first, "run-1", "message-1", "first old constraint"}, {second, "run-2", "message-2", "private other Session"}, {first, "run-3", "message-3", "current prompt"}} {
		if _, _, err := s.BeginRun(ctx, entry.session, entry.run, entry.message, entry.text); err != nil {
			t.Fatal(err)
		}
		if entry.run != "run-3" {
			if _, err := s.FinishRun(ctx, entry.session, entry.run, "answer-"+entry.run, "succeeded", "done"); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, err := s.HistoryIndex(ctx, first, 3, 10)
	if err != nil || len(items) != 2 || items[0].Role != "assistant" || items[1].Preview != "first old constraint" {
		t.Fatalf("wrong history index: %+v %v", items, err)
	}
	message, err := s.HistoryMessage(ctx, first, items[1].Sequence)
	if err != nil || message.Content != "first old constraint" {
		t.Fatalf("history message: %+v %v", message, err)
	}
	if _, err := s.HistoryMessage(ctx, first, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message: %v", err)
	}
	other, err := s.HistoryIndex(ctx, second, 100, 10)
	if err != nil || len(other) != 2 || other[1].Preview != "private other Session" {
		t.Fatalf("other Session history: %+v %v", other, err)
	}
}
