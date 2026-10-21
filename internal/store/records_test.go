package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestEscapedEightKiBInputEventsRemainDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000099"
	if _, err := s.Create(ctx, session, "escaped input"); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("<", 8<<10)
	message, event, err := s.BeginRun(ctx, session, "run-1", "message-1", content)
	if err != nil || message.Content != content || len(event.Data) <= 32<<10 || len(event.Data) > 64<<10 {
		t.Fatalf("escaped input was not durably accepted: message=%d event=%d err=%v", len(message.Content), len(event.Data), err)
	}
	queued, queuedEvent, err := s.QueueInput(ctx, session, "input-1", "now", content)
	if err != nil || queued.Content != content || len(queuedEvent.Data) <= 32<<10 || len(queuedEvent.Data) > 64<<10 {
		t.Fatalf("escaped queued input was not durably accepted: input=%d event=%d err=%v", len(queued.Content), len(queuedEvent.Data), err)
	}
	delivered, err := s.DeliverInput(ctx, session, "run-1", "input-1", "message-2")
	if err != nil || delivered == nil || delivered.Content != content {
		t.Fatalf("escaped queued input was not delivered: %+v err=%v", delivered, err)
	}
}

func TestNewSessionTitleUsesFirstMessageBeforeReplyAndRespectsManualRename(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000098"
	if _, err := s.Create(ctx, session, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-1", "user-1", "  Investigate   the build\n error  "); err != nil {
		t.Fatal(err)
	}
	if item, err := s.Get(ctx, session); err != nil || item.Title != "New session" {
		t.Fatalf("first prompt was copied into title: %+v %v", item, err)
	}
	if prompt, eligible, err := s.FirstMessageTitleCandidate(ctx, session, "user-1"); err != nil || !eligible || prompt != "  Investigate   the build\n error  " {
		t.Fatalf("first message title candidate before reply: %q %v %v", prompt, eligible, err)
	}
	if _, err := s.RecordModelRequest(ctx, session, "run-1", "openai", "test", "model-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, session, "run-1", "answer-1", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if event, err := s.SetGeneratedTitle(ctx, session, "user-1", "Build error investigation"); err != nil || event == nil || event.Kind != "session_title" {
		t.Fatalf("generated title not recorded: %+v %v", event, err)
	}
	if item, err := s.Get(ctx, session); err != nil || item.Title != "Build error investigation" {
		t.Fatalf("generated title missing: %+v %v", item, err)
	}
	if err := s.Rename(ctx, session, "Build investigation"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-2", "user-2", "something else"); err != nil {
		t.Fatal(err)
	}
	if event, err := s.SetGeneratedTitle(ctx, session, "user-2", "Something else"); err != nil || event != nil {
		t.Fatalf("generated title overwrote manual title: %+v %v", event, err)
	}
	if item, err := s.Get(ctx, session); err != nil || item.Title != "Build investigation" {
		t.Fatalf("manual title overwritten: %+v %v", item, err)
	}
}

func TestManualPlaceholderRenameBlocksLateGeneratedTitle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000097"
	if _, err := s.Create(ctx, session, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "Hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, session, "run", "answer", "succeeded", "Hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, session, "New session"); err != nil {
		t.Fatal(err)
	}
	if event, err := s.SetGeneratedTitle(ctx, session, "user", "Greeting"); err != nil || event != nil {
		t.Fatalf("late generation replaced an explicitly chosen placeholder: %+v %v", event, err)
	}
}

func TestTitleCandidateOnlyUsesFirstMessageEvenAfterFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000090"
	if _, err := s.Create(ctx, session, "New session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "failed", "failed-user", "Original topic"); err != nil {
		t.Fatal(err)
	}
	if prompt, eligible, err := s.FirstMessageTitleCandidate(ctx, session, "failed-user"); err != nil || !eligible || prompt != "Original topic" {
		t.Fatalf("first message candidate: %q %v %v", prompt, eligible, err)
	}
	if _, err := s.FinishRun(ctx, session, "failed", "failed-answer", "failed", "Unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "succeeded", "success-user", "Retry original topic"); err != nil {
		t.Fatal(err)
	}
	if prompt, eligible, err := s.FirstMessageTitleCandidate(ctx, session, "success-user"); err != nil || eligible || prompt != "" {
		t.Fatalf("second message candidate: %q %v %v", prompt, eligible, err)
	}
}

