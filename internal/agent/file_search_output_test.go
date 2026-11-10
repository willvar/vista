package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"vista/internal/tools"
)

func TestSearchOutputPreservesAllCoordinatesAndMetadata(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first.go", "nested/second.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("before\nneedle 中文 <tag>\nafter\n", 30)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"content", "files", "count"} {
		t.Run(mode, func(t *testing.T) {
			original, err := tools.SearchFiles(context.Background(), tools.SearchOptions{Root: root, Pattern: "needle", ContextLines: 1, Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			text, err := marshalSearchOutput(root, original)
			var output searchOutput
			if err != nil || json.Unmarshal([]byte(text), &output) != nil {
				t.Fatalf("invalid tool output: %s %v", text, err)
			}
			restored := []tools.Match{}
			for _, file := range output.Files {
				if filepath.IsAbs(file.Path) {
					t.Fatalf("group repeats absolute path: %s", file.Path)
				}
				for _, match := range file.Matches {
					restored = append(restored, tools.Match{Path: filepath.Join(output.Root, filepath.FromSlash(file.Path)), Line: match.Line, Text: match.Text, Context: match.Context})
				}
			}
			paths := make([]string, len(output.Paths))
			for i, path := range output.Paths {
				paths[i] = filepath.Join(output.Root, filepath.FromSlash(path))
			}
			if !reflect.DeepEqual(restored, original.Matches) || !slices.Equal(paths, original.Paths) || output.Count != original.Count || output.FilesScanned != original.FilesScanned || output.Truncated != original.Truncated {
				t.Fatal("compact output lost search information")
			}
			if mode == "content" {
				before, _ := json.Marshal(original)
				if len(text) >= len(before) {
					t.Fatalf("repeated paths were not reduced: %d >= %d", len(text), len(before))
				}
				t.Logf("search bytes: %d -> %d", len(before), len(text))
			}
		})
	}
}

func TestFindFilesOutputPreservesEmptyResultsAndPartialWarnings(t *testing.T) {
	root := t.TempDir()
	for _, paths := range [][]string{{}, {filepath.Join(root, "source.go"), filepath.Join(root, "nested", "source.go")}} {
		original := tools.FindFilesResult{Paths: paths, Truncated: true, SearchOmissions: tools.SearchOmissions{
			SkippedDirectories: 1, Warnings: []tools.SearchWarning{{Path: filepath.Join(root, "locked"), Error: "permission denied"}},
		}}
		text, err := marshalFindFilesOutput(root, original)
		var output struct {
			Root      string   `json:"root"`
			Paths     []string `json:"paths"`
			Truncated bool     `json:"truncated"`
			tools.SearchOmissions
		}
		if err != nil || json.Unmarshal([]byte(text), &output) != nil || output.Paths == nil || !output.Truncated || !reflect.DeepEqual(output.SearchOmissions, original.SearchOmissions) {
			t.Fatalf("lost empty or incomplete discovery metadata: %s %v", text, err)
		}
		for i, path := range output.Paths {
			if filepath.IsAbs(path) || filepath.Join(output.Root, filepath.FromSlash(path)) != paths[i] {
				t.Fatalf("incorrect relative coordinate: %s", path)
			}
		}
	}
}
