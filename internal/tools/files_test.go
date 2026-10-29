package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestLargeFilesRemainReadableAndSearchable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	for i := 0; i < 100000; i++ {
		fmt.Fprintf(writer, "row %d keeps original bytes\n", i+1)
	}
	io.WriteString(writer, "needle near end\n")
	file.Close()
	selected, err := os.CreateTemp(t.TempDir(), "selection")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	page, err := ReadLines(ctx, path, 100000, 10, 50000, selected)
	if err != nil || page.TotalLines != 100002 || page.EndLine != 100002 || page.SHA256 != hex.EncodeToString(hash.Sum(nil)) || page.Bytes < 1<<20 {
		t.Fatalf("large read: %+v %v", page, err)
	}
	selected.Seek(0, io.SeekStart)
	content, _ := io.ReadAll(selected)
	if !strings.Contains(string(content), "100001: needle near end") {
		t.Fatal("large-file tail missing")
	}
	matches, err := Search(ctx, root, "needle near end")
	if err != nil || matches.Truncated || len(matches.Matches) != 1 || matches.Matches[0].Line != 100001 {
		t.Fatalf("large search: %+v %v", matches, err)
	}
}

func TestSearchLargeSingleLineAndScanBudget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 32<<10)
	for i := 0; i < 40; i++ {
		io.WriteString(f, chunk)
	}
	io.WriteString(f, "tail needle")
	f.Close()
	result, err := Search(context.Background(), root, "tail needle$")
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Line != 1 || len(result.Matches[0].Text) > 512 {
		t.Fatalf("long-line search: %+v %v", result, err)
	}
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1024; i++ {
		io.WriteString(f, chunk)
	}
	f.Close()
	result, err = Search(context.Background(), root, "tail needle")
	if err != nil || !result.Truncated || result.FilesScanned != 1 {
		t.Fatalf("scan budget unreported: %+v %v", result, err)
	}
}

func TestLargeFileLineDigestSupportsConflictCheckedReplacement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	original := strings.Repeat("证据🙂\n", 200000)
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(dir, "page-*")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	page, err := ReadLines(ctx, path, 2, 1, 10000, output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(ctx, dir, path, page.SHA256, "replacement"); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(ctx, dir, path, page.SHA256, "stale replacement"); !errors.Is(err, ErrConflict) {
		t.Fatalf("missed conflict: %v", err)
	}
}

func TestSearchBudgetKeepsCompleteMatchesWithoutInventingLineEndings(t *testing.T) {
	for _, character := range []string{"x", "中"} {
		t.Run(character, func(t *testing.T) {
			root := t.TempDir()
			file, err := os.Create(filepath.Join(root, "large.txt"))
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(file, "needle\n")
			chunk := strings.Repeat(character, 1000)
			for n := 0; n <= maxSearchBytes/len(chunk); n++ {
				io.WriteString(file, chunk)
			}
			file.Close()
			result, err := Search(context.Background(), root, "needle|"+character+"$")
			if err != nil || !result.Truncated || len(result.Matches) != 1 || result.Matches[0].Line != 1 {
				t.Fatalf("budget boundary corrupted results: %+v %v", result, err)
			}
		})
	}
}
