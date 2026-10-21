package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func scriptedToolCall(id, name, arguments string) provider.ToolCall {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name, call.Function.Arguments = name, arguments
	return call
}

func continuationToolCalls(w http.ResponseWriter, calls ...provider.ToolCall) {
	wire := make([]map[string]any, len(calls))
	for i, call := range calls {
		wire[i] = map[string]any{"index": i, "id": call.ID, "type": "function", "function": call.Function}
	}
	continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": wire}, "finish_reason": "tool_calls"}}})
}

func TestLargeToolArgumentsExecuteAndPersistInFull(t *testing.T) {
	for _, size := range []int{5 << 10, 96 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "large.txt")
			content := strings.Repeat("x", size)
			arguments, _ := json.Marshal(map[string]string{"path": path, "expectedSha256": "missing", "content": content})
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					continuationToolCalls(w, scriptedToolCall("large-write", "write_file", string(arguments)))
					return
				}
				var request struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				last := request.Messages[len(request.Messages)-1]
				if last.Role != "tool" || last.ToolCallID != "large-write" || strings.Contains(last.Content, `"error"`) {
					t.Errorf("write did not return success to model: %+v", last)
				}
				continuationAnswer(w, "written")
			}))
			defer upstream.Close()
			user, _, err := db.BeginRun(ctx, "session", "large", "u1", "write the complete file")
			if err != nil {
				t.Fatal(err)
			}
			command := continuationCommand(upstream.URL, user.Content)
			command.RunID, command.SessionID, command.MessageSequence = "large", "session", user.Sequence
			var final store.ModelOutputRef
			sink := historyTestSink(db, command, &final)
			answer, err := Run(ctx, command, func(result workerproto.Result) error {
				encoded, err := json.Marshal(result)
				if err != nil || len(encoded) > workerproto.MaxResultFrame {
					return fmt.Errorf("unbounded result frame: %d bytes, %v", len(encoded), err)
				}
				return sink(result)
			})
			if err != nil || answer != "written" || requests.Load() != 2 {
				t.Fatalf("large write: answer=%q requests=%d err=%v", answer, requests.Load(), err)
			}
			written, err := os.ReadFile(path)
			if err != nil || string(written) != content {
				t.Fatalf("file was truncated: bytes=%d err=%v", len(written), err)
			}
			command.Action = "compact" // Load the current run's committed history too.
			messages, err := loadConversationContext(ctx, command, 0)
			if err != nil {
				t.Fatal(err)
			}
			var retained int
			for _, message := range messages {
				for _, call := range message.ToolCalls {
					if call.ID == "large-write" && call.Function.Arguments == string(arguments) {
						retained++
					}
				}
			}
			if retained != 1 {
				t.Fatalf("full arguments were not retained exactly once: %d", retained)
			}
		})
	}
}

func TestInvalidToolArgumentsAllowSiblingAndModelCorrection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	badPath, goodPath := filepath.Join(root, "corrected.txt"), filepath.Join(root, "sibling.txt")
	bad, _ := json.Marshal(map[string]string{"path": badPath, "expectedSha256": "missing", "content": strings.Repeat("x", 5<<10), "unexpected": "field"})
	good, _ := json.Marshal(map[string]string{"path": goodPath, "expectedSha256": "missing", "content": "sibling"})
	corrected, _ := json.Marshal(map[string]string{"path": badPath, "expectedSha256": "missing", "content": "corrected"})
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			continuationToolCalls(w, scriptedToolCall("bad", "write_file", string(bad)), scriptedToolCall("good", "write_file", string(good)))
		case 2:
			var request struct{ Messages []provider.ChatMessage }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			results := request.Messages[len(request.Messages)-2:]
			if results[0].ToolCallID != "bad" || !strings.Contains(results[0].Content, "unknown field") || results[1].ToolCallID != "good" || strings.Contains(results[1].Content, `"error"`) {
				t.Errorf("tool failure escaped its call or lost order: %+v", results)
			}
			continuationToolCalls(w, scriptedToolCall("retry", "write_file", string(corrected)))
		default:
			continuationAnswer(w, "both written")
		}
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failed, succeeded := 0, 0
	answer, err := Run(ctx, continuationCommand(upstream.URL, "write both files"), func(result workerproto.Result) error {
		if result.Kind == "tool" {
			if result.Status == "failed" {
				failed++
			} else if result.Status == "succeeded" {
				succeeded++
			}
		}
		return nil
	})
	if err != nil || answer != "both written" || failed != 1 || succeeded != 2 || requests.Load() != 3 {
		t.Fatalf("model correction: answer=%q failed=%d succeeded=%d requests=%d err=%v", answer, failed, succeeded, requests.Load(), err)
	}
	for path, want := range map[string]string{badPath: "corrected", goodPath: "sibling"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("write %s: %q %v", path, content, err)
		}
	}
}

