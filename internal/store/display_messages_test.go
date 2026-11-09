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
	if _, err := s.AppendRunEvent(ctx, "s", "r", "model_request", map[string]any{
		"runId": "r", "purpose": "summary", "key": "exact-summary-connection", "provider": "custom", "id": "model",
	}); err != nil {
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
	if summary.ModelKey != "exact-summary-connection" {
		t.Fatalf("summary lost its connection after its request left the event page: %+v", summary)
	}
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

func TestDisplayMessagesRetainFinalChecklistWithItsReplyAndPageCursor(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "interrupted"} {
		for _, text := range []string{"", "answer"} {
			t.Run(status+"/"+text, func(t *testing.T) {
				ctx := context.Background()
				s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if _, err := s.Create(ctx, "s", "Checklist"); err != nil {
					t.Fatal(err)
				}
				input, _, err := s.BeginRun(ctx, "s", "r", "input", "task")
				if err != nil {
					t.Fatal(err)
				}
				items := []ChecklistItem{{Text: "Done", Status: "done"}, {Text: "Still working", Status: "in_progress"}, {Text: "Blocked", Status: "blocked"}}
				updated, err := s.ReplaceChecklist(ctx, "s", "r", items)
				if err != nil {
					t.Fatal(err)
				}
				// Preserve the checklist time rather than the later reply time or
				// a child checklist's time, including for pre-existing histories.
				const checklistTime = "2026-11-06T04:51:38Z"
				if _, err := s.db.ExecContext(ctx, "UPDATE events SET created_at=? WHERE session_id=? AND sequence=?", checklistTime, "s", updated.Sequence); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendRunEvent(ctx, "s", "r", "checklist", map[string]any{"runId": "r", "depth": 1, "taskId": "child", "items": []ChecklistItem{{Text: "Child", Status: "done"}}}); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 110; i++ {
					if _, err := s.AppendModelDelta(ctx, "s", "r", 1, 1, "child progress"); err != nil {
						t.Fatal(err)
					}
				}
				finished, err := s.FinishRun(ctx, "s", "r", "answer", status, text)
				if err != nil {
					t.Fatal(err)
				}
				// Move the entire finished run off the recent event page.
				if _, _, err := s.BeginRun(ctx, "s", "next", "next-input", "next task"); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 110; i++ {
					if _, err := s.AppendModelDelta(ctx, "s", "next", 1, 1, "progress"); err != nil {
						t.Fatal(err)
					}
				}
				view, err := s.ConversationSnapshot(ctx, "s")
				if err != nil {
					t.Fatal(err)
				}
				if len(view.Messages) != 3 {
					t.Fatalf("unexpected messages: %+v", view.Messages)
				}
				final := view.Messages[1]
				if final.Sequence != finished.Sequence || final.RunID != "r" || final.Checklist == nil || final.Checklist.Status != status || !reflect.DeepEqual(final.Checklist.Items, items) {
					t.Fatalf("final checklist was lost or changed: %+v", final)
				}
				if final.Checklist.UpdatedAt != checklistTime {
					t.Fatalf("checklist timestamp = %q, want %q", final.Checklist.UpdatedAt, checklistTime)
				}
				if final.Checklist.UpdatedSequence != updated.Sequence {
					t.Fatalf("checklist display sequence = %d, want %d", final.Checklist.UpdatedSequence, updated.Sequence)
				}
				canonical, err := s.MessagesBefore(ctx, "s", 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				wantCanonical := 2
				if text != "" && status != "interrupted" {
					wantCanonical++
				}
				if len(canonical) != wantCanonical {
					t.Fatalf("checklist entered model history: %+v", canonical)
				}
				for _, message := range canonical {
					if message.Checklist != nil {
						t.Fatal("canonical message contains a display checklist")
					}
				}
				for _, forward := range []bool{false, true} {
					var paged []Message
					var cursor int64
					for {
						var page []Message
						if forward {
							page, err = s.DisplayMessagesAfter(ctx, "s", cursor, 1)
						} else {
							page, err = s.DisplayMessagesBefore(ctx, "s", cursor, 1)
						}
						if err != nil {
							t.Fatal(err)
						}
						if len(page) == 0 {
							break
						}
						if len(page) != 1 || page[0].Sequence == cursor {
							t.Fatal("cursor did not advance")
						}
						if forward {
							paged = append(paged, page...)
						} else {
							paged = append(page, paged...)
						}
						cursor = page[0].Sequence
					}
					if !reflect.DeepEqual(paged, view.Messages) {
						t.Fatalf("paging split or duplicated the final checklist: %+v", paged)
					}
				}
				if _, err := s.FinishRun(ctx, "s", "next", "next-answer", "succeeded", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Revert(ctx, "s", "input", input.Sequence); err != nil {
					t.Fatal(err)
				}
				remaining, err := s.DisplayMessagesBefore(ctx, "s", 0, 100)
				if err != nil || len(remaining) != 0 {
					t.Fatalf("withdrawal retained the checklist: %+v %v", remaining, err)
				}
			})
		}
	}
}

func TestDisplayMessagesOmitClearedChecklists(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(ctx, "s", "Checklist"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "s", "r", "input", "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "r", []ChecklistItem{{Text: "Plan", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceChecklist(ctx, "s", "r", []ChecklistItem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "s", "r", "answer", "succeeded", "answer"); err != nil {
		t.Fatal(err)
	}
	page, err := s.DisplayMessagesBefore(ctx, "s", 0, 100)
	if err != nil || len(page) != 2 || page[1].Checklist != nil {
		t.Fatalf("cleared checklist returned: %+v %v", page, err)
	}
}
