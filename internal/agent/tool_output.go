package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"vista/internal/toolinput"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

// Model context and event transport have different bounds. Full output is
// preserved before either preview is made, and a successful tool stays successful.
type boundedToolOutput struct {
	model     string
	event     string
	reference *workerproto.OutputRef
	truncated bool
}

func textPrefix(text string, maximum int) string {
	end := min(len(text), maximum)
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func eventPreview(text string) string {
	return jsonTextPreview(text, maxAnswerPreviewJSONBytes)
}

func jsonTextPreview(text string, maximum int) string {
	encoded, _ := json.Marshal(text)
	if len(encoded) <= maximum {
		return text
	}
	low, high, best := 0, len(text), 0
	for low <= high {
		probe := low + (high-low)/2
		prefix := textPrefix(text, probe)
		encoded, _ := json.Marshal(prefix)
		if len(encoded) <= maximum {
			best = len(prefix)
			low = probe + 1
		} else {
			high = probe - 1
		}
	}
	return text[:best]
}

func exceedsToolBudget(output string) bool { return tools.ExceedsToolBudget(output) }

func modelToolPreview(output string, reference *workerproto.OutputRef) string {
	return modelToolPreviewLimit(output, reference, maxToolResult)
}

func modelToolPreviewLimit(output string, reference *workerproto.OutputRef, maximum int) string {
	location := fmt.Sprintf("run %s, turn %d; use read_model_output", reference.RunID, reference.Turn)
	if reference.ArtifactID != "" {
		location = "artifact " + reference.ArtifactID + "; use read_tool_output with artifactId and offset 0"
	}
	modelRef := *reference
	modelRef.OutputPath = ""
	notice := fmt.Sprintf("Output preview truncated. Full output: %d bytes at %s.", reference.Bytes, location)
	previewText := output
	var content map[string]any
	if json.Unmarshal([]byte(output), &content) == nil {
		for _, key := range []string{"answer", "content", "preview"} {
			if text, ok := content[key].(string); ok {
				previewText = text
				break
			}
		}
	}
	_, hasFileHash := content["sha256"].(string)
	startLine, hasLine := content["startLine"].(float64)
	filePage := hasFileHash && hasLine && startLine > 0
	if filePage {
		notice += " Read the saved output to recover the complete source line; nextLine, when present, is for the following source line."
	}
	preview := textPrefix(previewText, maximum)
	lines := strings.SplitAfter(preview, "\n")
	if len(lines) > maxToolResultLines-2 {
		preview = strings.Join(lines[:maxToolResultLines-2], "")
	}
	encode := func(prefix string) []byte {
		value := map[string]any{"preview": prefix, "truncated": true, "complete": true, "outputRef": &modelRef, "bytes": reference.Bytes, "hint": notice}
		if complete, ok := content["complete"].(bool); ok {
			value["complete"] = complete
		}
		if failure, ok := content["error"].(string); ok {
			value["error"] = jsonTextPreview(failure, maximum/4)
		}
		for _, key := range []string{"exitCode", "signal", "terminationReason", "jobId", "offset", "nextOffset", "availableBytes"} {
			switch field := content[key].(type) {
			case float64, bool:
				value[key] = field
			case string:
				if len(field) <= 1024 {
					value[key] = field
				}
			}
		}
		// Child report metadata may identify any of several children. Its identity
		// must survive previewing, including a replay after compaction.
		if taskID, ok := content["taskId"].(string); ok && len(taskID) <= 1024 {
			value["taskId"] = taskID
			if status, ok := content["status"].(string); ok && len(status) <= 32 {
				value["status"] = status
			}
			if collected, ok := content["alreadyCollected"].(bool); ok {
				value["alreadyCollected"] = collected
			}
		}
		if reference.ArtifactID != "" {
			value["artifactId"] = reference.ArtifactID
		}
		if filePage {
			for _, key := range []string{"path", "sha256", "startLine", "endLine", "lineCount", "totalLines", "nextLine"} {
				if field, ok := content[key]; ok {
					value[key] = field
				}
			}
		}
		encoded, _ := json.Marshal(value)
		return encoded
	}
	low, high, best := 0, len(preview), 0
	for low <= high {
		probe := low + (high-low)/2
		prefix := textPrefix(preview, probe)
		if len(encode(prefix)) <= maximum {
			best = len(prefix)
			low = probe + 1
		} else {
			high = probe - 1
		}
	}
	return string(encode(preview[:best]))
}

// Preserve recoverable partial output when a tool fails (for example a shell
// timeout) while the caller still marks the tool event as failed.
func mergeToolError(output string, err error) string {
	value := map[string]any{}
	if output != "" {
		_ = json.Unmarshal([]byte(output), &value)
	}
	if value == nil {
		value = map[string]any{}
	}
	value["error"] = err.Error()
	var inputError *toolinput.Error
	if errors.As(err, &inputError) {
		value["executed"] = false
		value["field"] = inputError.Field
		value["actualBytes"] = inputError.ActualBytes
		value["maxBytes"] = inputError.MaxBytes
		value["hint"] = inputError.Hint
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func boundToolOutput(ctx context.Context, runID, callID, toolName, output string, reference *workerproto.OutputRef, emit func(workerproto.Result) error) (boundedToolOutput, error) {
	result := boundedToolOutput{model: output, event: eventPreview(output), reference: reference}
	result.truncated = result.event != output
	// Network tools apply the user's byte setting; shell already shares its
	// own 8 KiB source preview budget between head and tail. A second generic
	// preview would discard continuation metadata (or the final diagnostic).
	modelTruncated := toolName != "websearch" && toolName != "webfetch" && toolName != "shell" && exceedsToolBudget(output)
	if (result.truncated || modelTruncated) && reference == nil {
		var err error
		reference, err = recordToolOutput(ctx, runID, callID, []byte(output), emit)
		if err != nil {
			return result, err
		}
		result.reference = reference
	}
	if modelTruncated {
		result.model = modelToolPreview(output, reference)
		result.truncated = true
	}
	return result, nil
}

func resultOutputRef(output string) *workerproto.OutputRef {
	var value struct {
		ArtifactID string                 `json:"artifactId"`
		OutputPath string                 `json:"outputPath"`
		Bytes      int64                  `json:"bytes"`
		Report     *workerproto.OutputRef `json:"report"`
	}
	if json.Unmarshal([]byte(output), &value) != nil {
		return nil
	}
	if value.Report != nil {
		return value.Report
	}
	if value.ArtifactID != "" || value.OutputPath != "" {
		return &workerproto.OutputRef{ArtifactID: value.ArtifactID, OutputPath: value.OutputPath, Bytes: value.Bytes}
	}
	return nil
}
