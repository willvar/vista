package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func patchFixture(t *testing.T, files map[string]string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	hashes := map[string]string{}
	for name, text := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	}
	return root, hashes
}

func TestApplyPatchPreflightsAllFilesAndPreservesModes(t *testing.T) {
	root, hashes := patchFixture(t, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "three\n"})
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+changed\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-wrong\n+four\n"
	if result, err := ApplyPatch(context.Background(), t.TempDir(), PatchInput{root, patch, hashes}); err == nil || len(result.Applied) != 0 {
		t.Fatalf("preflight: %+v %v", result, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "one\ntwo\n" {
		t.Fatal("wrote before all hunks were validated")
	}
	patch = strings.Replace(patch, "-wrong", "-three", 1)
	result, err := ApplyPatch(context.Background(), t.TempDir(), PatchInput{root, patch, hashes})
	if err != nil || len(result.Files) != 2 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	for _, file := range result.Files {
		data, err := os.ReadFile(file.Path)
		info, _ := os.Stat(file.Path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != file.SHA256 || info.Mode().Perm() != 0644 || !strings.Contains(file.Diff.Unified, "+") {
			t.Fatalf("actual write diff/hash/mode: %+v %v", file, err)
		}
	}
	if _, err := ApplyPatch(context.Background(), t.TempDir(), PatchInput{root, patch, hashes}); err == nil {
		t.Fatal("stale hashes accepted")
	}
}

func TestApplyPatchCreateDeleteRenameAndNoFinalNewline(t *testing.T) {
	root, hashes := patchFixture(t, map[string]string{"delete.txt": "remove\n", "old.txt": "keep\n", "text.txt": "before"})
	hashes["new.txt"], hashes["moved.txt"] = "missing", "missing"
	patch := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+created\n--- a/delete.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-remove\ndiff --git a/old.txt b/moved.txt\nsimilarity index 100%\nrename from old.txt\nrename to moved.txt\ndiff --git a/text.txt b/text.txt\n--- a/text.txt\n+++ b/text.txt\n@@ -1 +1 @@\n-before\n\\ No newline at end of file\n+after\n\\ No newline at end of file\n"
	result, err := ApplyPatch(context.Background(), t.TempDir(), PatchInput{root, patch, hashes})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"new.txt": "created\n", "moved.txt": "keep\n", "text.txt": "after"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
	for _, name := range []string{"delete.txt", "old.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s retained", name)
		}
	}
	if len(result.Files) != 4 || !result.Files[0].Diff.Created || !result.Files[1].Deleted || result.Files[2].OldPath != filepath.Join(root, "old.txt") {
		t.Fatalf("metadata: %+v", result)
	}
}

