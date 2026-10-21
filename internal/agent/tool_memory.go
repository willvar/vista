package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

var memorySchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "memory",
	"description": "Manage the user's saved preferences across sessions. Save only explicitly expressed lasting preferences or corrections; phrase each as a concise actionable preference. Do not save temporary task requirements, inferred preferences, factual claims, secrets, or permission rules. Update an existing preference instead of adding a conflicting one. Use list to get current IDs and text before updating or removing; previousText must match exactly. Main Agent only. Changes are persistent and visible to the user.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"action":       map[string]any{"type": "string", "enum": []string{"list", "add", "update", "remove"}},
		"id":           map[string]any{"type": "string", "description": "Existing memory ID for update or remove; omit for list or add."},
		"text":         map[string]any{"type": "string", "description": "Preference text for add or update, up to 4096 UTF-8 bytes."},
		"previousText": map[string]any{"type": "string", "description": "Exact current text from list, required for update or remove to protect concurrent edits."},
	}, "required": []string{"action"}},
}}

func (r *toolRuntime) memory(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.depth != 0 {
		return "", errors.New("memory is only available to the main Agent")
	}
	var change workerproto.MemoryChange
	if err := decodeArguments(call.Function.Arguments, &change); err != nil {
		return "", err
	}
	if change.Action == "list" {
		if change.ID != "" || change.Text != "" || change.PreviousText != "" {
			return "", errors.New("memory list accepts no id or text")
		}
		state, err := instance.Dir()
		if err != nil {
			return "", err
		}
		db, err := store.OpenReadOnly(filepath.Join(state, "vista.db"))
		if err != nil {
			return "", err
		}
		defer db.Close()
		items, err := db.Memories(ctx)
		if err != nil {
			return "", err
		}
		return marshalResult(map[string]any{"memories": items})
	}
	if change.Action == "add" {
		if change.ID != "" {
			return "", errors.New("memory add does not accept an id")
		}
		change.ID = uuid.NewString()
	}
	if err := store.ValidateMemoryChange(change); err != nil {
		return "", err
	}
	if err := r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "memory_change", Memory: &change}); err != nil {
		return "", err
	}
	text := strings.TrimSpace(change.Text)
	if change.Action == "remove" {
		text = change.PreviousText
	}
	return marshalResult(map[string]any{"action": change.Action, "id": change.ID, "text": text})
}
