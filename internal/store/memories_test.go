package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"vista/internal/workerproto"
)

func TestMemoriesPersistIndependentlyOfSessionsAndRejectStaleEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, second := uuid.NewString(), uuid.NewString()
	for _, change := range []workerproto.MemoryChange{
		{Action: "add", ID: first, Text: "  回答简明。  "},
		{Action: "add", ID: second, Text: "Use metric units."},
		{Action: "update", ID: first, PreviousText: "回答简明。", Text: "回答简明，并提供依据。"},
	} {
		if err := db.ChangeMemory(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"update", "remove"} {
		text := "stale overwrite"
		if action == "remove" {
			text = ""
		}
		if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: action, ID: first, PreviousText: "回答简明。", Text: text}); !errors.Is(err, ErrMemoryChanged) {
			t.Fatalf("stale %s: %v", action, err)
		}
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "Use metric units."}); !errors.Is(err, ErrMemoryExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := db.Create(ctx, "disposable", "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(ctx, "disposable"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items, err := db.Memories(ctx)
	if err != nil || len(items) != 2 || items[0].ID != first || items[0].Text != "回答简明，并提供依据。" || items[1].ID != second {
		t.Fatalf("reopened memories: %+v %v", items, err)
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "remove", ID: first, PreviousText: items[0].Text}); err != nil {
		t.Fatal(err)
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "update", ID: first, Text: "resurrect", PreviousText: items[0].Text}); !errors.Is(err, ErrMemoryChanged) {
		t.Fatalf("deleted memory resurrected: %v", err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	remaining, err := reader.Memories(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ID != second {
		t.Fatalf("read-only memories: %+v %v", remaining, err)
	}
	if err := reader.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "must not write"}); err == nil {
		t.Fatal("read-only Worker wrote a memory")
	}
}

func TestMemoryConcurrentChangesAndLimits(t *testing.T) {
	db, ctx := executionStore(t)
	id := uuid.NewString()
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: id, Text: "old"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, text := range []string{"from UI", "from Agent"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			results <- db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "update", ID: id, PreviousText: "old", Text: text})
		}(text)
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrMemoryChanged) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent edits: %d succeeded, %d conflicted", succeeded, conflicted)
	}
	for _, text := range []string{"", "  ", strings.Repeat("中", MaxMemoryBytes/3+1), "bad\x00text", string([]byte{0xff})} {
		if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: text}); !errors.Is(err, ErrInvalidMemory) {
			t.Fatalf("invalid text accepted: %v", err)
		}
	}
	for i := 0; i < 7; i++ {
		text := strings.Repeat(string(rune('a'+i)), MaxMemoryBytes)
		if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: strings.Repeat("z", MaxMemoryBytes)}); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("total byte limit: %v", err)
	}
}

func TestRunMemoryAndAuditCommitTogetherOnlyWhileActive(t *testing.T) {
	db, ctx := executionStore(t)
	change := workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "Use SI units."}
	event, err := db.ChangeRunMemory(ctx, "session", "run", change)
	if err != nil || event == nil || event.Kind != "memory_changed" {
		t.Fatalf("commit: %+v %v", event, err)
	}
	if _, err := db.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	change.Action, change.PreviousText, change.Text = "update", change.Text, "late update"
	if _, err := db.ChangeRunMemory(ctx, "session", "run", change); err == nil {
		t.Fatal("late memory changed")
	}
	items, err := db.Memories(ctx)
	if err != nil || len(items) != 1 || items[0].Text != "Use SI units." {
		t.Fatalf("late result changed saved memory: %+v %v", items, err)
	}
}
