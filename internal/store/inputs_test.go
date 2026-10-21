package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"vista/internal/resources"
)

func TestQueuedInputsDeliverInOrderAndSurviveInterruption(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const sessionID = "00000000-0000-4000-8000-000000000004"
	if _, err := s.Create(ctx, sessionID, "inputs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, sessionID, "run-1", "msg-1", "start"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, sessionID, "now-1", "now", "change direction"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, sessionID, "later-1", "later", "next task", "zen", "high"); err != nil {
		t.Fatal(err)
	}
	message, err := s.DeliverInput(ctx, sessionID, "run-1", "now-1", "msg-2")
	if err != nil || message == nil || message.Content != "change direction" {
		t.Fatalf("immediate delivery: %+v %v", message, err)
	}
	if duplicate, err := s.DeliverInput(ctx, sessionID, "run-1", "now-1", "msg-duplicate"); err != nil || duplicate != nil {
		t.Fatalf("duplicate input delivery: %+v %v", duplicate, err)
	}
	if _, err := s.FinishRun(ctx, sessionID, "run-1", "msg-3", "succeeded", "first answer"); err != nil {
		t.Fatal(err)
	}
	queued, err := s.NextQueuedInput(ctx, sessionID)
	if err != nil || queued == nil || queued.ID != "later-1" || queued.ModelKey != "zen" || queued.ReasoningEffort != "high" {
		t.Fatalf("next queued input: %+v %v", queued, err)
	}
	message, err = s.BeginQueuedRun(ctx, sessionID, queued.ID, "run-2", "msg-4")
	if err != nil || message == nil || message.Content != "next task" {
		t.Fatalf("deferred delivery: %+v %v", message, err)
	}
	if _, err := s.FinishRun(ctx, sessionID, "run-2", "msg-5", "succeeded", "second answer"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, sessionID, "run-3", "msg-6", "again"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, sessionID, "later-2", "later", "keep me visible"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, sessionID, "run-3", "", "interrupted", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.NextQueuedInput(ctx, sessionID); err != nil || queued != nil {
		t.Fatalf("interrupted input was replayable: %+v %v", queued, err)
	}
	inputs, err := s.RecentInputs(ctx, sessionID)
	if err != nil || len(inputs) != 3 || inputs[2].ID != "later-2" || inputs[2].Status != "paused" {
		t.Fatalf("visible input status: %+v %v", inputs, err)
	}
}

func TestQueuedInputsHaveNoCountLimitAndAllPendingRemainVisible(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const sessionID = "00000000-0000-4000-8000-000000000005"
	if _, err := s.Create(ctx, sessionID, "inputs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, sessionID, "run", "message", "start"); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if _, _, err := s.QueueInput(ctx, sessionID, string(rune('a'+i)), "later", "later"); err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := s.RecentInputs(ctx, sessionID)
	if err != nil || len(inputs) != 40 {
		t.Fatalf("pending inputs truncated: %d %v", len(inputs), err)
	}
}

func TestInterjectionCommitsCompletedReplyBeforeUserMessage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(ctx, "session", "interjection"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "first", "start"); err != nil {
		t.Fatal(err)
	}
	const answer = "first answer"
	if _, err := s.AppendModelDelta(ctx, "session", "run", 1, 0, answer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, "session", "follow-up", "now", "continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeliverInput(ctx, "session", "run", "follow-up", "incomplete-follow-up", CompletedReply{
		ID: "incomplete-answer", Content: answer, Output: ModelOutputRef{Turn: 1, Bytes: len(answer) + 1},
	}); err == nil {
		t.Fatal("accepted an incomplete output checkpoint")
	}
	before, err := s.MessagesBefore(ctx, "session", 0, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("failed checkpoint partially committed: %+v %v", before, err)
	}
	message, err := s.DeliverInput(ctx, "session", "run", "follow-up", "follow-up-message", CompletedReply{
		ID: "first-answer", Content: answer, Output: ModelOutputRef{Turn: 1, Bytes: len(answer)},
	})
	if err != nil || message == nil || message.Content != "continue" {
		t.Fatalf("interjection delivery: %+v %v", message, err)
	}
	if duplicate, err := s.DeliverInput(ctx, "session", "run", "follow-up", "duplicate-user", CompletedReply{
		ID: "duplicate-answer", Content: answer, Output: ModelOutputRef{Turn: 1, Bytes: len(answer)},
	}); err != nil || duplicate != nil {
		t.Fatalf("repeated checkpoint delivery: %+v %v", duplicate, err)
	}
	messages, err := s.MessagesBefore(ctx, "session", 0, 10)
	if err != nil || len(messages) != 3 {
		t.Fatalf("message order: %+v %v", messages, err)
	}
	if messages[1].Role != "assistant" || messages[1].Content != answer || messages[1].OutputTurn != 1 || messages[1].OutputBytes != len(answer) || messages[2].Role != "user" || messages[2].Content != "continue" {
		t.Fatalf("unexpected interjection messages: %+v", messages)
	}
	events, err := s.EventsAfter(ctx, "session", 1, 10)
	if err != nil || len(events) != 5 || events[0].Kind != "model_delta" || events[1].Kind != "input_queued" || events[2].Kind != "message" || events[3].Kind != "message" || events[4].Kind != "input_delivered" {
		t.Fatalf("event order: %+v %v", events, err)
	}
	var persisted Message
	if err := json.Unmarshal(events[2].Data, &persisted); err != nil || persisted.Role != "assistant" || persisted.OutputTurn != 1 {
		t.Fatalf("checkpoint event: %+v %v", persisted, err)
	}
	page, err := s.ReadFullModelOutput(ctx, "session", "run", 1)
	if err != nil || !page.Complete || page.Content != answer {
		t.Fatalf("completed reply output: %+v %v", page, err)
	}
	if _, err := s.FinishRun(ctx, "session", "run", "", "interrupted", "cancelled"); err != nil {
		t.Fatal(err)
	}
	messages, err = s.MessagesBefore(ctx, "session", 0, 10)
	if err != nil || len(messages) != 3 || messages[1].Content != answer {
		t.Fatalf("checkpoint lost after interruption: %+v %v", messages, err)
	}
}

