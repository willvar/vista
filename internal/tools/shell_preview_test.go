package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShellHeadAndTailShareOneBudgetAndLeaveTheMiddleReadable(t *testing.T) {
	for _, content := range []string{
		"",
		"short evidence\n",
		strings.Repeat("a", maxShellPreview),
		strings.Repeat("a", maxShellPreview+1),
		strings.Repeat("a", maxShellPreview/2-1) + "中文🙂" + strings.Repeat("b", 9000) + "END_FAILURE",
		strings.Repeat("中文🙂", 3000),
	} {
		t.Run(fmt.Sprintf("%d bytes", len(content)), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "output-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.WriteString(content); err != nil {
				t.Fatal(err)
			}
			result := ShellResult{Bytes: int64(len(content))}
			if err := result.capturePreview(file, []byte(content[:min(len(content), maxShellPreview)])); err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(result.Preview) || result.NextOffset != int64(result.PreviewBytes) {
				t.Fatal("invalid head boundary")
			}
			if len(content) <= maxShellPreview {
				if result.Tail != nil || result.Truncated || result.Preview != content {
					t.Fatal("short output was not returned in full")
				}
				return
			}
			tail := result.Tail
			if tail == nil || !result.Truncated || tail.Offset <= result.NextOffset || tail.NextOffset != result.Bytes || !utf8.ValidString(tail.Content) {
				t.Fatal("invalid tail range")
			}
			if result.PreviewBytes+int(tail.NextOffset-tail.Offset) > maxShellPreview {
				t.Fatal("head and tail exceeded their shared budget")
			}
			middle := make([]byte, tail.Offset-result.NextOffset)
			if _, err := file.ReadAt(middle, result.NextOffset); err != nil {
				t.Fatal(err)
			}
			if result.Preview+string(middle)+tail.Content != content {
				t.Fatal("original byte ranges skipped or duplicated output")
			}
		})
	}
}
