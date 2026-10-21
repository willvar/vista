package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// This sink commits the same history/input/output boundaries as the Server,
// while keeping provider requests observable without a real upstream service.
func historyTestSink(db *store.Store, command workerproto.Command, final *store.ModelOutputRef) func(workerproto.Result) error {
	return func(result workerproto.Result) error {
		ctx := context.Background() // Completed results survive run cancellation.
		switch result.Kind {
		case "model_history":
			event, err := db.RecordModelHistory(ctx, command.SessionID, result)
			if err == nil && event == nil {
				return fmt.Errorf("history belongs to an inactive run")
			}
			return err
		case "model_request":
			_, err := db.RecordModelRequestEvent(ctx, command.SessionID, result)
			return err
		case "model_delta":
			_, err := db.AppendModelDelta(ctx, command.SessionID, result.RunID, result.Turn, result.Depth, result.Content, result.TaskID, result.ParentTaskID)
			return err
		case "tool":
			_, err := db.AppendRunEvent(ctx, command.SessionID, result.RunID, result.Kind, result)
			return err
		case "artifact":
			artifact := result.Artifact
			_, err := db.RecordArtifact(ctx, store.Artifact{ID: artifact.ID, SessionID: command.SessionID, RunID: result.RunID,
				CallID: artifact.CallID, TaskID: artifact.TaskID, ParentTaskID: artifact.ParentTaskID,
				OutputPath: artifact.OutputPath, Status: result.Status, Bytes: artifact.Bytes, Complete: artifact.Complete, Error: artifact.Error})
			return err
		case "context_notice":
			return persistCheckpoint(db, ctx, command, result.Checkpoint)
		case "model_complete":
			*final = store.ModelOutputRef{Turn: result.Turn, Bytes: result.FullBytes}
		}
		return nil
	}
}

func historyTextResponse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}, "finish_reason": "stop"}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

