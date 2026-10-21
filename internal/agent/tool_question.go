package agent

import (
	"encoding/json"
	"errors"

	"vista/internal/provider"
	"vista/internal/question"
	"vista/internal/workerproto"
)

var questionSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "question",
	"description": "Ask for user input needed to continue, respecting the user's requested interaction style. Supports single or multiple selections and freeform answers: use an empty options array for freeform input. Custom answers are enabled by default; do not add an Other option. Put a recommended option first and suffix its label with (Recommended). Main Agent only; call alone, without other tools. Stop until the user responds.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{"questions": map[string]any{
		"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "properties": map[string]any{
			"header": map[string]any{"type": "string"}, "question": map[string]any{"type": "string"},
			"options": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			}, "required": []string{"label"}}},
			"multiple": map[string]any{"type": "boolean"}, "custom": map[string]any{"type": "boolean", "default": true},
		}, "required": []string{"question", "options"}},
	}}, "required": []string{"questions"}},
}}

func questionRequest(call provider.ToolCall) (workerproto.PendingReply, error) {
	var input struct {
		Questions []question.Question `json:"questions"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return workerproto.PendingReply{}, err
	}
	if err := question.Validate(input.Questions); err != nil {
		return workerproto.PendingReply{}, err
	}
	pending := workerproto.PendingReply{CallID: call.ID, Kind: "question", Questions: input.Questions, Content: question.Format(input.Questions)}
	if call.ID == "" || pending.Content == "" || len(pending.Content) > workerproto.MaxPromptBytes {
		return pending, errors.New("invalid question request")
	}
	encoded, err := json.Marshal(pending)
	if err != nil || len(encoded) > workerproto.MaxResultFrame-(4<<10) {
		return pending, errors.New("question request exceeds the event size; shorten the questions and options")
	}
	return pending, nil
}
