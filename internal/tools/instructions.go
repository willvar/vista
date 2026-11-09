package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type InstructionFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// DiscoverProjectInstructions returns each target's outer rules before nearer
// rules, without rereading common ancestors during this operation. The cache
// never outlives this call; later tools must discover changed rules again.
func DiscoverProjectInstructions(ctx context.Context, targets ...string) ([]InstructionFile, error) {
	files := []InstructionFile{}
	directories := make(map[string][]InstructionFile)
	seen := make(map[string]bool)
	for _, target := range targets {
		target, err := absolute(target)
		if err != nil {
			return nil, err
		}
		directory := target
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			directory = filepath.Dir(target)
		}
		var ancestors []string
		for {
			ancestors = append(ancestors, directory)
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
		applicable := 0
		for i := len(ancestors) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			directory := ancestors[i]
			rules, cached := directories[directory]
			if !cached {
				for _, relative := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".github", "copilot-instructions.md")} {
					path := filepath.Join(directory, relative)
					info, err := os.Lstat(path)
					if err == nil && info.Mode().IsRegular() {
						hash, err := fileDigest(ctx, path)
						if err != nil {
							return nil, fmt.Errorf("read project instructions %s: %w", path, err)
						}
						rules = append(rules, InstructionFile{Path: path, Bytes: info.Size(), SHA256: hash})
					}
				}
				directories[directory] = rules
			}
			applicable += len(rules)
			if applicable > 40 {
				return nil, errors.New("more than 40 applicable instruction files")
			}
			for _, file := range rules {
				if !seen[file.Path] {
					seen[file.Path] = true
					files = append(files, file)
				}
			}
		}
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
