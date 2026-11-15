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
	"description": "Ask only for user input needed to continue, respecting the requested interaction style. Do not repeat supplied information or ask for facts available through authorized read-only checks; finish independent work first. Batch independent questions. For known branches, give every question and option a stable ID and put when on dependent questions, referencing an earlier single-choice question and its option ID. Declare all known alternatives, not an unconditional sequence assuming one answer. Example: kind offers image/video; image_format has when {questionId:kind,optionId:image}, video_codec has when {questionId:kind,optionId:video}. If a follow-up needs new evidence, tool results, reasoning, or an unexpected custom answer, ask the prerequisite first and generate the follow-up after the reply. One decision per question; explain consequences and tradeoffs in option descriptions. Supports single/multiple selection and freeform (empty options). Custom answers default on; do not add Other. Put a justified recommended option first and suffix its label with (Recommended), without treating it as consent. Main Agent only; call alone and stop until answered.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{"questions": map[string]any{
		"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "properties": map[string]any{
			"id":     map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]*$", "description": "Unique stable question ID; use IDs on every question and option."},
			"header": map[string]any{"type": "string"}, "question": map[string]any{"type": "string"},
			"options": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"id":    map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]*$", "description": "Stable option ID, unique within this question."},
				"label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			}, "required": []string{"id", "label"}}},
			"multiple": map[string]any{"type": "boolean"}, "custom": map[string]any{"type": "boolean", "default": true},
			"when": map[string]any{"type": "object", "description": "Show and require this question only when the referenced earlier single-choice question is active and its specified option is selected. Omit for independent questions. Custom parent answers select no predefined branch.", "properties": map[string]any{
				"questionId": map[string]any{"type": "string"}, "optionId": map[string]any{"type": "string"},
			}, "required": []string{"questionId", "optionId"}},
		}, "required": []string{"id", "question", "options"}},
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
