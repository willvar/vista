package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"vista/internal/agentconfig"
	"vista/internal/appdirs"
	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/tools"
)

var editInstructionsSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "edit_instructions",
	"description": "Read or maintain the user's long-term instructions in AGENTS.md. Use global for explicit cross-project requirements, or project with an explicit absolute directory for requirements limited to that project or subtree. Do not guess a project, store temporary requests, infer lasting preferences, or duplicate existing instructions. Respect authoritative-source and synchronization rules; use their prescribed maintenance process instead of editing a generated copy. Read before editing. append adds text without rewriting the document; replace requires exactly one oldText match (text may be empty to delete it). Writes require the SHA-256 returned by read, or missing when the file does not exist. Concurrent changes require rereading. Existing project-rule discovery and precedence remain unchanged. Main Agent only.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"action":         map[string]any{"type": "string", "enum": []string{"read", "append", "replace"}},
		"scope":          map[string]any{"type": "string", "enum": []string{"global", "project"}},
		"directory":      map[string]any{"type": "string", "description": "Explicit absolute project directory containing the target AGENTS.md; required only for project scope. No automatic project-root selection."},
		"expectedSha256": map[string]any{"type": "string", "description": "Required for writes; SHA-256 from read, or missing for a new file."},
		"text":           map[string]any{"type": "string", "description": "Text to append or replacement text. Empty replacement removes oldText."},
		"oldText":        map[string]any{"type": "string", "description": "Required only for replace; exact, nonempty text occurring once."},
		"startLine":      map[string]any{"type": "integer", "minimum": 1, "description": "Read pagination, as in read_file."},
		"lineCount":      map[string]any{"type": "integer", "minimum": 1, "description": "Read pagination, as in read_file."},
	}, "required": []string{"action", "scope"}},
}}

type instructionEdit struct {
	Action         string  `json:"action"`
	Scope          string  `json:"scope"`
	Directory      string  `json:"directory"`
	ExpectedSHA256 string  `json:"expectedSha256"`
	Text           *string `json:"text"`
	OldText        string  `json:"oldText"`
	StartLine      *int    `json:"startLine"`
	LineCount      *int    `json:"lineCount"`
}

func instructionEditTarget(call provider.ToolCall) (instructionEdit, string, error) {
	var input instructionEdit
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return input, "", err
	}
	var directory string
	switch input.Scope {
	case "global":
		if input.Directory != "" {
			return input, "", errors.New("global scope does not accept a project directory")
		}
		var err error
		directory, err = appdirs.Config()
		if err != nil {
			return input, "", err
		}
	case "project":
		if !filepath.IsAbs(input.Directory) {
			return input, "", errors.New("project scope requires an explicit absolute directory")
		}
		directory = input.Directory
	default:
		return input, "", errors.New("scope must be global or project")
	}
	// Resolve directory aliases before discovering rules and sharing write locks.
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return input, "", err
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return input, "", errors.New("instruction directory must exist and be a directory")
	}
	path := agentconfig.InstructionsPath(directory)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return input, "", errors.New("AGENTS.md must be a regular file; follow the authoritative-source maintenance process for links")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return input, "", err
	}
	switch input.Action {
	case "read":
		if input.Text != nil || input.OldText != "" || input.ExpectedSHA256 != "" || input.StartLine != nil && *input.StartLine < 1 || input.LineCount != nil && *input.LineCount < 1 {
			return input, "", errors.New("read accepts only scope, directory and positive pagination fields")
		}
	case "append", "replace":
		if input.ExpectedSHA256 == "" || input.Text == nil || input.StartLine != nil || input.LineCount != nil {
			return input, "", errors.New("writes require text and expectedSha256 and do not accept pagination")
		}
		if input.Action == "append" && (strings.TrimSpace(*input.Text) == "" || input.OldText != "") || input.Action == "replace" && input.OldText == "" {
			return input, "", errors.New("append requires nonblank text and no oldText; replace requires nonempty oldText")
		}
	default:
		return input, "", errors.New("action must be read, append or replace")
	}
	return input, path, nil
}

func (r *toolRuntime) editInstructions(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.depth != 0 {
		return "", errors.New("edit_instructions is only available to the main Agent")
	}
	input, path, err := instructionEditTarget(call)
	if err != nil {
		return "", err
	}
	if input.Action == "read" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return marshalResult(map[string]any{"path": path, "sha256": "missing", "content": "", "exists": false})
		} else if err != nil {
			return "", err
		}
		arguments, _ := json.Marshal(map[string]any{"path": path, "startLine": input.StartLine, "lineCount": input.LineCount})
		call.Function.Arguments = string(arguments)
		return r.readFile(ctx, call)
	}
	snapshot, err := agentconfig.ReadSnapshot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	if snapshot.SHA256 != input.ExpectedSHA256 {
		return "", tools.ErrConflict
	}
	content := snapshot.Content
	if input.Action == "append" {
		separator := ""
		if content != "" && !strings.HasSuffix(content, "\n") {
			separator = "\n"
		}
		content += separator + *input.Text
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
	} else {
		if strings.Count(content, input.OldText) != 1 {
			return "", errors.New("oldText must match exactly once; reread and provide a unique passage")
		}
		content = strings.Replace(content, input.OldText, *input.Text, 1)
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	result, err := tools.WriteTextWithDiff(ctx, stateDir, path, input.ExpectedSHA256, content)
	if err != nil {
		return "", err
	}
	return r.fileWriteResult(ctx, call, result)
}
