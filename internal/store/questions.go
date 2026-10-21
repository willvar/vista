package store

import (
	"encoding/json"
	"errors"

	"vista/internal/question"
	"vista/internal/workerproto"
)

var ErrNoPendingQuestion = errors.New("this question is no longer awaiting an answer")
var ErrInvalidQuestionReply = errors.New("invalid question reply")

func ValidatePendingReply(pending *PendingReply) error {
	if pending.CallID == "" {
		return errors.New("invalid pending question call id")
	}
	if pending.Kind != "question" || pending.Content == "" || len(pending.Content) > workerproto.MaxPromptBytes {
		return errors.New("invalid pending reply content")
	}
	if err := question.Validate(pending.Questions); err != nil {
		return err
	}
	if pending.Content != question.Format(pending.Questions) {
		return errors.New("pending question content does not match its choices")
	}
	return nil
}

// Keep the original tool identity alongside the choices, across restarts and
// conversation rollback. Content remains the human-readable display projection.
func pendingFromStorage(kind, runID, content string) *PendingReply {
	pending := &PendingReply{Kind: kind, RunID: runID, Content: content}
	var saved pendingQuestion
	if err := json.Unmarshal([]byte(content), &saved); err != nil {
		pending.Content = ""
		return pending
	}
	pending.CallID, pending.Questions = saved.CallID, saved.Questions
	pending.Content = question.Format(pending.Questions)
	return pending
}

type pendingQuestion struct {
	CallID    string              `json:"callId"`
	Questions []question.Question `json:"questions"`
}

func pendingStorageContent(pending *PendingReply) (string, error) {
	encoded, err := json.Marshal(pendingQuestion{CallID: pending.CallID, Questions: pending.Questions})
	return string(encoded), err
}
