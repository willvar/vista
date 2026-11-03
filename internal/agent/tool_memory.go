package agent

import (
	"context"
	"errors"
	"fmt"
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
	"description": "Manage the user's saved preferences across sessions. Save only explicitly expressed lasting preferences or corrections; phrase each as a concise actionable preference. Do not save temporary task requirements, inferred preferences, factual claims, secrets, or permission rules. Reuse or update related preferences instead of adding duplicates or contradictions. Use list to get current IDs, text, and limits before updating or removing; previousText must match exactly. To consolidate, use action=update with changes: update retained preferences and remove absorbed entries atomically, preserving all still-valid information. If full, consolidate related entries or shorten wording, then retry; if valid preferences still do not fit, tell the user what was not saved. Never discard valid preferences just to make room. On a conflict, list again and reconsider the whole change. Main Agent only. Changes are persistent and visible to the user.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"action":       map[string]any{"type": "string", "enum": []string{"list", "add", "update", "remove"}},
		"id":           map[string]any{"type": "string", "description": "Existing memory ID for a single update or remove; omit for list, add, or changes."},
		"text":         map[string]any{"type": "string", "description": "Preference text for add or a single update, up to 4096 UTF-8 bytes; omit with changes."},
		"previousText": map[string]any{"type": "string", "description": "Exact current text from list for a single update or remove; omit with changes."},
		"changes": map[string]any{"type": "array", "minItems": 1, "maxItems": store.MaxMemories,
			"description": "For action=update only, instead of id/text/previousText: apply existing-memory updates and removals together. Each ID must appear once. All edits commit or none do; limits apply to the final result.",
			"items": map[string]any{"type": "object", "properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []string{"update", "remove"}},
				"id":           map[string]any{"type": "string", "description": "Existing memory ID from list."},
				"text":         map[string]any{"type": "string", "description": "Required for update, up to 4096 UTF-8 bytes; omit for remove."},
				"previousText": map[string]any{"type": "string", "description": "Exact current text from list, required for both actions."},
			}, "required": []string{"action", "id", "previousText"}},
		},
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
		if change.ID != "" || change.Text != "" || change.PreviousText != "" || change.Changes != nil {
			return "", errors.New("memory list accepts no edit fields")
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
		return marshalResult(map[string]any{"memories": items, "maxCount": store.MaxMemories, "maxBytes": store.MaxMemoryBytes, "maxTotalBytes": store.MaxMemoriesBytes})
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
		return "", fmt.Errorf("memory change was not confirmed: %w", err)
	}
	if change.Changes != nil {
		changes := make([]map[string]string, 0, len(change.Changes))
		for _, edit := range change.Changes {
			item := map[string]string{"action": edit.Action, "id": edit.ID}
			if edit.Action == "update" {
				item["text"] = strings.TrimSpace(edit.Text)
			}
			changes = append(changes, item)
		}
		return marshalResult(map[string]any{"action": change.Action, "changes": changes})
	}
	text := strings.TrimSpace(change.Text)
	if change.Action == "remove" {
		text = change.PreviousText
	}
	return marshalResult(map[string]any{"action": change.Action, "id": change.ID, "text": text})
}
