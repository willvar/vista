package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWriteDiffCapturesCommittedContent(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		created             bool
		want                []string
	}{
		{"create", "", "中文🙂\nsecond\n", true, []string{"--- /dev/null\n", "+中文🙂\n", "+second\n"}},
		{"empty-create", "", "", true, nil},
		{"edit", "keep\nbefore\n", "keep\nafter\n", false, []string{" keep\n", "-before\n", "+after\n"}},
		{"no-op", "same\n", "same\n", false, nil},
		{"clear", "old", "", false, []string{"-old\n", "\\ No newline at end of file"}},
		{"crlf", "old\r\n", "new\r\n", false, []string{"-old\r\n", "+new\r\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.txt")
			expected := "missing"
			if !test.created {
				if err := os.WriteFile(path, []byte(test.before), 0640); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256([]byte(test.before))
				expected = hex.EncodeToString(hash[:])
			}
			result, err := WriteFileWithDiff(context.Background(), dir, path, expected, test.after)
			if err != nil || result.Diff.Created != test.created || result.Diff.Path != path || result.Diff.Omitted != "" {
				t.Fatalf("write diff: %+v, %v", result, err)
			}
			saved, err := ReadFile(path)
			if err != nil || saved.Content != test.after || saved.SHA256 != result.SHA256 {
				t.Fatalf("diff did not describe the committed write: %+v, %v", saved, err)
			}
			for _, want := range test.want {
				if !strings.Contains(result.Diff.Unified, want) {
					t.Fatalf("diff missing %q: %s", want, result.Diff.Unified)
				}
			}
			if len(test.want) == 0 && result.Diff.Unified != "" {
				t.Fatalf("invented content changes: %s", result.Diff.Unified)
			}
			if failed, err := WriteFileWithDiff(context.Background(), dir, path, "missing", "conflict"); !errors.Is(err, ErrConflict) || failed.SHA256 != "" || failed.Diff.Unified != "" {
				t.Fatalf("conflict returned a successful diff: %+v, %v", failed, err)
			}
		})
	}
}

func TestLargeOriginalOmitsDiffWithoutBlockingReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	before := strings.Repeat("x", maxFileBytes+1)
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(before))
	result, err := WriteFileWithDiff(context.Background(), dir, path, hex.EncodeToString(hash[:]), "short replacement")
	if err != nil || result.Diff.Omitted != "too_large" || result.Diff.Unified != "" || result.SHA256 == "" {
		t.Fatalf("large replacement: %+v, %v", result, err)
	}
}
