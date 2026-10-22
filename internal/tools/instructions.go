package tools

import (
	"errors"
	"os"
	"path/filepath"
)

type InstructionFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// DiscoverProjectInstructions returns outer rules before nearer rules. A caller
// should apply later instructions only within the path where they were found.
func DiscoverProjectInstructions(target string) ([]InstructionFile, error) {
	target, err := absolute(target)
	if err != nil {
		return nil, err
	}
	directory := target
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		directory = filepath.Dir(target)
	}
	ancestors := []string{}
	for {
		ancestors = append(ancestors, directory)
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	files := []InstructionFile{}
	for i := len(ancestors) - 1; i >= 0; i-- {
		for _, relative := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".github", "copilot-instructions.md")} {
			path := filepath.Join(ancestors[i], relative)
			info, err := os.Lstat(path)
			if err == nil && info.Mode().IsRegular() {
				files = append(files, InstructionFile{Path: path, Bytes: info.Size()})
			}
		}
	}
	if len(files) > 40 {
		return nil, errors.New("more than 40 applicable instruction files")
	}
	return files, nil
}

func ReadProjectInstruction(path string, offset int) (SkillPage, error) {
	base := filepath.Base(path)
	if base != "AGENTS.md" && base != "CLAUDE.md" && base != "copilot-instructions.md" {
		return SkillPage{}, errors.New("unsupported project instruction filename")
	}
	return readPagedText(path, offset)
}
