package server

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

const maxToolPreviewBytes = 24 << 10
const maxToolEventBytes = 32 << 10

func validTaskIdentity(taskID, parentTaskID string) bool {
	return len(taskID) <= 1024 && len(parentTaskID) <= 1024 && utf8.ValidString(taskID) && utf8.ValidString(parentTaskID)
}

func validOutputReference(stateDir, runID string, ref *workerproto.OutputRef) bool {
	if ref == nil {
		return true
	}
	if ref.Bytes < 0 || (ref.OutputPath != "" && !validOutputPath(stateDir, ref.OutputPath)) {
		return false
	}
	if ref.ArtifactID != "" {
		if _, err := uuid.Parse(ref.ArtifactID); err != nil {
			return false
		}
	}
	if ref.RunID != "" && (ref.RunID != runID || ref.Turn <= 0 || ref.Bytes > store.MaxModelOutputBytes) {
		return false
	}
	return ref.ArtifactID != "" || ref.OutputPath != "" || ref.RunID != ""
}

func validAgentTaskProgress(stateDir string, result workerproto.Result) bool {
	task := result.Task
	titleLimit := 300
	if result.Depth > 0 && task != nil && task.ID != "main" {
		titleLimit = 2048
	}
	if result.RunID == "" || task == nil || task.ID == "" || !validTaskIdentity(task.ID, task.ParentID) || len(task.Title) > titleLimit || !utf8.ValidString(task.Title) || !validOutputReference(stateDir, result.RunID, task.Report) {
		return false
	}
	if result.TaskID != "" && result.TaskID != task.ID || result.ParentTaskID != "" && result.ParentTaskID != task.ParentID {
		return false
	}
	switch task.Status {
	case "started", "succeeded", "failed", "canceled", "interrupted":
		return true
	}
	return false
}

func progressTextPrefix(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

// The transport may carry a longer result, while the event remains a preview.
// Bound the encoded JSON too: control characters can expand sixfold in JSON.
func prepareToolProgress(stateDir string, result workerproto.Result) (workerproto.Result, bool) {
	tool := result.Tool
	if result.RunID == "" || tool == nil || tool.CallID == "" || len(tool.CallID) > 1024 || len(tool.Name) > 128 || len(tool.Summary) > 300 || !utf8.ValidString(result.Content) || !validTaskIdentity(result.TaskID, result.ParentTaskID) || !validOutputReference(stateDir, result.RunID, tool.OutputRef) {
		return result, false
	}
	if result.Status != "started" && result.Status != "succeeded" && result.Status != "failed" {
		return result, false
	}
	if result.Status == "started" && (result.AckID == "" || len(result.AckID) > 64 || tool.ProviderExecuted) {
		return result, false
	}
	result.AckID = ""
	copiedTool := *tool
	result.Tool = &copiedTool
	content := progressTextPrefix(result.Content, maxToolPreviewBytes)
	result.Tool.Truncated = result.Tool.Truncated || len(content) < len(result.Content)
	result.Content = content
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, false
	}
	if len(encoded) <= maxToolEventBytes {
		return result, true
	}
	result.Tool.Truncated = true
	low, high := 0, len(content)
	for low < high {
		middle := (low + high + 1) / 2
		result.Content = progressTextPrefix(content, middle)
		encoded, err = json.Marshal(result)
		if err != nil {
			return result, false
		}
		if len(encoded) <= maxToolEventBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	result.Content = progressTextPrefix(content, low)
	encoded, err = json.Marshal(result)
	return result, err == nil && len(encoded) <= maxToolEventBytes
}
