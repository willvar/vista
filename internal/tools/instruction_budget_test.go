package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInstructionPagesFitTheSharedSerializedBudget(t *testing.T) {
	for _, filename := range []string{"AGENTS.md", "SKILL.md"} {
		for _, fixture := range []struct {
			name, content string
			onePage       bool
		}{
			{"ordinary rules", strings.Repeat("Keep evidence. 中文。\n", 400), true},
			{"JSON escaping", strings.Repeat("<\t\"\\", 18000), false},
			{"short lines", strings.Repeat("x\n", 5000), false},
			{"UTF-8 boundary", strings.Repeat("🙂中文", 14000), false},
		} {
			t.Run(filename+"/"+fixture.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), filename)
				if err := os.WriteFile(path, []byte(fixture.content), 0600); err != nil {
					t.Fatal(err)
				}
				read := ReadProjectInstruction
				if filename == "SKILL.md" {
					read = ReadSkill
				}
				var recovered strings.Builder
				pages := 0
				for offset := 0; ; {
					page, err := read(path, offset)
					if err != nil {
						t.Fatal(err)
					}
					pages++
					encoded, err := json.Marshal(page)
					if err != nil || ExceedsToolBudget(string(encoded)) || !utf8.ValidString(page.Content) || page.NextOffset-offset != len(page.Content) || page.NextOffset <= offset {
						t.Fatalf("page %d: offset=%d next=%d encoded=%d error=%v", pages, offset, page.NextOffset, len(encoded), err)
					}
					if page.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(fixture.content))) {
						t.Fatal("digest no longer identifies the complete source")
					}
					recovered.WriteString(page.Content)
					if !page.Truncated {
						break
					}
					offset = page.NextOffset
				}
				if recovered.String() != fixture.content || fixture.onePage && pages != 1 || !fixture.onePage && pages < 2 {
					t.Fatalf("reconstruction=%v pages=%d", recovered.String() == fixture.content, pages)
				}
			})
		}
	}
}
