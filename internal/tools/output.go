package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const DefaultOutputPageBytes = 8 << 10
const MaxOutputPageBytes = 32 << 10

type OutputPage struct {
	Offset         int64  `json:"offset"`
	NextOffset     int64  `json:"nextOffset"`
	AvailableBytes int64  `json:"availableBytes"`
	Content        string `json:"content"`
	Truncated      bool   `json:"truncated"`
}

func OutputPageLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultOutputPageBytes, nil
	}
	if limit < utf8.UTFMax || limit > MaxOutputPageBytes {
		return 0, errors.New("limit must be between 4 and 32768 bytes")
	}
	return limit, nil
}

// completeUTF8Prefix leaves a trailing partial rune for the next byte page.
// Invalid bytes already present in shell output are preserved until rendering.
func completeUTF8Prefix(content []byte) []byte {
	start := len(content) - 1
	for start >= 0 && len(content)-start <= utf8.UTFMax && !utf8.RuneStart(content[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(content[start:]) {
		return content[:start]
	}
	return content
}

// openOutput reads an already resolved artifact path beneath Vista's output
// directory. Opening through os.Root also prevents symlinks escaping that root.
func openOutput(stateDir, outputPath string) (*os.File, error) {
	rootPath := filepath.Join(stateDir, "outputs")
	rel, err := filepath.Rel(rootPath, outputPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("invalid output path")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(rel)
}

// ReadOutputPreview applies only the user's UTF-8 byte setting. Zero returns
// the entire saved text; continuation-page and line budgets do not apply here.
func ReadOutputPreview(stateDir, outputPath string, limit int) (OutputPage, error) {
	if limit < 0 {
		return OutputPage{}, errors.New("byte limit must be nonnegative")
	}
	file, err := openOutput(stateDir, outputPath)
	if err != nil {
		return OutputPage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return OutputPage{}, err
	}
	if !info.Mode().IsRegular() {
		return OutputPage{}, errors.New("invalid output path")
	}
	var source io.Reader = file
	if limit > 0 {
		source = io.LimitReader(file, int64(limit))
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return OutputPage{}, err
	}
	if int64(len(data)) < info.Size() {
		data = completeUTF8Prefix(data)
	}
	next := int64(len(data))
	return OutputPage{NextOffset: next, AvailableBytes: info.Size(), Content: strings.ToValidUTF8(string(data), "�"), Truncated: next < info.Size()}, nil
}

func ReadOutputPage(stateDir, outputPath string, offset int64, limit int) (OutputPage, error) {
	limit, err := OutputPageLimit(limit)
	if err != nil {
		return OutputPage{}, err
	}
	file, err := openOutput(stateDir, outputPath)
	if err != nil {
		return OutputPage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return OutputPage{}, err
	}
	if !info.Mode().IsRegular() || offset < 0 || offset > info.Size() {
		return OutputPage{}, errors.New("invalid output range")
	}
	length := min(int64(limit), info.Size()-offset)
	content := make([]byte, length)
	n, err := file.ReadAt(content, offset)
	if (err != nil && err != io.EOF) || int64(n) != length {
		return OutputPage{}, errors.New("output read failed")
	}
	if offset+length < info.Size() {
		content = completeUTF8Prefix(content)
	}
	next := offset + int64(len(content))
	return OutputPage{Offset: offset, NextOffset: next, AvailableBytes: info.Size(), Content: strings.ToValidUTF8(string(content), "�"), Truncated: next < info.Size()}, nil
}
