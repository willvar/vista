package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOptimisticFileWrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	path := filepath.Join(root, "note.txt")
	if _, err := WriteFile(ctx, state, path, "missing", "first"); err != nil {
		t.Fatal(err)
	}
	initial, err := ReadFile(path)
	if err != nil || initial.Content != "first" {
		t.Fatalf("read after create: %+v, %v", initial, err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for _, content := range []string{"second", "third"} {
		go func(content string) {
			defer wg.Done()
			_, err := WriteFile(ctx, state, path, initial.SHA256, content)
			results <- err
		}(content)
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected one write and one conflict, got %d and %d", success, conflict)
	}
	current, err := ReadFile(path)
	if err != nil || (current.Content != "second" && current.Content != "third") {
		t.Fatalf("unexpected winner: %+v, %v", current, err)
	}
	if _, err := WriteFile(ctx, state, path, initial.SHA256, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write was accepted: %v", err)
	}
	if _, err := WriteFile(ctx, state, "relative.txt", "missing", "bad"); err == nil {
		t.Fatal("relative path was accepted")
	}
}

func TestSearchExplicitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nmatch here\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "hidden"), []byte("match hidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Search(context.Background(), root, "match")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Line != 2 || result.Matches[0].Path != filepath.Join(root, "a.txt") {
		t.Fatalf("unexpected matches: %+v", result)
	}
	if _, err := Search(context.Background(), ".", "match"); err == nil {
		t.Fatal("relative search root was accepted")
	}
}
