package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	gitignore "github.com/denormal/go-gitignore"
)

// Each scope owns one directory's rules. Walking skips ignored directories
// before loading their rules, so a negation cannot reopen an excluded parent.
type searchIgnore struct {
	directory string
	parent    *searchIgnore
	rules     gitignore.GitIgnore
	excluded  bool
}

func newSearchIgnore(root string) (*searchIgnore, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	var directories []string
	for directory := root; ; directory = filepath.Dir(directory) {
		directories = append(directories, directory)
		// A .git file marks a worktree just as a .git directory marks a repo.
		if _, err := os.Lstat(filepath.Join(directory, ".git")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	var scope *searchIgnore
	for i := len(directories) - 1; i >= 0; i-- {
		scope, err = scope.descend(directories[i])
		if err != nil {
			return nil, err
		}
	}
	return scope, nil
}

func (s *searchIgnore) descend(directory string) (*searchIgnore, error) {
	scope := &searchIgnore{directory: directory, parent: s}
	if s != nil && s.ignored(directory, true) {
		scope.excluded = true
		return scope, nil
	}
	path := filepath.Join(directory, ".gitignore")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return scope, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read search ignore rules: %w", err)
	}
	// Like Git, do not follow symbolic links to ignore files.
	if !info.Mode().IsRegular() {
		return scope, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read search ignore rules: %w", err)
	}
	scope.rules = gitignore.New(bytes.NewReader(data), directory, nil)
	return scope, nil
}

func (s *searchIgnore) ignored(path string, isDir bool) bool {
	if s.excluded {
		return true
	}
	// A matching rule in a nearer directory overrides an outer rule. Within
	// each file the parser applies the last matching rule, including ! entries.
	for scope := s; scope != nil; scope = scope.parent {
		if scope.rules == nil {
			continue
		}
		relative, err := filepath.Rel(scope.directory, path)
		if err != nil || relative == "." {
			continue
		}
		if match := scope.rules.Relative(relative, isDir); match != nil {
			return match.Ignore()
		}
	}
	return false
}