func TestInputEditWithdrawAndDeliveryAreMutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(ctx, "session", "inputs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "first", "start"); err != nil {
		t.Fatal(err)
	}
	image := fixtureImage(t)
	if _, _, err := s.QueueInputWithImages(ctx, "session", "edit", "later", "old", []Image{image}, "model", "high"); err != nil {
		t.Fatal(err)
	}
	content, mode := "latest", "now"
	updated, _, err := s.UpdateInput(ctx, "session", "edit", &content, &mode)
	if err != nil || updated.Content != content || updated.Mode != mode || updated.ImageCount != 1 || updated.ModelKey != "model" || updated.ReasoningEffort != "high" {
		t.Fatalf("edit lost metadata: %+v %v", updated, err)
	}
	withdrawn, images, _, err := s.RemoveInput(ctx, "session", "edit", true)
	if err != nil || withdrawn.Content != content || len(images) != 1 || len(images[0].Hash) != 64 || len(images[0].Data) != 0 {
		t.Fatalf("withdraw lost draft or image: %+v %v", withdrawn, err)
	}
	original, err := s.ResolveImages(ctx, []resources.Ref{images[0].Ref()})
	if err != nil || !bytes.Equal(original[0].Data, image.Data) {
		t.Fatalf("withdrawn image original changed: %v", err)
	}
	if delivered, err := s.DeliverInput(ctx, "session", "run", "edit", "removed"); err != nil || delivered != nil {
		t.Fatalf("withdrawn input executed: %+v %v", delivered, err)
	}
	if _, _, err := s.QueueInput(ctx, "session", "delivered", "now", "accepted"); err != nil {
		t.Fatal(err)
	}
	if message, err := s.DeliverInput(ctx, "session", "run", "delivered", "accepted-message"); err != nil || message == nil {
		t.Fatalf("delivery: %+v %v", message, err)
	}
	if _, _, err := s.UpdateInput(ctx, "session", "delivered", &content, nil); !errors.Is(err, ErrInputConsumed) {
		t.Fatalf("edited consumed input: %v", err)
	}
	if _, _, _, err := s.RemoveInput(ctx, "session", "delivered", true); !errors.Is(err, ErrInputConsumed) {
		t.Fatalf("withdrew consumed input: %v", err)
	}
}