func TestApplyPatchRejectsEscapesAndSharesWriteLock(t *testing.T) {
	root, hashes := patchFixture(t, map[string]string{"a.txt": "one\n"})
	state := t.TempDir()
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+two\n"
	release, err := lockFilePaths(context.Background(), state, []string{filepath.Join(root, "a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ApplyPatch(ctx, state, PatchInput{root, patch, hashes}); err == nil {
		t.Fatal("canceled patch succeeded")
	}
	release()
	for _, name := range []string{"../outside.txt", "/absolute.txt"} {
		bad := strings.ReplaceAll(patch, "a.txt", name)
		if _, err := ApplyPatch(context.Background(), state, PatchInput{root, bad, hashes}); err == nil {
			t.Fatalf("escaped path %s accepted", name)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err == nil {
		if _, err := PatchPaths(PatchInput{Directory: root, Patch: "--- /dev/null\n+++ b/link/new.txt\n@@ -0,0 +1 @@\n+bad\n"}); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}

func TestApplyPatchPreservesHeaderLikeContentAndLiteralGitPaths(t *testing.T) {
	root, hashes := patchFixture(t, map[string]string{"content.txt": "-- a/example\n", "a/nested.txt": "before\n"})
	patch := "--- a/content.txt\n+++ b/content.txt\n@@ -1 +1 @@\n--- a/example\n+++ b/example\ndiff --git a/a/nested.txt b/a/nested.txt\n--- a/a/nested.txt\n+++ b/a/nested.txt\n@@ -1 +1 @@\n-before\n+after\n"
	if _, err := ApplyPatch(context.Background(), t.TempDir(), PatchInput{root, patch, hashes}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"content.txt": "++ b/example\n", "a/nested.txt": "after\n"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
}

func TestPatchPartialRenameReportsOnlyActualWrites(t *testing.T) {
	root := t.TempDir()
	old, next := filepath.Join(root, "old"), filepath.Join(root, "new.txt")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "external-file"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := os.CreateTemp(t.TempDir(), "stage-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	stage.WriteString("committed\n")
	result, err := commitPatchOperations(context.Background(), []*patchOperation{{
		file: &gitdiff.File{}, old: old, next: next, staged: stage.Name(), mode: 0600,
		result: PatchFile{Path: next, OldPath: old, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("committed\n")))},
	}})
	if err == nil || len(result.Files) != 1 || len(result.Applied) != 1 || len(result.Unapplied) != 1 || result.Unapplied[0] != old || result.Applied[0] != next {
		t.Fatalf("partial result: %+v %v", result, err)
	}
	if result.Files[0].OldPath != "" || !result.Files[0].Diff.Created || !strings.Contains(result.Files[0].Diff.Unified, "--- /dev/null") {
		t.Fatalf("inaccurate committed diff: %+v", result.Files[0])
	}
	data, err := os.ReadFile(next)
	if err != nil || string(data) != "committed\n" {
		t.Fatal("committed destination lost")
	}
}

func TestFindAndContentSearchHaveDistinctFiltersAndIgnoreParity(t *testing.T) {
	root, _ := patchFixture(t, map[string]string{".gitignore": "hidden/\n*.tmp\n", "src/a.go": "zero\nNeedle.Value\nend\n", "src/b.go": "no match\n", "src/note.txt": "NeedleXValue\n", "hidden/a.go": "Needle.Value\n", "src/a.tmp": "Needle.Value\n"})
	found, err := FindFiles(context.Background(), root, "**/*.go", nil, 100)
	if err != nil || len(found.Paths) != 2 {
		t.Fatalf("find: %+v %v", found, err)
	}
	result, err := SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "needle.value", Literal: true, IgnoreCase: true, Include: []string{"**/*.go"}, ContextLines: 1})
	if err != nil || result.Count != 1 || len(result.Matches) != 3 || !result.Matches[0].Context || result.Matches[1].Context {
		t.Fatalf("content: %+v %v", result, err)
	}
	result, err = SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "Needle.Value", Literal: true, Mode: "files"})
	if err != nil || len(result.Paths) != 1 || len(result.Matches) != 0 {
		t.Fatalf("files: %+v %v", result, err)
	}
	result, err = SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "Needle.Value", Mode: "count", Exclude: []string{"*.txt"}})
	if err != nil || result.Count != 1 || len(result.Matches) != 0 {
		t.Fatalf("count: %+v %v", result, err)
	}
}

func TestSearchLongLinesStayBoundedWithoutLosingTailMatches(t *testing.T) {
	root := t.TempDir()
	// Both the matching tail and a UTF-8 rune lie beyond the preview buffer.
	text := strings.Repeat("x", 16<<20) + "命中🙂\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "命中🙂$"})
	runtime.ReadMemStats(&after)
	if err != nil || result.Count != 1 || len(result.Matches) != 1 || result.Matches[0].Line != 1 || result.Truncated {
		t.Fatalf("long-line tail was lost: %+v %v", result, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("search materialized a long line: allocated %d bytes", allocated)
	}
	for _, mode := range []string{"files", "count"} {
		result, err := SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "命中🙂", Literal: true, Mode: mode})
		if err != nil || result.Count != 1 || len(result.Matches) != 0 || mode == "files" && len(result.Paths) != 1 {
			t.Fatalf("long-line %s search: %+v %v", mode, result, err)
		}
	}
}

func TestPatchPathsReturnEachPhysicalTargetOnce(t *testing.T) {
	root := t.TempDir()
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-a\n+b\n"
	paths, err := PatchPaths(PatchInput{Directory: root, Patch: patch + patch})
	if err != nil || len(paths) != 1 || paths[0] != filepath.Join(root, "a.txt") {
		t.Fatalf("duplicate instruction-check targets: %+v %v", paths, err)
	}
}

func TestSearchOutputPagesRetainEveryHitAndOffsets(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	text := "prefix\n" + strings.Repeat("hit line🙂\ncontext\n", 100) + strings.Repeat("long ", 10000) + "hit at end\n"
	path := filepath.Join(state, "outputs", "test.log")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var offset int64
	seen := map[int64]bool{}
	hits := 0
	for {
		page, err := SearchOutput(context.Background(), state, path, offset, 512, "HIT", true, true, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range page.Matches {
			if match.Context {
				continue
			}
			if seen[match.Offset] {
				t.Fatalf("duplicate hit %d", match.Offset)
			}
			seen[match.Offset] = true
			hits++
			if match.NextOffset <= match.Offset {
				t.Fatal("invalid byte range")
			}
		}
		if page.NextOffset <= offset {
			t.Fatalf("no continuation progress: %+v", page)
		}
		offset = page.NextOffset
		if !page.Truncated {
			break
		}
	}
	if hits != 101 || offset != int64(len(text)) {
		t.Fatalf("hits=%d offset=%d/%d", hits, offset, len(text))
	}
}
