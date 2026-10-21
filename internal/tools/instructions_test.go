package tools

import (
	"os"
	"path/filepath"
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
	files, err := DiscoverProjectInstructions(filepath.Join(left, "nested", "file.txt"))
	if err != nil || len(files) != 3 {
		t.Fatalf("left instructions: %+v %v", files, err)
	}
	if files[0].Path != filepath.Join(root, "AGENTS.md") || files[1].Path != filepath.Join(left, "CLAUDE.md") || files[2].Path != filepath.Join(left, "nested", "AGENTS.md") {
		t.Fatalf("wrong precedence: %+v", files)
	}
	other, err := DiscoverProjectInstructions(filepath.Join(right, "file.txt"))
	if err != nil || len(other) != 2 || other[1].Path != filepath.Join(right, "AGENTS.md") {
		t.Fatalf("right instructions: %+v %v", other, err)
	}
	page, err := ReadProjectInstruction(files[2].Path, 0)
	if err != nil || page.Content != "near rule" {
		t.Fatalf("read instruction: %+v %v", page, err)
	}
}
