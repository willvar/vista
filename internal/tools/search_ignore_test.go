package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeSearchFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertSearchPaths(t *testing.T, root string, want ...string) {
	t.Helper()
	result, err := Search(context.Background(), root, "needle")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		name, err := filepath.Rel(root, match.Path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.ToSlash(name))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || result.Truncated {
		t.Fatalf("search paths = %v (truncated=%v), want %v", got, result.Truncated, want)
	}
}

func TestSearchGitignoreRules(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", "# comment\n*.log\n!keep.log\n/root-only.txt\n**/cache/\nassets/**/generated-?.txt\nblocked/\n!blocked/keep.txt\n\\#secret.txt\n\\!secret.txt\n")
	for _, name := range []string{
		"main.txt", "debug.log", "keep.log", "root-only.txt", "nested/root-only.txt",
		"nested/debug.log", "nested/cache/output.txt", "cache/output.txt",
		"assets/generated-a.txt", "assets/deep/generated-b.txt", "assets/deep/keep.txt",
		"blocked/keep.txt", "#secret.txt", "!secret.txt", "with spaces.txt",
	} {
		writeSearchFixture(t, root, name, "needle\n")
	}
	assertSearchPaths(t, root, "main.txt", "keep.log", "nested/root-only.txt", "assets/deep/keep.txt", "with spaces.txt")
}

func TestSearchNestedGitignoreAndSubdirectoryRoot(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", "*.log\n*.tmp\n")
	writeSearchFixture(t, root, "web/.gitignore", "!debug.log\n/local.txt\nbuild/*\n!build/keep.txt\n")
	writeSearchFixture(t, root, "web/nested/.gitignore", "!keep.tmp\n")
	for _, name := range []string{
		"web/main.txt", "web/debug.log", "web/local.txt", "web/build/drop.txt", "web/build/keep.txt",
		"web/nested/local.txt", "web/nested/keep.tmp", "web/nested/drop.tmp", "sibling/debug.log", "sibling/local.txt",
	} {
		writeSearchFixture(t, root, name, "needle\n")
	}
	assertSearchPaths(t, filepath.Join(root, "web"), "main.txt", "debug.log", "build/keep.txt", "nested/local.txt", "nested/keep.tmp")
	assertSearchPaths(t, root, "web/main.txt", "web/debug.log", "web/build/keep.txt", "web/nested/local.txt", "web/nested/keep.tmp", "sibling/local.txt")
}

func TestSearchGitignoreStopsAtRepositoryBoundary(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		t.Run(fmt.Sprint(worktree), func(t *testing.T) {
			root := t.TempDir()
			writeSearchFixture(t, root, ".gitignore", "*.txt\n")
			repo := filepath.Join(root, "repo")
			writeSearchFixture(t, repo, "src/main.txt", "needle\n")
			if worktree {
				writeSearchFixture(t, repo, ".git", "gitdir: ../worktrees/repo\n")
			} else if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			assertSearchPaths(t, filepath.Join(repo, "src"), "main.txt")
		})
	}
}

func TestSearchGitignoreExcludedAncestor(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", "blocked/\n!blocked/nested/keep.txt\n")
	writeSearchFixture(t, root, "blocked/nested/.gitignore", "!keep.txt\n")
	writeSearchFixture(t, root, "blocked/nested/keep.txt", "needle\n")
	assertSearchPaths(t, filepath.Join(root, "blocked", "nested"))
}

func TestSearchReloadsGitignore(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "main.txt", "needle\n")
	assertSearchPaths(t, root, "main.txt")
	writeSearchFixture(t, root, ".gitignore", "*.txt\n")
	assertSearchPaths(t, root)
	writeSearchFixture(t, root, ".gitignore", "*.txt\n!main.txt\n")
	assertSearchPaths(t, root, "main.txt")
}

func TestSearchIgnoredArtifactsDoNotConsumeBudget(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", ".vista/\n")
	writeSearchFixture(t, root, "web/src/main.txt", "needle\n")
	outputDir := filepath.Join(root, "web", ".vista", "test-state")
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := range maxSearchBytes/maxFileBytes + 1 {
		path := filepath.Join(outputDir, fmt.Sprintf("output-%03d.txt", i))
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = file.Truncate(maxFileBytes)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("prepare output: %v, %v", err, closeErr)
		}
	}
	assertSearchPaths(t, filepath.Join(root, "web"), "src/main.txt")
}
