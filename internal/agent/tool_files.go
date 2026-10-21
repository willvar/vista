package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/tools"
)

const defaultReadFileLines = 200

var readFileSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "read_file",
	"description": "Read numbered UTF-8 lines at an absolute path. Use 1-based line numbers from search, not byte offsets. Defaults to line 1 and 200 lines. Returns the actual line range and nextLine; set startLine to nextLine to continue. Large pages stop between complete lines; a single oversized line is saved for read_tool_output. SHA-256 covers the entire original file for optimistic writes; line-number prefixes are not file contents. Example: {\"path\":\"/project/file.ts\",\"startLine\":280,\"lineCount\":80}.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string"},
			"startLine": map[string]any{"type": "integer", "minimum": 1, "description": "First line to read (1-based, default 1)."},
			"lineCount": map[string]any{"type": "integer", "minimum": 1, "description": "Maximum number of lines to read (default 200)."},
		},
		"required": []string{"path"},
	},
}}

type fileReadPage struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	LineCount  int    `json:"lineCount"`
	TotalLines int    `json:"totalLines"`
	NextLine   int    `json:"nextLine,omitempty"`
	Bytes      int    `json:"bytes"`
	Truncated  bool   `json:"truncated"`
}

func (r *toolRuntime) readFile(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Path      string `json:"path"`
		StartLine *int   `json:"startLine"`
		LineCount *int   `json:"lineCount"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	start, count := 1, defaultReadFileLines
	if input.StartLine != nil {
		start = *input.StartLine
	}
	if input.LineCount != nil {
		count = *input.LineCount
	}
	if start < 1 || count < 1 {
		return "", errors.New("startLine and lineCount must be positive integers; line numbers start at 1")
	}
	file, err := tools.ReadFile(input.Path)
	if err != nil {
		return "", err
	}
	// Use the same newline/line-number convention as search, including a blank
	// final line after a trailing newline. Hashes still cover the original bytes.
	lines := strings.Split(file.Content, "\n")
	if start > len(lines) {
		return "", fmt.Errorf("startLine %d is beyond the end of the file (%d lines); use a startLine between 1 and %d", start, len(lines), len(lines))
	}
	count = min(count, len(lines)-start+1, maxToolResultLines)
	for {
		numbered := make([]string, count)
		for i := range numbered {
			numbered[i] = fmt.Sprintf("%d: %s", start+i, lines[start+i-1])
		}
		page := fileReadPage{
			Path: file.Path, SHA256: file.SHA256, Content: strings.Join(numbered, "\n"),
			StartLine: start, EndLine: start + count - 1, LineCount: count,
			TotalLines: len(lines), Bytes: len(file.Content), Truncated: start+count-1 < len(lines),
		}
		if page.Truncated {
			page.NextLine = page.EndLine + 1
		}
		output, err := marshalResult(page)
		if err != nil || !exceedsToolBudget(output) || count == 1 {
			// A single oversized line uses the normal recoverable artifact path;
			// never split it and then advance nextLine past unread content.
			return output, err
		}
		count /= 2
	}
}

var searchSchema = map[string]any{"type": "function", "function": map[string]any{"name": "search", "description": "Search UTF-8 files under an explicit absolute directory with a Go regular expression. Matches include 1-based line numbers; use read_file with startLine to read surrounding lines. Respects ancestor and nested .gitignore rules; skips .git and node_modules directories.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"root": map[string]any{"type": "string"}, "pattern": map[string]any{"type": "string"}}, "required": []string{"root", "pattern"}}}}

func (r *toolRuntime) search(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Root    string `json:"root"`
		Pattern string `json:"pattern"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	result, err := tools.Search(ctx, input.Root, input.Pattern)
	if err != nil {
		return "", err
	}
	return marshalResult(result)
}

var writeFileSchema = map[string]any{"type": "function", "function": map[string]any{"name": "write_file", "description": "Atomically write UTF-8 content at an absolute path only if its SHA-256 matches the value from read_file, or expectedSha256 is 'missing' for a new file.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "expectedSha256": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "expectedSha256", "content"}}}}

func (r *toolRuntime) writeFile(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Path           string `json:"path"`
		ExpectedSHA256 string `json:"expectedSha256"`
		Content        string `json:"content"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	hash, err := tools.WriteFile(ctx, stateDir, input.Path, input.ExpectedSHA256, input.Content)
	if err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"path": input.Path, "sha256": hash})
}