func TestInvalidControlCallsDoNotAbortSiblingTools(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "sibling.txt")
	arguments, _ := json.Marshal(map[string]string{"path": path, "expectedSha256": "missing", "content": "written"})
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			continuationToolCalls(w, scriptedToolCall("ask", "question", `{}`), scriptedToolCall("list-1", "update_checklist", `{}`), scriptedToolCall("list-2", "update_checklist", `{}`), scriptedToolCall("write", "write_file", string(arguments)))
			return
		}
		var request struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		results := request.Messages[len(request.Messages)-4:]
		for i, message := range results {
			if message.Role != "tool" || strings.Contains(message.Content, `"error"`) != (i < 3) {
				t.Errorf("bad control call aborted valid sibling: %+v", results)
			}
		}
		continuationAnswer(w, "recovered")
	}))
	defer upstream.Close()
	answer, err := Run(context.Background(), continuationCommand(upstream.URL, "work"), func(result workerproto.Result) error {
		if result.Kind == "question" || result.Kind == "checklist" {
			return fmt.Errorf("invalid control call was executed: %s", result.Kind)
		}
		return nil
	})
	if err != nil || answer != "recovered" || requests.Load() != 2 {
		t.Fatalf("control validation: answer=%q requests=%d err=%v", answer, requests.Load(), err)
	}
}

func TestQuestionValidationAllowsModelCorrectionAndLargeValidRequest(t *testing.T) {
	largeQuestion, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{
		"question": "Which implementation?", "options": []any{map[string]string{"label": "Go", "description": strings.Repeat("detail ", 750)}},
	}}})
	largeHeader, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{
		"header": strings.Repeat("h", 96<<10), "question": "Choose?", "options": []any{map[string]string{"label": "Go"}},
	}}})
	for _, test := range []struct{ name, arguments, wantError string }{
		{"missing-questions", `{}`, "at least one question"},
		{"oversized-event", string(largeHeader), "question request exceeds the event size"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, baseCtx := checkpointFixture(t)
			ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			defer cancel()
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					continuationToolCalls(w, scriptedToolCall("invalid-question", "question", test.arguments))
					return
				}
				var request struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				last := request.Messages[len(request.Messages)-1]
				if last.Role != "tool" || last.ToolCallID != "invalid-question" || !strings.Contains(last.Content, test.wantError) {
					t.Errorf("invalid question did not return a correctable tool error: %+v", last)
				}
				continuationToolCalls(w, scriptedToolCall("valid-question", "question", string(largeQuestion)))
			}))
			defer upstream.Close()
			command := continuationCommand(upstream.URL, "choose an implementation")
			command.SessionID = "session"
			var pending *workerproto.PendingReply
			answer, err := Run(ctx, command, func(result workerproto.Result) error {
				encoded, err := json.Marshal(result)
				if err != nil || len(encoded) > workerproto.MaxResultFrame {
					return fmt.Errorf("oversized question event: %d bytes, %v", len(encoded), err)
				}
				if result.Kind == "question" {
					if pending != nil {
						return fmt.Errorf("invalid request produced an extra pending question")
					}
					pending = result.Pending
				}
				return nil
			})
			if err != nil || pending == nil || len(answer) <= 4<<10 || answer != pending.Content || requests.Load() != 2 {
				t.Fatalf("question correction: pending=%t answerBytes=%d requests=%d err=%v", pending != nil, len(answer), requests.Load(), err)
			}
		})
	}
}
