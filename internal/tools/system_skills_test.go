package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSystemSkillsRemainAvailableWithoutUserDirectoryAndReadOnDemand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skills, err := AvailableSkills(root)
	var guide SkillInfo
	for _, skill := range skills {
		if skill.Name == "vista-guide" {
			guide = skill
		}
	}
	if err != nil || guide.Source != "system" {
		t.Fatalf("system skill missing: %+v %v", skills, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("discovering embedded skills created filesystem files")
	}
	var content, digest string
	for offset := 0; ; {
		page, err := ReadSkill(guide.Path, offset)
		if err != nil || page.NextOffset <= offset {
			t.Fatalf("could not read system skill: %+v %v", page, err)
		}
		if digest != "" && digest != page.SHA256 {
			t.Fatal("embedded document digest changed across pages")
		}
		digest, content = page.SHA256, content+page.Content
		if !page.Truncated {
			break
		}
		offset = page.NextOffset
	}
	name, description, err := SkillMetadata(content)
	if err != nil || name != guide.Name || description != guide.Description {
		t.Fatal("catalog and on-demand content differ", err)
	}
	if _, err := ReadSkill("skill://system/vista-guide/references/settings.md", 0); err != nil {
		t.Fatal("embedded references are not readable", err)
	}
	for _, path := range []string{"skill://system/../vista-guide/SKILL.md", "skill://system/vista-guide/../../skills.go", "skill://system/unknown/SKILL.md"} {
		if _, err := ReadSkill(path, 0); err == nil {
			t.Fatalf("invalid system path accepted: %s", path)
		}
	}
	if strings.Contains(guide.Description, "state/instance.json") {
		t.Fatal("skill body leaked into catalog metadata")
	}
}

func TestSystemSkillCatalogDiscoversMultipleSkillsAndTheirOwnReferences(t *testing.T) {
	files := fstest.MapFS{
		"systemskills/first/SKILL.md":              {Data: []byte("---\nname: first\ndescription: First capability\n---\nFirst instructions")},
		"systemskills/first/references/first.md":   {Data: []byte("First reference")},
		"systemskills/second/SKILL.md":             {Data: []byte("---\nname: second\ndescription: Second capability\n---\nSecond instructions")},
		"systemskills/second/references/second.md": {Data: []byte("Second reference")},
	}
	entries := systemSkillsFrom(files)
	if len(entries) != 2 || entries[0].ID != "system:first" || entries[1].ID != "system:second" || entries[0].Description != "First capability" || entries[1].Description != "Second capability" {
		t.Fatalf("independent skill metadata lost: %+v", entries)
	}
	for _, entry := range entries {
		content, references, err := systemSkillDocument(files, entry.ID)
		if err != nil || len(content) == 0 || entry.Source != "system" {
			t.Fatalf("skill is not readable: %+v %v", entry, err)
		}
		if len(references) != 1 || references[0] != "references/"+entry.Name+".md" {
			t.Fatal("skill-specific reference missing", references)
		}
	}
}
