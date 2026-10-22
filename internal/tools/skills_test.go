package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndPageSkills(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "review")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "SKILL.md")
	content := "---\nname: review\ndescription: Check code carefully\n---\n" + strings.Repeat("reference content 中文\n", 2000)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := DiscoverSkills(root)
	if err != nil || len(list) != 1 || list[0].Name != "review" || list[0].Path != path {
		t.Fatalf("discover: %+v %v", list, err)
	}
	first, err := ReadSkill(path, 0)
	if err != nil || !first.Truncated || first.NextOffset <= 0 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	var assembled strings.Builder
	for offset := 0; ; {
		page, err := ReadSkill(path, offset)
		if err != nil {
			t.Fatal(err)
		}
		assembled.WriteString(page.Content)
		if !page.Truncated {
			break
		}
		offset = page.NextOffset
	}
	if assembled.String() != content {
		t.Fatal("paged skill content changed")
	}
	if _, err := ReadSkill(path, first.NextOffset+1); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSkill(filepath.Join(root, "wrong.md"), 0); err == nil {
		t.Fatal("non-skill path accepted")
	}
}

func TestSkillDescriptionUsesUnicodeCharacters(t *testing.T) {
	for _, letter := range []string{"a", "中", "🦄"} {
		for _, count := range []int{640, 1024, 1025} {
			t.Run(fmt.Sprintf("%s-%d", letter, count), func(t *testing.T) {
				description := strings.Repeat(letter, count)
				_, got, err := SkillMetadata("---\nname: example\ndescription: " + description + "\n---\nInstructions")
				if count <= MaxSkillDescriptionChars {
					if err != nil || got != description {
						t.Fatalf("valid description was rejected or changed: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "1024 characters") {
					t.Fatalf("expected character limit error, got %v", err)
				}
			})
		}
	}
}

func TestSkillsRequireUsableMetadata(t *testing.T) {
	for _, content := range []string{
		"Plain Markdown", "---\nname: example\n---\n", "---\ndescription: Example\n---\n",
		"---\nname: example\ndescription: '  '\n---\n", "---\nname: [broken\n---\n",
	} {
		if _, _, err := SkillMetadata(content); err == nil {
			t.Fatalf("unusable metadata was accepted: %q", content)
		}
	}
}

func TestSkillDiscoveryDoesNotFailWhenDirectoryExceeds100Entries(t *testing.T) {
	root := t.TempDir()
	for i := range 105 {
		dir := filepath.Join(root, fmt.Sprintf("skill-%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: skill-%d\ndescription: Example\n---\n", i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := os.WriteFile(filepath.Join(dir, SkillDisabledFile), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if skills, err := DiscoverSkills(root); err != nil || len(skills) != 103 {
		t.Fatalf("large skill directory is unavailable: count=%d error=%v", len(skills), err)
	}
}

func TestSkillDiscoveryDisabledInvalidAndYAMLMetadata(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir := write("review", "---\r\nname: 'Code review'\r\ndescription: >-\r\n  Check changes\r\n  and run relevant tests.\r\nmetadata:\r\n  author: example\r\n---\r\n"+strings.Repeat("中文正文", 500))
	write("invalid", "---\nname: [broken\n---\n")
	write(".staging", "Hidden")
	list, err := DiscoverSkills(root)
	if err != nil || len(list) != 1 || list[0].Name != "Code review" || list[0].Description != "Check changes and run relevant tests." {
		t.Fatalf("metadata or UTF-8 discovery: %+v %v", list, err)
	}
	all, err := ListSkills(root)
	if err != nil || len(all) != 2 || all[0].Error == "" || all[1].Error != "" {
		t.Fatalf("settings should include invalid skills: %+v %v", all, err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillDisabledFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if list, err := DiscoverSkills(root); err != nil || len(list) != 0 {
		t.Fatalf("disabled skill remains discoverable: %+v %v", list, err)
	}
	all, err = ListSkills(root)
	if err != nil || !all[1].Disabled {
		t.Fatalf("disabled skill missing from settings: %+v %v", all, err)
	}
	if err := os.Remove(filepath.Join(dir, SkillDisabledFile)); err != nil {
		t.Fatal(err)
	}
	if list, err := DiscoverSkills(root); err != nil || len(list) != 1 {
		t.Fatalf("reenabled skill not discovered: %+v %v", list, err)
	}
}
