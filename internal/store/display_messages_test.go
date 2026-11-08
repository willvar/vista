package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDisplayMessagesProjectCompactionWithoutChangingCanonicalHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(ctx, "s", "Compaction"); err != nil {
		t.Fatal(err)
	}
	input, _, err := s.BeginRun(ctx, "s", "r", "u", "original task")
	if err != nil {
		t.Fatal(err)
	}
	notice, err := s.AppendRunEvent(ctx, "s", "r", "context_notice", map[string]any{
		"runId": "r", "depth": 0, "summary": "## Findings\n\nThe original evidence is retained.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []map[string]any{
		{"runId": "r", "depth": 1, "summary": "child summary"},
		{"runId": "r", "depth": 0, "summary": "   "},
	} {
		if _, err := s.AppendRunEvent(ctx, "s", "r", "context_notice", data); err != nil {
			t.Fatal(err)
		}
	}
	// The summary must survive snapshots even after leaving the recent event page.
	for i := 0; i < 110; i++ {
		if _, err := s.AppendModelDelta(ctx, "s", "r", 1, 1, "child progress"); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := s.FinishRun(ctx, "s", "r", "a", "succeeded", "final answer")
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ConversationSnapshot(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 3 || view.Events[0].Sequence <= notice.Sequence {
		t.Fatalf("summary was not restored independently of recent events: %+v", view)
	}
	summary := view.Messages[1]
	if !summary.ContextSummary || summary.ID != fmt.Sprintf("context-summary/%d", notice.Sequence) || summary.Role != "assistant" || summary.Sequence != notice.Sequence || summary.RunID != "r" || summary.CreatedAt != notice.CreatedAt || summary.Content != "## Findings\n\nThe original evidence is retained." {
		t.Fatalf("incorrect summary projection: %+v", summary)
	}
	canonical, err := s.MessagesBefore(ctx, "s", 0, 100)
	if err != nil || len(canonical) != 2 || canonical[0].ID != input.ID || canonical[1].ID != "a" {
		t.Fatalf("display changed model history: %+v err=%v", canonical, err)
	}
	if view.Session.LatestReplySequence != finished.Sequence {
		t.Fatal("summary changed the authoritative reply sequence")
	}
	// A new reader, including a restarted Server, derives the exact same message.
	reopened, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.DisplayMessagesBefore(ctx, "s", 0, 100)
	if err != nil || !reflect.DeepEqual(restored, view.Messages) {
		t.Fatalf("restored messages differ: %+v err=%v", restored, err)
	}
	// Paging in either direction uses the same event cursor, including a page
	// containing only a summary between the input and final answer.
	var forward []Message
	var cursor int64
	for {
		page, err := s.DisplayMessagesAfter(ctx, "s", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if page[0].Sequence <= cursor {
			t.Fatal("forward cursor did not advance")
		}
		forward = append(forward, page...)
		cursor = page[0].Sequence
	}
	if !reflect.DeepEqual(forward, view.Messages) {
		t.Fatalf("forward pages lost or duplicated a summary: %+v", forward)
	}
	var backward []Message
	cursor = 0
	for {
		page, err := s.DisplayMessagesBefore(ctx, "s", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		backward = append(page, backward...)
		cursor = page[0].Sequence
	}
	if !reflect.DeepEqual(backward, view.Messages) {
		t.Fatalf("backward pages lost or duplicated a summary: %+v", backward)
	}
	if _, err := s.Revert(ctx, "s", "u", input.Sequence); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.DisplayMessagesBefore(ctx, "s", 0, 100)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("rollback retained a discarded summary: %+v err=%v", remaining, err)
	}
}