func TestPausedInputsPreserveImagesAcrossRestartAndResume(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "session", "inputs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "run", "first", "start"); err != nil {
		t.Fatal(err)
	}
	image := fixtureImage(t)
	if _, _, err := s.QueueInputWithImages(ctx, "session", "pending", "now", "keep image", []Image{image}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "session", "run", "", "interrupted", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, err := s.Get(ctx, "session")
	if err != nil || !item.InputQueuePaused {
		t.Fatalf("pause not restored: %+v %v", item, err)
	}
	if pending, err := s.NextQueuedInput(ctx, "session"); err != nil || pending != nil {
		t.Fatalf("paused input automatically replayed: %+v %v", pending, err)
	}
	if _, _, err := s.QueueInput(ctx, "session", "while-paused", "later", "new task"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("idle input should start a new run: %v", err)
	}
	if _, err := s.ResumeInputs(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.NextQueuedInput(ctx, "session")
	if err != nil || pending == nil || pending.ID != "pending" {
		t.Fatalf("resume: %+v %v", pending, err)
	}
	message, err := s.BeginQueuedRun(ctx, "session", pending.ID, "resumed", "resumed-first")
	if err != nil || message == nil {
		t.Fatalf("start resumed: %+v %v", message, err)
	}
	images, err := s.MessageImages(ctx, "session", message.ID)
	if err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, image.Data) {
		t.Fatalf("paused image not preserved: %+v %v", images, err)
	}
}

func TestNewInputsRemainDeliverableBesidePausedInputs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(ctx, "session", "queue recovery"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "failed", "first", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, "session", "old", "now", "keep paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "session", "failed", "error", "failed", "HTTP 403"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, "session", "retry", "retry-user", "try again"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"now", "later"} {
		input, _, err := s.QueueInput(ctx, "session", mode, mode, "new "+mode)
		if err != nil || input.Status != "queued" || input.RunID != "retry" {
			t.Fatalf("new input inherited old pause: %+v %v", input, err)
		}
	}
	input, err := s.NextInterjection(ctx, "session", "retry")
	if err != nil || input == nil || input.ID != "now" {
		t.Fatalf("new interjection was blocked: %+v %v", input, err)
	}
	message, err := s.DeliverInput(ctx, "session", "retry", "now", "now-user")
	if err != nil || message == nil || message.Content != "new now" {
		t.Fatalf("new interjection was not delivered: %+v %v", message, err)
	}
	if _, err := s.FinishRun(ctx, "session", "retry", "retry-answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	input, err = s.NextQueuedInput(ctx, "session")
	if err != nil || input == nil || input.ID != "later" {
		t.Fatalf("new deferred input was blocked: %+v %v", input, err)
	}
	if _, err := s.BeginQueuedRun(ctx, "session", "later", "next", "later-user"); err != nil {
		t.Fatalf("new deferred input could not start: %v", err)
	}
	if _, err := s.FinishRun(ctx, "session", "next", "next-answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if input, err := s.NextQueuedInput(ctx, "session"); err != nil || input != nil {
		t.Fatalf("old paused input resumed automatically: %+v %v", input, err)
	}
	inputs, err := s.RecentInputs(ctx, "session")
	if err != nil || len(inputs) != 3 || inputs[0].ID != "old" || inputs[0].Status != "paused" {
		t.Fatalf("old paused input was changed: %+v %v", inputs, err)
	}
	if _, _, _, err := s.RemoveInput(ctx, "session", "old", false); err != nil {
		t.Fatal(err)
	}
	if item, err := s.Get(ctx, "session"); err != nil || item.InputQueuePaused {
		t.Fatalf("last paused input removal left stale pause: %+v %v", item, err)
	}
}
