package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Agent Skills limits descriptions by Unicode characters, not UTF-8 bytes.
const MaxSkillDescriptionChars = 1024
const SkillDisabledFile = ".vista-disabled"
const skillPageBytes = 1536

type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}
type SkillPage struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Content    string `json:"content"`
	NextOffset int    `json:"nextOffset"`
	Truncated  bool   `json:"truncated"`
}

// SkillEntry also includes disabled and invalid skills so settings can repair or
// remove them. DiscoverSkills exposes only usable, enabled entries to the Agent.
type SkillEntry struct {
	SkillInfo
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	Error    string `json:"error,omitempty"`
}

func ListSkills(directory string) ([]SkillEntry, error) {
	directory, err := absolute(directory)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := []SkillEntry{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(directory, entry.Name(), "SKILL.md")
		item := SkillEntry{ID: entry.Name(), SkillInfo: SkillInfo{Name: entry.Name(), Path: path, Source: "user"}}
		_, markerErr := root.Lstat(filepath.Join(entry.Name(), SkillDisabledFile))
		item.Disabled = !errors.Is(markerErr, os.ErrNotExist)
		func() {
			info, err := root.Lstat(filepath.Join(entry.Name(), "SKILL.md"))
			if err != nil {
				item.Error = err.Error()
				return
			}
			if !info.Mode().IsRegular() {
				item.Error = "SKILL.md must be a regular file"
				return
			}
			file, err := root.Open(filepath.Join(entry.Name(), "SKILL.md"))
			if err != nil {
				item.Error = err.Error()
				return
			}
			defer file.Close()
			b, err := io.ReadAll(file)
			if err != nil {
				item.Error = err.Error()
				return
			}
			name, description, err := SkillMetadata(string(b))
			if err != nil {
				item.Error = err.Error()
				return
			}
			if name != "" {
				item.Name = name
			}
			item.Description = description
		}()
		result = append(result, item)
	}
	return result, nil
}

func DiscoverSkills(directory string) ([]SkillInfo, error) {
	entries, err := ListSkills(directory)
	if err != nil {
		return nil, err
	}
	result := []SkillInfo{}
	for _, item := range entries {
		if !item.Disabled && item.Error == "" {
			result = append(result, item.SkillInfo)
		}
	}
	return result, nil
}

func SkillMetadata(content string) (string, string, error) {
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return "", "", errors.New("SKILL.md must be UTF-8 text without NUL characters")
	}
	content = strings.ReplaceAll(strings.TrimPrefix(content, "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", "", errors.New("SKILL.md must begin with YAML frontmatter")
	}
	lines := strings.Split(content[4:], "\n")
	end := 0
	for end < len(lines) && strings.TrimSpace(lines[end]) != "---" {
		end++
	}
	if end == len(lines) {
		return "", "", errors.New("SKILL.md frontmatter is missing its closing ---")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[:end], "\n")), &metadata); err != nil {
		return "", "", errors.New("invalid SKILL.md frontmatter: " + err.Error())
	}
	name, description := strings.TrimSpace(metadata.Name), strings.TrimSpace(metadata.Description)
	if name == "" || description == "" {
		return "", "", errors.New("skill name and description are required")
	}
	if utf8.RuneCountInString(description) > MaxSkillDescriptionChars {
		return "", "", errors.New("skill description must be at most 1024 characters")
	}
	// Consume names leniently: cosmetic naming violations must not hide an
	// otherwise usable skill. The path remains the skill's unambiguous identity.
	return name, description, nil
}

func ReadSkill(path string, offset int) (SkillPage, error) {
	if strings.HasPrefix(path, systemSkillPrefix) {
		content, err := systemSkillFile(path)
		if err != nil {
			return SkillPage{}, err
		}
		return skillTextPage(path, content, offset)
	}
	path, err := absolute(path)
	if err != nil {
		return SkillPage{}, err
	}
	if filepath.Base(path) != "SKILL.md" {
		return SkillPage{}, errors.New("skill path must end in SKILL.md")
	}
	return readPagedText(path, offset)
}

func readPagedText(path string, offset int) (SkillPage, error) {
	path, err := absolute(path)
	if err != nil {
		return SkillPage{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return SkillPage{}, err
	}
	if !info.Mode().IsRegular() {
		return SkillPage{}, errors.New("instruction file must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return SkillPage{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return SkillPage{}, err
	}
	return skillTextPage(path, b, offset)
}

func skillTextPage(path string, b []byte, offset int) (SkillPage, error) {
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return SkillPage{}, errors.New("instruction file must be UTF-8 text without NUL characters")
	}
	if offset < 0 || offset > len(b) || !utf8.Valid(b[:offset]) {
		return SkillPage{}, errors.New("invalid skill byte offset")
	}
	end := min(len(b), offset+skillPageBytes)
	for end > offset && !utf8.Valid(b[offset:end]) {
		end--
	}
	hash := sha256.Sum256(b)
	return SkillPage{Path: path, SHA256: hex.EncodeToString(hash[:]), Content: string(b[offset:end]), NextOffset: end, Truncated: end < len(b)}, nil
}
