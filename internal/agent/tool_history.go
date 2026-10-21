package agent

import (
	"context"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

var historyIndexSchema = map[string]any{"type": "function", "function": map[string]any{"name": "history_index", "description": "List older messages in this Session with sequence numbers and short previews. Use beforeSequence to page backward.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"beforeSequence": map[string]any{"type": "integer", "minimum": 1}}}}}

func (r *toolRuntime) historyIndex(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.parent.SessionID == "" || r.parent.MessageSequence <= 0 {
		return "", errors.New("Session history is unavailable")
	}
	var input struct {
		BeforeSequence int64 `json:"beforeSequence"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.BeforeSequence == 0 {
		input.BeforeSequence = r.parent.MessageSequence
	}
	if input.BeforeSequence < 1 || input.BeforeSequence > r.parent.MessageSequence {
		return "", errors.New("history index must be before this message")
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	db, err := store.OpenReadOnly(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	items, err := db.HistoryIndex(ctx, r.parent.SessionID, input.BeforeSequence, 4)
	if err != nil {
		return "", err
	}
	var next int64
	var more bool
	if len(items) > 0 {
		next = items[len(items)-1].Sequence
		more, err = db.HasMessagesBefore(ctx, r.parent.SessionID, next)
		if err != nil {
			return "", err
		}
	}
	for {
		result, err := marshalResult(map[string]any{"messages": items, "nextBeforeSequence": next, "hasMore": more})
		if err == nil || len(items) == 0 {
			return result, err
		}
		items = items[:len(items)-1]
		if len(items) == 0 {
			return "", errors.New("history index entry exceeds model result budget")
		}
		next = items[len(items)-1].Sequence
		more = true
	}
}

var readHistorySchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_history", "description": "Read a page of an earlier message in this Session by sequence. Use nextOffset for the next page.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"sequence": map[string]any{"type": "integer", "minimum": 1}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"sequence"}}}}

func (r *toolRuntime) readHistory(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.parent.SessionID == "" || r.parent.MessageSequence <= 0 {
		return "", errors.New("Session history is unavailable")
	}
	var input struct {
		Sequence int64 `json:"sequence"`
		Offset   int   `json:"offset"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.Sequence < 1 || input.Sequence >= r.parent.MessageSequence {
		return "", errors.New("history sequence must be before this message")
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	db, err := store.OpenReadOnly(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	message, err := db.HistoryMessage(ctx, r.parent.SessionID, input.Sequence)
	if err != nil {
		return "", err
	}
	refs, err := db.MessageResources(ctx, r.parent.SessionID, message.ID)
	if err != nil {
		return "", err
	}
	if input.Offset < 0 || input.Offset > len(message.Content) || !utf8.ValidString(message.Content[:input.Offset]) {
		return "", errors.New("history offset must be a UTF-8 byte boundary")
	}
	end := min(len(message.Content), input.Offset+1024)
	for end > input.Offset && !utf8.ValidString(message.Content[input.Offset:end]) {
		end--
	}
	for {
		result, err := marshalResult(map[string]any{"sequence": message.Sequence, "role": message.Role, "content": message.Content[input.Offset:end], "nextOffset": end, "totalBytes": len(message.Content), "imageCount": message.ImageCount, "resources": refs, "outputTurn": message.OutputTurn, "outputBytes": message.OutputBytes, "runId": message.RunID})
		if err == nil || end == input.Offset {
			return result, err
		}
		end--
		for end > input.Offset && !utf8.ValidString(message.Content[input.Offset:end]) {
			end--
		}
	}
}

var readModelOutputSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_model_output", "description": "Read a byte page of a long model answer in this Session using its run ID and turn number. Limit defaults to 8192 bytes, up to 32768. Use nextOffset to continue.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"runId": map[string]any{"type": "string"}, "turn": map[string]any{"type": "integer", "minimum": 1}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 4, "maximum": tools.MaxOutputPageBytes}}, "required": []string{"runId", "turn"}}}}

func (r *toolRuntime) readModelOutput(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.parent.SessionID == "" {
		return "", errors.New("Session model output is unavailable")
	}
	var input struct {
		RunID  string `json:"runId"`
		Turn   int32  `json:"turn"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.RunID == "" || len(input.RunID) > 64 || input.Turn <= 0 || input.Offset < 0 {
		return "", errors.New("invalid model output range")
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	db, err := store.OpenReadOnly(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	limit, err := tools.OutputPageLimit(input.Limit)
	if err != nil {
		return "", err
	}
	return fitOutputPage(limit, func(pageLimit int) (string, error) {
		page, err := db.ReadModelOutput(ctx, r.parent.SessionID, input.RunID, input.Turn, input.Offset, pageLimit)
		if err != nil {
			return "", err
		}
		return marshalResult(page)
	})
}

func (r *toolRuntime) readToolOutput(ctx context.Context, call provider.ToolCall) (string, error) {
	return readToolOutput(ctx, call.Function.Arguments, r.parent)
}

var updateChecklistSchema = map[string]any{"type": "function", "function": map[string]any{"name": "update_checklist", "description": "Replace this run's visible checklist for a multi-step task. Use pending, in_progress, done, or blocked. Mark only verified work done; an empty list clears the checklist. Main Agent only.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"items": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "maxLength": 160}, "status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "blocked"}}}, "required": []string{"text", "status"}}}}, "required": []string{"items"}}}}

func (r *toolRuntime) updateChecklist(ctx context.Context, call provider.ToolCall) (string, error) {
	if r.depth != 0 || r.parent.SessionID == "" {
		return "", errors.New("only the main Agent can update the run checklist")
	}
	var input struct {
		Items []workerproto.ChecklistItem `json:"items"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.Items == nil || len(input.Items) > 16 {
		return "", errors.New("checklist requires an items array of at most 16 entries")
	}
	items := make([]store.ChecklistItem, len(input.Items))
	for index, item := range input.Items {
		items[index] = store.ChecklistItem{Text: item.Text, Status: item.Status}
	}
	if err := store.ValidateChecklist(items); err != nil {
		return "", err
	}
	if err := r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "checklist", Status: "succeeded", Checklist: &workerproto.Checklist{Items: input.Items}}); err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"items": input.Items})
}
