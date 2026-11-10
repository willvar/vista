package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func checklistTextTurn(w http.ResponseWriter, text string, calls ...provider.ToolCall) {
	items := make([]any, len(calls))
	for i, call := range calls {
		items[i] = map[string]any{"index": i, "id": call.ID, "type": "function", "function": call.Function}
	}
	continuationEvent(w, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": text, "tool_calls": items}, "finish_reason": "tool_calls",
	}}})
}

func persistTestChecklist(db *store.Store, command workerproto.Command, result workerproto.Result) error {
	items := make([]store.ChecklistItem, len(result.Checklist.Items))
	for i, item := range result.Checklist.Items {
		items[i] = store.ChecklistItem{Text: item.Text, Status: item.Status}
	}
	_, err := db.ReplaceChecklist(context.Background(), command.SessionID, command.RunID, items)
	return err
}

func TestChecklistFinishSavesOneResponseAndRestoresFullPairedHistory(t *testing.T) {
	db, ctx := checkpointFixture(t)
	full := strings.Repeat("完整答复 <detail>🙂\n", 5000)
	args := `{"items":[{"text":"Verified work","status":"done"},{"text":"Unavailable dependency","status":"blocked"}],"finish":true}`
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			checklistTextTurn(w, full, scriptedToolCall("final-checklist", "update_checklist", args))
			return
		}
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		answers, calls, results := 0, 0, 0
		for _, m := range body.Messages {
			if m.Role == "assistant" && m.Content == full {
				answers++
			}
			for _, call := range m.ToolCalls {
				if call.ID == "final-checklist" {
					calls++
				}
			}
			if m.Role == "tool" && m.ToolCallID == "final-checklist" && strings.Contains(m.Content, `"finish":true`) {
				results++
			}
		}
		if answers != 1 || calls != 1 || results != 1 {
			t.Errorf("restoration lost or duplicated final text/tool history: answers=%d calls=%d results=%d", answers, calls, results)
		}
		continuationAnswer(w, "Restored.")
	}))
	defer model.Close()
	user, _, err := db.BeginRun(ctx, "session", "first", "u1", "Finish the work.")
	if err != nil {
		t.Fatal(err)
	}
	command := continuationCommand(model.URL, user.Content)
	command.SessionID, command.RunID, command.MessageSequence = "session", "first", user.Sequence
	var final store.ModelOutputRef
	sink := historyTestSink(db, command, &final)
	emit := func(result workerproto.Result) error {
		if result.Kind == "checklist" {
			return persistTestChecklist(db, command, result)
		}
		return sink(result)
	}
	session := NewSession(ctx)
	answer, err := session.Run(ctx, command, emit, nil, nil)
	session.Close()
	if err != nil || requests.Load() != 1 || final.Bytes != len(full) || !strings.Contains(answer, "[Full answer:") || !strings.HasPrefix(full, strings.Split(answer, "\n\n[Full answer:")[0]) {
		t.Fatalf("final checklist required another request or lost full answer: requests=%d bytes=%d err=%v", requests.Load(), final.Bytes, err)
	}
	items, err := db.RunChecklist(ctx, "session", "first")
	if err != nil || len(items) != 2 || items[1].Status != "blocked" {
		t.Fatalf("final checklist was not persisted as supplied: %+v %v", items, err)
	}
	if _, err := db.FinishRun(ctx, "session", "first", "a1", "succeeded", answer, final); err != nil {
		t.Fatal(err)
	}
	user, _, err = db.BeginRun(ctx, "session", "second", "u2", "Continue from the recorded answer.")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "second", user.Sequence, user.Content
	restored := NewSession(ctx)
	defer restored.Close()
	answer, err = restored.Run(ctx, command, historyTestSink(db, command, &final), nil, nil)
	if err != nil || answer != "Restored." || requests.Load() != 2 {
		t.Fatalf("restore after checklist completion: %q %v requests=%d", answer, err, requests.Load())
	}
}

func TestChecklistFinishDoesNotEndOnProgressInvalidCallsOrSaveFailure(t *testing.T) {
	for _, kind := range []string{"progress", "no answer", "mixed calls", "invalid items", "save failure"} {
		t.Run(kind, func(t *testing.T) {
			_, ctx := checkpointFixture(t)
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					text, args := "Final reply.", `{"items":[{"text":"Work","status":"done"}],"finish":true}`
					switch kind {
					case "progress":
						args = `{"items":[{"text":"Work","status":"in_progress"}]}`
					case "no answer":
						text = ""
					case "invalid items":
						args = `{"items":[{"text":"Work","status":"invalid"}],"finish":true}`
					}
					calls := []provider.ToolCall{scriptedToolCall("check", "update_checklist", args)}
					if kind == "mixed calls" {
						calls = append(calls, scriptedToolCall("other", "project_instructions", `{"path":"/"}`))
					}
					checklistTextTurn(w, text, calls...)
					return
				}
				var body struct{ Messages []provider.ChatMessage }
				_ = json.NewDecoder(r.Body).Decode(&body)
				if kind != "progress" {
					found := false
					for _, m := range body.Messages {
						found = found || m.ToolCallID == "check" && strings.Contains(m.Content, `"error"`)
					}
					if !found {
						t.Error("invalid or failed finish was not returned to the model")
					}
				}
				continuationAnswer(w, "Recovered.")
			}))
			defer model.Close()
			command := continuationCommand(model.URL, "Work.")
			command.SessionID = "session"
			answer, err := Run(ctx, command, func(result workerproto.Result) error {
				if result.Kind == "checklist" && kind == "save failure" {
					return errors.New("checklist commit failed")
				}
				return nil
			})
			if err != nil || answer != "Recovered." || requests.Load() != 2 {
				t.Fatalf("premature completion: answer=%q requests=%d err=%v", answer, requests.Load(), err)
			}
		})
	}
}

