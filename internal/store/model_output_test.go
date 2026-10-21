package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestModelOutputAcceptsPositiveInt32TurnsAfter256(t *testing.T) {
	for _, turn := range []int32{257, 1<<31 - 1} {
		t.Run(strconv.FormatInt(int64(turn), 10), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const session = "00000000-0000-4000-8000-000000000001"
			if _, err := s.Create(ctx, session, "test"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.BeginRun(ctx, session, "run-1", "input-1", "hello"); err != nil {
				t.Fatal(err)
			}
			const content = "Reply after many model turns"
			if event, err := s.AppendModelDelta(ctx, session, "run-1", turn, 0, content); err != nil || event == nil {
				t.Fatalf("append turn %d: event=%+v err=%v", turn, event, err)
			}
			for _, invalid := range []int32{0, -1} {
				if _, err := s.AppendModelDelta(ctx, session, "run-1", invalid, 0, "bad turn"); err == nil {
					t.Fatalf("accepted non-positive turn %d", invalid)
				}
			}
			if _, err := s.AppendModelDelta(ctx, session, "run-1", turn, 0, strings.Repeat("x", 2049)); err == nil {
				t.Fatal("accepted fragment over the byte limit")
			}
			if event, err := s.FinishRun(ctx, session, "run-1", "answer-1", "succeeded", content, ModelOutputRef{Turn: turn, Bytes: len(content)}); err != nil || event == nil {
				t.Fatalf("finish turn %d: event=%+v err=%v", turn, event, err)
			}
			messages, err := s.MessagesBefore(ctx, session, 0, 10)
			if err != nil || len(messages) != 2 || messages[1].OutputTurn != turn || messages[1].OutputBytes != len(content) {
				t.Fatalf("persisted turn %d: messages=%+v err=%v", turn, messages, err)
			}
			page, err := s.ReadModelOutput(ctx, session, "run-1", turn, 0, 4096)
			if err != nil || page.Turn != turn || !page.Complete || page.Content != content {
				t.Fatalf("read turn %d: page=%+v err=%v", turn, page, err)
			}
			for _, invalid := range []int32{0, -1} {
				if _, err := s.ReadModelOutput(ctx, session, "run-1", invalid, 0, 4096); !errors.Is(err, ErrModelOutputRange) {
					t.Fatalf("read non-positive turn %d: %v", invalid, err)
				}
			}
		})
	}
}

func TestLongModelOutputPersistsByPageAndStaysInSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000001"
	const other = "00000000-0000-4000-8000-000000000002"
	for _, id := range []string{session, other} {
		if _, err := s.Create(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.BeginRun(ctx, session, "run-1", "input-1", "hello"); err != nil {
		t.Fatal(err)
	}
	fragment := strings.Repeat("甲", 500)
	full := strings.Repeat(fragment, 8)
	for range 8 {
		if event, err := s.AppendModelDelta(ctx, session, "run-1", 2, 0, fragment); err != nil || event == nil || event.Kind != "model_delta" {
			t.Fatalf("append model output: %+v %v", event, err)
		}
	}
	if _, err := s.FinishRun(ctx, session, "run-1", "answer-1", "succeeded", "preview", ModelOutputRef{Turn: 2, Bytes: len(full) - 1}); err == nil {
		t.Fatal("incomplete full output reference was accepted")
	}
	if _, err := s.FinishRun(ctx, session, "run-1", "answer-1", "succeeded", "preview", ModelOutputRef{Turn: 2, Bytes: len(full)}); err != nil {
		t.Fatal(err)
	}
	messages, err := s.MessagesBefore(ctx, session, 0, 10)
	if err != nil || len(messages) != 2 || messages[1].OutputTurn != 2 || messages[1].OutputBytes != len(full) {
		t.Fatalf("full answer metadata: %+v %v", messages, err)
	}
	if _, err := s.ReadModelOutput(ctx, other, "run-1", 2, 0, 4096); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other Session read model output: %v", err)
	}
	if _, err := s.ReadModelOutput(ctx, session, "run-1", 2, 1, 4096); !errors.Is(err, ErrModelOutputRange) {
		t.Fatalf("non-UTF-8 offset accepted: %v", err)
	}
	var read strings.Builder
	for offset := int64(0); ; {
		page, err := s.ReadModelOutput(ctx, session, "run-1", 2, offset, 4096)
		if err != nil || !utf8.ValidString(page.Content) || page.NextOffset <= offset || page.AvailableBytes != int64(len(full)) || !page.Complete {
			t.Fatalf("paged model output: %+v %v", page, err)
		}
		read.WriteString(page.Content)
		offset = page.NextOffset
		if offset == page.AvailableBytes {
			break
		}
	}
	if read.String() != full {
		t.Fatal("paged output differs from the model stream")
	}
	if err := s.Delete(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadModelOutput(ctx, session, "run-1", 2, 0, 4096); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Session retained model output: %v", err)
	}
}

func TestInterruptedModelOutputRemainsReadableAndPartial(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000001"
	if _, err := s.Create(ctx, session, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-1", "input-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendModelDelta(ctx, session, "run-1", 1, 0, "partial reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InterruptRunning(ctx, session, "Worker exited"); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadModelOutput(ctx, session, "run-1", 1, 0, 4096)
	if err != nil || page.Complete || page.Content != "partial reply" {
		t.Fatalf("interrupted output: %+v %v", page, err)
	}
	if event, err := s.AppendModelDelta(ctx, session, "run-1", 1, 0, "late text"); err != nil || event != nil {
		t.Fatalf("late model output appended: %+v %v", event, err)
	}
}