func TestCrossRunRequestsRetainToolEvidenceAfterSuccessFailureAndCancel(t *testing.T) {
	for _, ending := range []string{"succeeded", "failed", "interrupted"} {
		t.Run(ending, func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			const evidence = "SOURCE_ONLY_FROM_TOOL_93b186"
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(evidence), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			var verified atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				n := requests.Add(1)
				if n == 1 {
					args, _ := json.Marshal(map[string]string{"path": path})
					chunk, _ := json.Marshal(mcpToolEvent("read-source", "read_file", string(args)))
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
				followup := request.Messages[len(request.Messages)-1].Content == "Where did that evidence come from?"
				if followup {
					calls, results, answers := 0, 0, 0
					for _, m := range request.Messages {
						calls += len(m.ToolCalls)
						if m.Role == "tool" && m.ToolCallID == "read-source" && strings.Contains(m.Content, evidence) {
							results++
						}
						if m.Role == "assistant" && m.Content == "File checked." {
							answers++
						}
					}
					wantAnswers := 0
					if ending == "succeeded" {
						wantAnswers = 1
					}
					if calls != 1 || results != 1 || answers != wantAnswers {
						t.Errorf("follow-up lost/duplicated history: calls=%d results=%d answers=%d", calls, results, answers)
					}
					verified.Store(true)
					historyTextResponse(w, "The file tool supplied the evidence.")
				} else if ending == "failed" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"message":"fixture rejected"}}`)
				} else {
					historyTextResponse(w, "File checked.")
				}
			}))
			defer upstream.Close()
			user, _, err := db.BeginRun(ctx, "session", "first", "u1", "Read the file.")
			if err != nil {
				t.Fatal(err)
			}
			command := workerproto.Command{SessionID: "session", RunID: "first", MessageSequence: user.Sequence, Prompt: user.Content,
				Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
			var final store.ModelOutputRef
			sink := historyTestSink(db, command, &final)
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			session := NewSession(ctx)
			defer session.Close()
			answer, runErr := session.Run(runCtx, command, func(result workerproto.Result) error {
				err := sink(result)
				if result.Kind == "tool" && result.Status == "succeeded" && ending == "interrupted" {
					cancel()
				}
				return err
			}, nil, nil)
			if (runErr == nil) != (ending == "succeeded") {
				t.Fatalf("unexpected first run error: %v", runErr)
			}
			var output []store.ModelOutputRef
			if ending == "succeeded" {
				output = append(output, final)
			} else {
				answer = "Execution failed."
			}
			if _, err := db.FinishRun(ctx, "session", "first", "a1", ending, answer, output...); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			user, _, err = db.BeginRun(ctx, "session", "second", "u2", "Where did that evidence come from?")
			if err != nil {
				t.Fatal(err)
			}
			command.RunID, command.MessageSequence, command.Prompt = "second", user.Sequence, user.Content
			answer, err = session.Run(ctx, command, historyTestSink(db, command, &final), nil, nil)
			if err != nil || !verified.Load() || answer != "The file tool supplied the evidence." {
				t.Fatalf("follow-up: answer=%q verified=%t error=%v", answer, verified.Load(), err)
			}
		})
	}
}

func TestInterjectedRepliesAndLongToolHistoryRestoreInOrder(t *testing.T) {
	db, ctx := checkpointFixture(t)
	evidence := strings.Repeat("verified source line\n", 700) + "SOURCE_END"
	path := filepath.Join(t.TempDir(), "long-source.txt")
	if err := os.WriteFile(path, []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	longAnswer := strings.Repeat("Reply detail. ", 900) + "ANSWER_END"
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"path": path, "lineCount": 1000})
			chunk, _ := json.Marshal(mcpToolEvent("long-read", "read_file", string(args)))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		} else if requests.Load() == 2 {
			historyTextResponse(w, longAnswer)
		} else {
			historyTextResponse(w, "Final reply.")
		}
	}))
	defer upstream.Close()
	user, _, err := db.BeginRun(ctx, "session", "first", "u1", "Read the full source.")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "first", MessageSequence: user.Sequence, Prompt: user.Content,
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
	var final store.ModelOutputRef
	interjected := false
	next := func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
		if reply == nil || interjected {
			return nil, nil
		}
		interjected = true
		if _, _, err := db.QueueInput(ctx, "session", "follow-up", "now", "Add the new constraint."); err != nil {
			return nil, err
		}
		message, err := db.DeliverInput(ctx, "session", "first", "follow-up", "u2", store.CompletedReply{ID: "a1", Content: reply.Content, Output: store.ModelOutputRef{Turn: reply.Turn, Bytes: reply.FullBytes}})
		if err != nil {
			return nil, err
		}
		return &workerproto.Command{RunID: "first", InputID: "follow-up", MessageSequence: message.Sequence, Prompt: message.Content}, nil
	}
	answer, err := RunWithInputSource(ctx, command, historyTestSink(db, command, &final), next, nil)
	if err != nil || !interjected || answer != "Final reply." {
		t.Fatalf("answer=%q interjected=%t error=%v", answer, interjected, err)
	}
	if _, err := db.FinishRun(ctx, "session", "first", "a2", "succeeded", answer, final); err != nil {
		t.Fatal(err)
	}
	// A second completion frame must not add a second final reply or history.
	if event, err := db.FinishRun(ctx, "session", "first", "duplicate", "succeeded", answer, final); err != nil || event != nil {
		t.Fatalf("duplicate finish: %v %v", event, err)
	}
	user, _, err = db.BeginRun(ctx, "session", "next", "u3", "Continue.")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence, command.Prompt = "next", user.Sequence, user.Content
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil {
		t.Fatal(err)
	}
	roles := make([]string, len(loaded))
	for i, m := range loaded {
		roles[i] = m.Role
	}
	want := []string{"user", "assistant", "tool", "assistant", "user", "assistant"}
	if !reflect.DeepEqual(roles, want) || loaded[3].Content != longAnswer || loaded[4].Content != "Add the new constraint." || loaded[5].Content != "Final reply." || !strings.Contains(loaded[2].Content, "SOURCE_END") {
		t.Fatalf("interjection history was reordered, truncated or duplicated: roles=%v messages=%d", roles, len(loaded))
	}
	// Idle estimates and manual compaction must read the same retained tools.
	command.Action = "compact"
	before, err := EstimateSessionContext(ctx, command)
	if err != nil || before < 5000 {
		t.Fatalf("tool content missing from idle estimate: tokens=%d error=%v", before, err)
	}
	// Finish the unexecuted follow-up before maintenance.
	if _, err := db.FinishRun(ctx, "session", "next", "unused", "interrupted", ""); err != nil {
		t.Fatal(err)
	}
	through, _, err := db.BeginCompaction(ctx, "session", "compact")
	if err != nil {
		t.Fatal(err)
	}
	var sourceSeen atomic.Bool
	summaryModel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := new(strings.Builder)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, value := range body {
			raw.Write(value)
		}
		if strings.Contains(raw.String(), "SOURCE_END") {
			sourceSeen.Store(true)
		}
		historyTextResponse(w, "Verified source and completed replies retained in summary.")
	}))
	defer summaryModel.Close()
	command.RunID, command.MessageSequence, command.Model.BaseURL = "compact", through, summaryModel.URL
	if err := CompactSession(ctx, command, historyTestSink(db, command, &final)); err != nil {
		t.Fatal(err)
	}
	if !sourceSeen.Load() {
		t.Fatal("summary did not receive the tool's source evidence")
	}
	if _, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	after, err := EstimateSessionContext(ctx, command)
	if err != nil || after >= before {
		t.Fatalf("compaction estimate before=%d after=%d error=%v", before, after, err)
	}
	loaded, err = loadConversationContext(ctx, command, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range loaded {
		if len(m.ToolCalls) != 0 || m.Role == "tool" {
			t.Fatal("tool history before the checkpoint was appended again")
		}
	}
}

func TestInterruptedHistoryPreservesCompletedResultsAndOpaqueState(t *testing.T) {
	db, ctx := checkpointFixture(t)
	user, _, err := db.BeginRun(ctx, "session", "first", "u1", "Inspect two files.")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "first", MessageSequence: user.Sequence,
		Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", BaseURL: "https://original.invalid"}}
	group := opaqueCompactionGroup("complete")
	missing := group[0].ToolCalls[0]
	missing.ID = "unconfirmed"
	group[0].ToolCalls = append(group[0].ToolCalls, missing)
	group[1].Resources = []workerproto.Image{putResourceFixture(t, []byte("retained image"), "image/png")}
	sink := historyTestSink(db, command, new(store.ModelOutputRef))
	if err := retainModelMessage(command, 1, "assistant", 0, group[0], 0, sink); err != nil {
		t.Fatal(err)
	}
	if err := retainModelMessage(command, 1, "tool", 0, group[1], 0, sink); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InterruptRunning(ctx, "session", "Worker exited"); err != nil {
		t.Fatal(err)
	}
	user, _, err = db.BeginRun(ctx, "session", "next", "u2", "What was verified?")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence = "next", user.Sequence
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil || len(loaded) != 4 {
		t.Fatalf("interrupted history: len=%d error=%v", len(loaded), err)
	}
	if !reflect.DeepEqual(loaded[1], group[0]) || !reflect.DeepEqual(loaded[2], group[1]) || loaded[3].ToolCallID != "unconfirmed" || !strings.Contains(loaded[3].Content, "unknown") {
		t.Fatal("lost provider state, confirmed evidence or the explicit unknown outcome")
	}
	command.Model.BaseURL = "https://another.invalid"
	changed, err := loadConversationContext(ctx, command, 0)
	if err != nil || len(changed[1].ResponseOutput) != 0 || len(changed[1].AnthropicBlocks) != 0 || changed[1].ToolCalls[0].ThoughtSignature != "" || changed[2].Content != group[1].Content || len(changed[2].Resources) != 1 {
		t.Fatalf("provider switch lost portable history or retained signatures: error=%v", err)
	}
}

func TestAutomaticCompactionKeepsItsHistoryBoundaryAcrossNextRun(t *testing.T) {
	db, ctx := checkpointFixture(t)
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("verified source evidence ", 250)), 0600); err != nil {
		t.Fatal(err)
	}
	var calls, summaries atomic.Int32
	var resumed []provider.ChatMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if strings.HasPrefix(body.Messages[0].Content, executionSummaryPrompt) {
			summaries.Add(1)
			historyTextResponse(w, "Earlier tool reads verified the source evidence.")
			return
		}
		n := calls.Add(1)
		if n == 4 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
			return
		}
		if n <= 3 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(mcpToolEvent(fmt.Sprintf("read-%d", n), "read_file", string(args)))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		resumed = body.Messages[1:]
		historyTextResponse(w, "Verified conclusion.")
	}))
	defer upstream.Close()
	user, _, err := db.BeginRun(ctx, "session", "first", "u1", "Inspect and retain the source evidence.")
	if err != nil {
		t.Fatal(err)
	}
	command := workerproto.Command{SessionID: "session", RunID: "first", MessageSequence: user.Sequence, Prompt: user.Content,
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
	var final store.ModelOutputRef
	answer, err := Run(ctx, command, historyTestSink(db, command, &final))
	if err != nil || summaries.Load() == 0 || calls.Load() != 5 {
		t.Fatalf("automatic recovery: answer=%q requests=%d summaries=%d error=%v", answer, calls.Load(), summaries.Load(), err)
	}
	if _, err := db.FinishRun(ctx, "session", "first", "a1", "succeeded", answer, final); err != nil {
		t.Fatal(err)
	}
	user, _, err = db.BeginRun(ctx, "session", "next", "u2", "Continue.")
	if err != nil {
		t.Fatal(err)
	}
	command.RunID, command.MessageSequence = "next", user.Sequence
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := append(resumed, provider.ChatMessage{Role: "assistant", Content: "Verified conclusion."})
	// Formal user inputs have empty resource slices; normalize that wire-only
	// difference before comparing the actual request to restored history.
	for i := range loaded {
		if len(loaded[i].Resources) == 0 {
			loaded[i].Resources = nil
		}
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("checkpoint boundary lost or duplicated model history: restored=%d expected=%d", len(loaded), len(want))
	}
}