func TestChecklistFinishStillConsumesNewInputsAndRuntimeResults(t *testing.T) {
	for _, kind := range []string{"input", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			_, ctx := checkpointFixture(t)
			var requests atomic.Int32
			var pending, delivered atomic.Bool
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					checklistTextTurn(w, "Initial answer.", scriptedToolCall("finish", "update_checklist", `{"items":[],"finish":true}`))
					return
				}
				var body struct{ Messages []provider.ChatMessage }
				_ = json.NewDecoder(r.Body).Decode(&body)
				found := false
				for _, m := range body.Messages {
					found = found || strings.Contains(m.Content, "New evidence")
				}
				if !found {
					t.Error("finish omitted new input/result")
				}
				continuationAnswer(w, "Updated answer.")
			}))
			defer model.Close()
			command := continuationCommand(model.URL, "Work.")
			command.SessionID = "session"
			if kind == "runtime" {
				ctx = WithRuntimeSource(ctx, RuntimeSource{Changed: func() <-chan struct{} { return nil }, Poll: func(context.Context, string, string) ([]workerproto.RuntimeEvent, error) {
					if pending.Load() && delivered.CompareAndSwap(false, true) {
						return []workerproto.RuntimeEvent{{ID: "result", RunID: command.RunID, Recipient: "main", Kind: "tool", SourceID: "prior-work", Content: "New evidence"}}, nil
					}
					return nil, nil
				}})
			}
			answer, err := RunWithInputSource(ctx, command, func(result workerproto.Result) error {
				if result.Kind == "checklist" {
					pending.Store(true)
				}
				return nil
			}, func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
				if kind == "input" && pending.Load() && delivered.CompareAndSwap(false, true) {
					if reply == nil || reply.Content != "Initial answer." {
						return nil, fmt.Errorf("previous final answer was not checkpointed: %+v", reply)
					}
					return &workerproto.Command{RunID: command.RunID, InputID: "followup", Prompt: "New evidence"}, nil
				}
				return nil, nil
			}, nil)
			if err != nil || answer != "Updated answer." || requests.Load() != 2 || !delivered.Load() {
				t.Fatalf("pending work was skipped: answer=%q requests=%d err=%v", answer, requests.Load(), err)
			}
		})
	}
}

func TestChecklistFinishOnResumeDoesNotDuplicateTheFormalReply(t *testing.T) {
	_, ctx := checkpointFixture(t)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		checklistTextTurn(w, "Final continuation answer.", scriptedToolCall("finish", "update_checklist", `{"items":[],"finish":true}`))
	}))
	defer model.Close()
	command := continuationCommand(model.URL, "Continue.")
	command.SessionID, command.Action = "session", "resume"
	checkpoints := 0
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "reply_checkpoint" {
			checkpoints++
		}
		return nil
	})
	if err != nil || answer != "Final continuation answer." || requests.Load() != 1 || checkpoints != 0 {
		t.Fatalf("duplicate continuation reply: answer=%q requests=%d checkpoints=%d err=%v", answer, requests.Load(), checkpoints, err)
	}
}

func TestChecklistFinishStillWaitsForRequiredChild(t *testing.T) {
	_, ctx := checkpointFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	var parents atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Messages[1].Content == "child" {
			select {
			case <-release:
				continuationAnswer(w, "Child evidence.")
			case <-ctx.Done():
			}
			return
		}
		switch parents.Add(1) {
		case 1:
			continuationEvent(w, mcpToolEvent("child", "delegate", `{"task":"child"}`))
		case 2:
			checklistTextTurn(w, "Initial answer.", scriptedToolCall("finish", "update_checklist", `{"items":[],"finish":true}`))
		default:
			found := false
			for _, m := range body.Messages {
				found = found || strings.Contains(m.Content, "Child evidence.")
			}
			if !found {
				t.Error("final checklist skipped required child evidence")
			}
			continuationAnswer(w, "Answer with child evidence.")
		}
	}))
	defer model.Close()
	command := continuationCommand(model.URL, "Parent work.")
	command.SessionID = "session"
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "reply_checkpoint" && released.CompareAndSwap(false, true) {
			close(release)
		}
		return nil
	})
	if err != nil || answer != "Answer with child evidence." || parents.Load() != 3 || !released.Load() {
		t.Fatalf("required work abandoned at checklist finish: answer=%q requests=%d released=%v err=%v", answer, parents.Load(), released.Load(), err)
	}
}
