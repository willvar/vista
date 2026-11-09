package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectInstructionsFollowTargetPath(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, directory := range []string{left, right, filepath.Join(left, "nested")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "AGENTS.md"):           "root rule",
		filepath.Join(left, "CLAUDE.md"):           "left rule",
		filepath.Join(right, "AGENTS.md"):          "right rule",
		filepath.Join(left, "nested", "AGENTS.md"): "near rule",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := DiscoverProjectInstructions(context.Background(), filepath.Join(left, "nested", "file.txt"))
	if err != nil || len(files) != 3 {
		t.Fatalf("left instructions: %+v %v", files, err)
	}
	if files[0].Path != filepath.Join(root, "AGENTS.md") || files[1].Path != filepath.Join(left, "CLAUDE.md") || files[2].Path != filepath.Join(left, "nested", "AGENTS.md") {
		t.Fatalf("wrong precedence: %+v", files)
	}
	other, err := DiscoverProjectInstructions(context.Background(), filepath.Join(right, "file.txt"))
	if err != nil || len(other) != 2 || other[1].Path != filepath.Join(right, "AGENTS.md") {
		t.Fatalf("right instructions: %+v %v", other, err)
	}
	page, err := ReadProjectInstruction(files[2].Path, 0)
	hash := sha256.Sum256([]byte("near rule"))
	if err != nil || page.Content != "near rule" || files[2].SHA256 != hex.EncodeToString(hash[:]) || files[2].SHA256 != page.SHA256 {
		t.Fatalf("read instruction: %+v %v", page, err)
	}
}

func TestProjectInstructionDiscoveryCanBeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverProjectInstructions(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery: %v", err)
	}
}

func TestLongProjectInstructionsAreDiscoveredAndReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	content := strings.Repeat("Keep the full project rules. 中文。\n", 2000)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := DiscoverProjectInstructions(context.Background(), dir)
	if err != nil || len(files) == 0 || files[len(files)-1].Path != path || files[len(files)-1].Bytes != int64(len(content)) {
		t.Fatalf("long instructions were skipped: %+v %v", files, err)
	}
	var got strings.Builder
	for offset := 0; ; {
		page, err := ReadProjectInstruction(path, offset)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(page.Content)
		if !page.Truncated {
			break
		}
		offset = page.NextOffset
	}
	if got.String() != content {
		t.Fatal("paged instructions were not preserved")
	}
}

func TestInstructionDiscoverySharesAncestorsOnlyWithinOneOperation(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(outer, []byte("outer rules"), 0600); err != nil {
		t.Fatal(err)
	}
	var targets []string
	for i := range 48 {
		dir := filepath.Join(root, fmt.Sprint(i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("scoped rules"), 0600); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, filepath.Join(dir, "new.txt"))
	}
	targets = append(targets, targets[0])
	files, err := DiscoverProjectInstructions(context.Background(), targets...)
	if err != nil || len(files) != 49 || files[0].Path != outer {
		t.Fatalf("shared rule scopes or per-target limit changed: %+v %v", files, err)
	}
	if err := os.WriteFile(outer, []byte("changed rules"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := DiscoverProjectInstructions(context.Background(), targets...)
	if err != nil || len(next) != len(files) || next[0].SHA256 == files[0].SHA256 {
		t.Fatalf("cached rules survived their operation: %+v %v", next, err)
	}
}