func TestRunRecordsAreOrderedAndInterrupted(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000001"
	if _, err := s.Create(ctx, session, "test"); err != nil {
		t.Fatal(err)
	}
	m, e, err := s.BeginRun(ctx, session, "run-1", "message-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if m.Sequence != 1 || e.Sequence != 1 || e.Kind != "message" {
		t.Fatalf("unexpected first record: %+v %+v", m, e)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-2", "message-2", "again"); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy, got %v", err)
	}
	finished, err := s.FinishRun(ctx, session, "run-1", "message-3", "succeeded", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if finished == nil || finished.Sequence != 2 {
		t.Fatalf("unexpected finish: %+v", finished)
	}
	if again, err := s.FinishRun(ctx, session, "run-1", "message-4", "succeeded", "duplicate"); err != nil || again != nil {
		t.Fatalf("duplicate completion: %+v %v", again, err)
	}
	messages, err := s.MessagesBefore(ctx, session, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	_, _, err = s.BeginRun(ctx, session, "run-2", "message-5", "more")
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := s.InterruptRunning(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].Sequence != 4 {
		t.Fatalf("unexpected interruption: %+v", interrupted)
	}
	events, err := s.EventsAfter(ctx, session, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Sequence != 2 || events[2].Sequence != 4 || events[2].Kind != "run_finished" {
		t.Fatalf("unexpected replay: %+v", events)
	}
	if err := s.SetOpen(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-3", "message-6", "closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed, got %v", err)
	}
	if err := s.Delete(ctx, session); err != nil {
		t.Fatal(err)
	}
	events, err = s.EventsAfter(ctx, session, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events survived deletion: %+v", events)
	}
}

func TestToolEventsStayOrderedAndStopAfterRun(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000002"
	if _, err := s.Create(ctx, session, "tools"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "read"); err != nil {
		t.Fatal(err)
	}
	started, err := s.AppendRunEvent(ctx, session, "run", "tool", map[string]any{"runId": "run", "status": "started", "tool": map[string]string{"callId": "call-1", "name": "shell"}})
	if err != nil || started == nil || started.Sequence != 2 {
		t.Fatalf("start event: %+v %v", started, err)
	}
	completed, err := s.AppendRunEvent(ctx, session, "run", "tool", map[string]any{"runId": "run", "status": "succeeded", "tool": map[string]string{"callId": "call-1", "name": "shell"}})
	if err != nil || completed == nil || completed.Sequence != 3 {
		t.Fatalf("completion event: %+v %v", completed, err)
	}
	if _, err := s.FinishRun(ctx, session, "run", "assistant", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	late, err := s.AppendRunEvent(ctx, session, "run", "tool", map[string]string{"status": "late"})
	if err != nil || late != nil {
		t.Fatalf("late event persisted: %+v %v", late, err)
	}
	events, err := s.EventsAfter(ctx, session, 0, 100)
	if err != nil || len(events) != 4 || events[1].Kind != "tool" || events[2].Kind != "tool" {
		t.Fatalf("unexpected events: %+v %v", events, err)
	}
}

func TestWorkerInterruptionMarksUnconfirmedToolResultUnknown(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000004"
	if _, err := s.Create(ctx, session, "crash"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "start tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, session, "run", "tool", map[string]any{"runId": "run", "status": "started", "tool": map[string]string{"callId": "call-1", "name": "shell", "summary": "/tmp"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InterruptRunning(ctx, session, "Worker crashed"); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfter(ctx, session, 0, 100)
	if err != nil || len(events) != 4 || events[2].Kind != "tool" || events[3].Kind != "run_finished" {
		t.Fatalf("crash events: %+v %v", events, err)
	}
	var unknown struct {
		Status string `json:"status"`
		Tool   struct {
			CallID string `json:"callId"`
		} `json:"tool"`
	}
	if err := json.Unmarshal(events[2].Data, &unknown); err != nil || unknown.Status != "unknown" || unknown.Tool.CallID != "call-1" {
		t.Fatalf("unknown tool result: %+v %v", unknown, err)
	}
}

func TestSuccessfulResultCannotHideUnknownTool(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000005"
	if _, err := s.Create(ctx, session, "unknown"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, session, "run", "tool", map[string]any{"runId": "run", "status": "started", "tool": map[string]string{"callId": "call-1", "name": "write_file"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInput(ctx, session, "later", "later", "follow up"); err != nil {
		t.Fatal(err)
	}
	finished, err := s.FinishRun(ctx, session, "run", "assistant", "succeeded", "all done")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status       string `json:"status"`
		UnknownTools int    `json:"unknownTools"`
	}
	if err := json.Unmarshal(finished.Data, &result); err != nil || result.Status != "failed" || result.UnknownTools != 1 {
		t.Fatalf("unconfirmed tool was hidden by success: %+v %v", result, err)
	}
	inputs, err := s.RecentInputs(ctx, session)
	if err != nil || len(inputs) != 1 || inputs[0].Status != "paused" {
		t.Fatalf("follow-up was launched after unknown side effect: %+v %v", inputs, err)
	}
}

func TestReplyRecordsLastRequestedModel(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000003"
	if _, err := s.Create(ctx, session, "models"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	for _, model := range []struct{ provider, id string }{{"openai-chat", "gpt-test"}, {"anthropic", "claude-test"}} {
		if event, err := s.RecordModelRequest(ctx, session, "run", model.provider, model.id); err != nil || event == nil || event.Kind != "model_request" {
			t.Fatalf("record model %+v: %+v %v", model, event, err)
		}
	}
	if _, err := s.FinishRun(ctx, session, "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if event, err := s.RecordModelRequest(ctx, session, "run", "openai-chat", "late"); err != nil || event != nil {
		t.Fatalf("late model event: %+v %v", event, err)
	}
	messages, err := s.MessagesBefore(ctx, session, 0, 10)
	if err != nil || len(messages) != 2 || messages[1].Provider != "anthropic" || messages[1].ModelID != "claude-test" {
		t.Fatalf("reply model: %+v %v", messages, err)
	}
}

func TestReplyKeepsConfiguredConnectionAcrossPagesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const session = "00000000-0000-4000-8000-000000000004"
	if _, err := s.Create(ctx, session, "same model ID"); err != nil {
		t.Fatal(err)
	}
	for index, key := range []string{"herdex-luna", "other-luna"} {
		runID := "run-" + key
		if _, _, err := s.BeginRun(ctx, session, runID, "user-"+key, "Hi", key); err != nil {
			t.Fatal(err)
		}
		event, err := s.RecordModelRequest(ctx, session, runID, "openai-responses", "gpt-6-luna", key, "", "high")
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]string
		if err := json.Unmarshal(event.Data, &request); err != nil || request["key"] != key || request["reasoningEffort"] != "high" {
			t.Fatalf("request %d: %+v %v", index, request, err)
		}
		finished, err := s.FinishRun(ctx, session, runID, "answer-"+key, "succeeded", "Hey!")
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Message Message `json:"message"`
		}
		if err := json.Unmarshal(finished.Data, &data); err != nil || data.Message.ModelKey != key {
			t.Fatalf("finished %d: %+v %v", index, data.Message, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.MessagesBefore(ctx, session, 0, 2)
	if err != nil || len(page) != 2 || page[1].ModelKey != "other-luna" {
		t.Fatalf("recent page: %+v %v", page, err)
	}
	older, err := s.MessagesBefore(ctx, session, page[0].Sequence, 2)
	if err != nil || len(older) != 2 || older[1].ModelKey != "herdex-luna" {
		t.Fatalf("older page: %+v %v", older, err)
	}
	forward, err := s.MessagesAfter(ctx, session, 0, 4)
	if err != nil || len(forward) != 4 || forward[1].ModelKey != "herdex-luna" || forward[3].ModelKey != "other-luna" {
		t.Fatalf("forward page: %+v %v", forward, err)
	}
	history, err := s.HistoryMessage(ctx, session, forward[3].Sequence)
	if err != nil || history.ModelKey != "other-luna" {
		t.Fatalf("history message: %+v %v", history, err)
	}
	next, err := s.NextHistoryMessage(ctx, session, forward[2].Sequence, forward[3].Sequence+1)
	if err != nil || next.ModelKey != "other-luna" {
		t.Fatalf("next history message: %+v %v", next, err)
	}
}
