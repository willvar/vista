package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFullModelOutputSnapshotKeepsUnicodeAndSessionOwnership(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "full-output.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, session := range []string{"session", "other"} {
		if _, err := s.Create(ctx, session, "output"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "input", "hello"); err != nil {
		t.Fatal(err)
	}
	fragment := strings.Repeat("标题🙂\n", 180)
	content := strings.Repeat(fragment, 8)
	for range 8 {
		if event, err := s.AppendModelDelta(ctx, "session", "run", 300, 0, fragment); err != nil || event == nil {
			t.Fatalf("append output: event=%+v err=%v", event, err)
		}
	}
	page, err := s.ReadFullModelOutput(ctx, "session", "run", 300)
	if err != nil || page.Offset != 0 || page.NextOffset != int64(len(content)) || page.AvailableBytes != int64(len(content)) || page.Content != content || !utf8.ValidString(page.Content) || page.Complete {
		t.Fatalf("running snapshot: bytes=%d next=%d complete=%t err=%v", len(page.Content), page.NextOffset, page.Complete, err)
	}
	if _, err := s.AppendModelDelta(ctx, "session", "run", 300, 0, "完成\n"); err != nil {
		t.Fatal(err)
	}
	// A previous snapshot remains the exact content saved at its read; the next
	// read includes later fragments and completion independently.
	if page.Content != content || page.AvailableBytes != int64(len(content)) {
		t.Fatal("later writes changed the existing output snapshot")
	}
	content += "完成\n"
	if _, err := s.FinishRun(ctx, "session", "run", "answer", "succeeded", "preview", ModelOutputRef{Turn: 300, Bytes: len(content)}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReadFullModelOutput(ctx, "session", "run", 300)
	if err != nil || !page.Complete || page.Content != content || page.NextOffset != page.AvailableBytes || page.AvailableBytes != int64(len(content)) {
		t.Fatalf("completed snapshot: bytes=%d available=%d complete=%t err=%v", len(page.Content), page.AvailableBytes, page.Complete, err)
	}
	for _, input := range []struct{ session, run string }{{"other", "run"}, {"session", "missing"}} {
		if _, err := s.ReadFullModelOutput(ctx, input.session, input.run, 300); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unowned output %v: %v", input, err)
		}
	}
	for _, turn := range []int32{0, -1} {
		if _, err := s.ReadFullModelOutput(ctx, "session", "run", turn); !errors.Is(err, ErrModelOutputRange) {
			t.Fatalf("invalid turn %d: %v", turn, err)
		}
	}
	defaultPage, err := s.ReadModelOutput(ctx, "session", "run", 300, 0, 4096)
	if err != nil || defaultPage.NextOffset >= defaultPage.AvailableBytes || len(defaultPage.Content) > 4096 || !utf8.ValidString(defaultPage.Content) {
		t.Fatalf("default paging changed: bytes=%d next=%d err=%v", len(defaultPage.Content), defaultPage.NextOffset, err)
	}
}
