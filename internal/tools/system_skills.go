package tools

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// System skills are read directly from the executable. Their opaque identities
// are separate from user folder names, so neither can replace the other.
const systemSkillPrefix = "skill://system/"

//go:embed systemskills
var systemSkillFiles embed.FS

func SystemSkills() []SkillEntry {
	return systemSkillsFrom(systemSkillFiles)
}

func systemSkillsFrom(files fs.FS) []SkillEntry {
	entries, err := fs.ReadDir(files, "systemskills")
	if err != nil {
		panic("unreadable embedded skill catalog: " + err.Error())
	}
	result := make([]SkillEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		content, _, err := systemSkillDocument(files, "system:"+entry.Name())
		if err != nil {
			panic("unreadable embedded skill: " + entry.Name())
		}
		name, description, err := SkillMetadata(string(content))
		if err != nil {
			panic("invalid embedded skill: " + entry.Name())
		}
		result = append(result, SkillEntry{ID: "system:" + entry.Name(), SkillInfo: SkillInfo{
			Name: name, Description: description, Path: systemSkillPrefix + entry.Name() + "/SKILL.md", Source: "system",
		}})
	}
	return result
}

func AvailableSkills(directory string) ([]SkillInfo, error) {
	result := []SkillInfo{}
	for _, entry := range SystemSkills() {
		result = append(result, entry.SkillInfo)
	}
	users, err := DiscoverSkills(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return append(result, users...), nil
}

func systemSkillFile(path string) ([]byte, error) {
	name := strings.TrimPrefix(path, systemSkillPrefix)
	if name == path || !fs.ValidPath(name) || !strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
		return nil, errors.New("invalid system skill path")
	}
	return systemSkillFiles.ReadFile("systemskills/" + name)
}

func SystemSkillDocument(id string) ([]byte, []string, error) {
	return systemSkillDocument(systemSkillFiles, id)
}

func systemSkillDocument(filesystem fs.FS, id string) ([]byte, []string, error) {
	name := strings.TrimPrefix(id, "system:")
	if name == id || !fs.ValidPath(name) || strings.Contains(name, "/") {
		return nil, nil, os.ErrNotExist
	}
	file := "SKILL.md"
	content, err := fs.ReadFile(filesystem, "systemskills/"+name+"/"+file)
	if err != nil {
		return nil, nil, err
	}
	files := []string{}
	err = fs.WalkDir(filesystem, "systemskills/"+name, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && path != "systemskills/"+name+"/"+file {
			files = append(files, strings.TrimPrefix(path, "systemskills/"+name+"/"))
		}
		return err
	})
	return content, files, err
}
