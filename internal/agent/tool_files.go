package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, "outputs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	selected, err := os.CreateTemp(dir, "lines-*")
	if err != nil {
		return "", err
	}
	defer selected.Close()
	defer os.Remove(selected.Name())
	// Leave room for JSON escaping and metadata without splitting source lines.
	file, err := tools.ReadLines(ctx, input.Path, start, min(count, maxToolResultLines-2), maxToolResult-2048, selected)
	if err != nil {
		return "", err
	}
	page := fileReadPage{Path: file.Path, SHA256: file.SHA256, StartLine: start, EndLine: file.EndLine,
		LineCount: file.EndLine - start + 1, TotalLines: file.TotalLines, Bytes: int(file.Bytes), Truncated: file.EndLine < file.TotalLines}
	if page.Truncated {
		page.NextLine = page.EndLine + 1
	}
	if _, err = selected.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if !file.Oversized {
		text, err := io.ReadAll(selected)
		if err != nil {
			return "", err
		}
		page.Content = string(text)
		return marshalResult(page)
	}
	// A first line larger than a page is retained in an artifact. JSON is
	// escaped in bounded chunks instead of materializing the line in RAM.
	reference, err := recordToolOutputStream(ctx, r.parent.RunID, call.ID, func(output io.Writer) error {
		envelope, err := json.Marshal(page)
		if err != nil {
			return err
		}
		prefix, suffix, _ := strings.Cut(string(envelope), `"content":""`)
		if _, err = io.WriteString(output, prefix+`"content":`); err != nil {
			return err
		}
		if err = streamJSONString(ctx, output, selected); err != nil {
			return err
		}
		_, err = io.WriteString(output, suffix)
		return err
	}, r.emit)
	if err != nil {
		return "", err
	}
	if _, err = selected.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	preview, err := io.ReadAll(io.LimitReader(selected, maxToolResult/8))
	if err != nil {
		return "", err
	}
	page.Content = textPrefix(string(preview), len(preview))
	output, err := marshalResult(page)
	if err != nil {
		return "", err
	}
	return modelToolPreview(output, reference), nil
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

// streamJSONString preserves the source exactly while bounding each escaped chunk.
func streamJSONString(ctx context.Context, w io.Writer, r io.Reader) error {
	if _, err := io.WriteString(w, `"`); err != nil {
		return err
	}
	buffer := make([]byte, (32<<10)+utf8.UTFMax)
	pending := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.Read(buffer[pending : 32<<10])
		n += pending
		cut := n
		if err != io.EOF {
			for cut > 0 && !utf8.Valid(buffer[:cut]) && n-cut < utf8.UTFMax {
				cut--
			}
		}
		if !utf8.Valid(buffer[:cut]) {
			return errors.New("file changed to invalid UTF-8")
		}
		escaped, encodeErr := json.Marshal(string(buffer[:cut]))
		if encodeErr != nil {
			return encodeErr
		}
		if _, writeErr := w.Write(escaped[1 : len(escaped)-1]); writeErr != nil {
			return writeErr
		}
		pending = copy(buffer, buffer[cut:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, `"`)
	return err
}
