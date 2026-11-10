package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchSkipsUnreadableSubtreesAndPreservesIgnoreRules(t *testing.T) {
	for _, kind := range []string{"directory", "ignore-file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeSearchFixture(t, root, "project/main.go", "needle\n")
			writeSearchFixture(t, root, "blocked/.gitignore", "hidden.go\n")
			writeSearchFixture(t, root, "blocked/hidden.go", "needle\n")
			blocked := filepath.Join(root, "blocked")
			locked := blocked
			if kind == "ignore-file" {
				locked = filepath.Join(blocked, ".gitignore")
			}
			if err := os.Chmod(locked, 0); err != nil {
				t.Skipf("cannot restrict fixture access: %v", err)
			}
			t.Cleanup(func() { os.Chmod(locked, 0700) })
			var accessErr error
			if kind == "directory" {
				_, accessErr = os.ReadDir(locked)
			} else {
				_, accessErr = os.ReadFile(locked)
			}
			if !errors.Is(accessErr, os.ErrPermission) {
				t.Skip("fixture permissions are not enforced for this user/platform")
			}
			found, err := FindFiles(context.Background(), root, "*.go", nil, 100)
			if err != nil || len(found.Paths) != 1 || found.Paths[0] != filepath.Join(root, "project", "main.go") || !found.Truncated || found.SkippedDirectories != 1 || len(found.Warnings) != 1 || found.Warnings[0].Path != blocked {
				t.Fatalf("unreadable subtree aborted discovery or bypassed ignores: %+v %v", found, err)
			}
			searched, err := SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "needle", Include: []string{"*.go"}})
			if err != nil || searched.Count != 1 || !searched.Truncated || searched.SkippedDirectories != 1 || len(searched.Warnings) != 1 {
				t.Fatalf("partial content search was not explicit: %+v %v", searched, err)
			}
			// A concrete prefix avoids the inaccessible branch altogether.
			found, err = FindFiles(context.Background(), root, "project/main.go", nil, 100)
			if err != nil || len(found.Paths) != 1 || found.Truncated || found.SkippedDirectories != 0 {
				t.Fatalf("literal-prefix pruning failed: %+v %v", found, err)
			}
			if _, err := FindFiles(context.Background(), blocked, "*.go", nil, 100); err == nil {
				t.Fatal("an explicitly requested inaccessible root must fail")
			}
		})
	}
}

func TestSearchPrefixPruningPreservesGlobSemantics(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"src/a.go", "src/deep/b.go", "other/c.go", "src/[literal]/d.go"} {
		writeSearchFixture(t, root, path, "needle\n")
	}
	for pattern, count := range map[string]int{
		"*.go": 4, "**/*.go": 4, "src/*.go": 1, "src/**/*.go": 3,
		"{src,other}/**/*.go": 4, `src/\[literal\]/*.go`: 1, "missing/file.go": 0,
	} {
		found, err := FindFiles(context.Background(), root, pattern, nil, 100)
		if err != nil || len(found.Paths) != count || found.Truncated {
			t.Fatalf("pattern %q changed meaning: %+v %v", pattern, found, err)
		}
	}
	result, err := SearchFiles(context.Background(), SearchOptions{Root: root, Pattern: "needle", Include: []string{"src/a.go", "other/*.go"}})
	if err != nil || result.Count != 2 || result.FilesScanned != 2 {
		t.Fatalf("multiple include prefixes lost results: %+v %v", result, err)
	}
}
