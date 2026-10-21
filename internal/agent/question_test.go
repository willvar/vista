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

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/question"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestQuestionToolYieldsStructuredRequest(t *testing.T) {
	root, _ := continuationFixture(t)
	state := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Create(context.Background(), "session", "question"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	const arguments = `{"questions":[{"header":"Language","question":"Which language?","options":[{"label":"Go","description":"Existing backend"},{"label":"Rust"}],"multiple":true}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		found := false
		for _, tool := range input.Tools {
			found = found || tool.Function.Name == "question"
		}
		if !found {
			t.Error("question tool was not advertised")
		}
		continuationEvent(w, mcpToolEvent("ask", "question", arguments))
	}))
	defer upstream.Close()
	command := continuationCommand(upstream.URL, "choose")
	command.SessionID = "session"
	var pending *workerproto.PendingReply
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "question" {
			pending = event.Pending
		}
		return nil
	})
	if err != nil || pending == nil || pending.Kind != "question" || len(pending.Questions) != 1 || !pending.Questions[0].Multiple || pending.Content != answer || !strings.Contains(answer, "Go: Existing backend") {
		t.Fatalf("question yield: answer=%q pending=%+v err=%v", answer, pending, err)
	}
}

func TestQuestionHistoryPairsAnswersAcrossRestartCompactionAndFollowingRuns(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			if _, _, err := db.BeginRun(ctx, "session", "older", "older-user", "Earlier task"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.FinishRun(ctx, "session", "older", "older-answer", "succeeded", strings.Repeat("Verified older evidence. ", 400)); err != nil {
				t.Fatal(err)
			}
			const arguments = `{"questions":[{"question":"Which parts?","options":[{"label":"Backend"},{"label":"Frontend"}],"multiple":true},{"question":"Any constraints?","options":[]}]}`
			var calls, summaries atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Instructions string           `json:"instructions"`
					Input        []map[string]any `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Instructions == executionSummaryPrompt {
					summaries.Add(1)
					compactionResponse(w, []any{compactionText("Earlier evidence retained.")})
					return
				}
				switch calls.Add(1) {
				case 1:
					compactionResponse(w, []any{
						map[string]any{"type": "reasoning", "id": "reasoning", "summary": []any{}, "encrypted_content": "retained-question-reasoning"},
						map[string]any{"type": "function_call", "id": "question-item", "call_id": "ask", "name": "question", "arguments": arguments},
					})
				default:
					questions, answers, signatures, visibleAnswers := 0, 0, 0, 0
					for _, item := range request.Input {
						if item["type"] == "function_call" && item["call_id"] == "ask" && item["name"] == "question" && item["arguments"] == arguments {
							questions++
						}
						if item["type"] == "function_call_output" && item["call_id"] == "ask" {
							output, _ := item["output"].(string)
							if strings.Contains(output, "Which parts?\n\nBackend; Frontend") && strings.Contains(output, "Any constraints?\n\nKeep the API stable") {
								answers++
							}
						}
						if item["encrypted_content"] == "retained-question-reasoning" {
							signatures++
						}
						if item["role"] == "user" && strings.Contains(fmt.Sprint(item["content"]), "Keep the API stable") {
							visibleAnswers++
						}
					}
					if questions != 1 || answers != 1 || signatures != 1 || visibleAnswers != 0 {
						t.Errorf("question replay lost/duplicated the pair or provider state: calls=%d results=%d signatures=%d userAnswers=%d", questions, answers, signatures, visibleAnswers)
					}
					compactionResponse(w, []any{compactionText("Plan ready.")})
				}
			}))
			defer upstream.Close()
			user, _, err := db.BeginRun(ctx, "session", "ask-run", "ask-user", "Ask me before choosing.")
			if err != nil {
				t.Fatal(err)
			}
			command := workerproto.Command{SessionID: "session", RunID: "ask-run", MessageSequence: user.Sequence, Prompt: user.Content,
				Model: modelcfg.Model{Provider: "openai-responses", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
			var final store.ModelOutputRef
			var pending *workerproto.PendingReply
			sink := historyTestSink(db, command, &final)
			answer, err := Run(ctx, command, func(result workerproto.Result) error {
				if result.Kind == "question" {
					pending = result.Pending
				}
				return sink(result)
			})
			if err != nil || pending == nil || pending.CallID != "ask" {
				t.Fatalf("question did not yield: %+v %v", pending, err)
			}
			if _, err := db.FinishRunWithPending(ctx, "session", "ask-run", "ask-display", "succeeded", answer,
				&store.PendingReply{CallID: pending.CallID, Kind: pending.Kind, Content: pending.Content, Questions: pending.Questions}); err != nil {
				t.Fatal(err)
			}
			// Idle estimation and maintenance must leave the unresolved call intact.
			command.Action = "compact"
			prepared, err := prepareSessionContext(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			last := prepared.messages[len(prepared.messages)-1]
			if last.Role != "assistant" || len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "ask" || !prepared.awaitingUser {
				t.Fatalf("pending question was marked interrupted: %+v", last)
			}
			if compact {
				command.MessageSequence, _, err = db.BeginCompaction(ctx, "session", "compact")
				command.RunID = "compact"
				if err != nil {
					t.Fatal(err)
				}
				if err := CompactSession(ctx, command, historyTestSink(db, command, &final)); err != nil {
					t.Fatal(err)
				}
				if _, err := db.FinishCompaction(ctx, "session", "compact", "succeeded", ""); err != nil || summaries.Load() == 0 {
					t.Fatalf("pending compaction: summaries=%d error=%v", summaries.Load(), err)
				}
			}
			// A fresh Agent and reopened database simulate a restart while waiting.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "vista", "state", "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			session, err := db.Get(ctx, "session")
			if err != nil || session.Pending == nil || session.Pending.CallID != "ask" {
				t.Fatalf("pending lost: %+v %v", session, err)
			}
			reply := question.Reply{RunID: "ask-run", Answers: []question.Answer{{Options: []string{"Backend", "Frontend"}}, {Text: "Keep the API stable"}}}
			user, _, err = db.BeginQuestionRun(ctx, "session", "answer-run", "answer-user", reply)
			if err != nil {
				t.Fatal(err)
			}
			answerInput := user
			command.Action, command.RunID, command.MessageSequence, command.Prompt, command.QuestionCallID = "", "answer-run", user.Sequence, user.Content, user.QuestionCallID
			answer, err = Run(ctx, command, historyTestSink(db, command, &final))
			if err != nil || calls.Load() != 2 || answer != "Plan ready." {
				t.Fatalf("answer continuation: %q %v requests=%d", answer, err, calls.Load())
			}
			if _, err := db.FinishRun(ctx, "session", "answer-run", "answer-display", "succeeded", answer, final); err != nil {
				t.Fatal(err)
			}
			user, _, err = db.BeginRun(ctx, "session", "following", "following-user", "Continue.")
			if err != nil {
				t.Fatal(err)
			}
			command.RunID, command.MessageSequence, command.Prompt, command.QuestionCallID = "following", user.Sequence, user.Content, ""
			answer, err = Run(ctx, command, historyTestSink(db, command, &final))
			if err != nil || calls.Load() != 3 {
				t.Fatalf("historical answer continuation: %v requests=%d", err, calls.Load())
			}
			if _, err := db.FinishRun(ctx, "session", "following", "following-answer", "succeeded", answer, final); err != nil {
				t.Fatal(err)
			}
			// Withdrawing the answer restores the same unanswered call, even when
			// it lives in a checkpoint created while waiting for the user.
			if _, err := db.Revert(ctx, "session", answerInput.ID, answerInput.Sequence); err != nil {
				t.Fatal(err)
			}
			command.Action, command.MessageSequence = "compact", answerInput.Sequence
			prepared, err = prepareSessionContext(ctx, command)
			if err != nil || !prepared.awaitingUser {
				t.Fatalf("rollback lost pending question: %v", err)
			}
			last = prepared.messages[len(prepared.messages)-1]
			if last.Role != "assistant" || len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "ask" {
				t.Fatalf("rolled back answer remained in model context: %+v", last)
			}
			reply.Answers = []question.Answer{{Options: []string{"Backend"}}, {Text: "Use the revised constraint"}}
			user, _, err = db.BeginQuestionRun(ctx, "session", "revised", "revised-user", reply)
			if err != nil {
				t.Fatal(err)
			}
			command.Action, command.RunID, command.MessageSequence, command.Prompt, command.QuestionCallID = "", "revised", user.Sequence, user.Content, user.QuestionCallID
			prepared, err = prepareContext(ctx, command, nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			last = prepared.messages[len(prepared.messages)-1]
			if last.Role != "tool" || last.ToolCallID != "ask" || !strings.Contains(last.Content, "Use the revised constraint") || strings.Contains(last.Content, "Keep the API stable") {
				t.Fatalf("revised answer not paired: %+v", last)
			}
		})
	}
}

func TestQuestionHistorySupersededOrInterruptedRemainsPaired(t *testing.T) {
	for _, ending := range []string{"succeeded", "failed", "interrupted"} {
		t.Run(ending, func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			user, _, err := db.BeginRun(ctx, "session", "ask-run", "u1", "Choose.")
			if err != nil {
				t.Fatal(err)
			}
			const args = `{"questions":[{"question":"Which path?","options":[{"label":"src"}]}]}`
			call := scriptedToolCall("ask", "question", args)
			command := workerproto.Command{SessionID: "session", RunID: "ask-run", MessageSequence: user.Sequence}
			if err := retainModelMessage(command, 1, "question", 0, provider.ChatMessage{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, 0, historyTestSink(db, command, new(store.ModelOutputRef))); err != nil {
				t.Fatal(err)
			}
			var pending *store.PendingReply
			content := "Run failed before yielding."
			if ending == "succeeded" {
				request, err := questionRequest(call)
				if err != nil {
					t.Fatal(err)
				}
				pending = &store.PendingReply{CallID: request.CallID, Kind: request.Kind, Content: request.Content, Questions: request.Questions}
				content = request.Content
			}
			if _, err := db.FinishRunWithPending(ctx, "session", "ask-run", "a1", ending, content, pending); err != nil {
				t.Fatal(err)
			}
			user, _, err = db.BeginRun(ctx, "session", "next", "u2", "Use the default instead.")
			if err != nil {
				t.Fatal(err)
			}
			command.RunID, command.MessageSequence, command.Prompt = "next", user.Sequence, user.Content
			prepared, err := prepareContext(ctx, command, nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			calls, results := 0, 0
			for i, message := range prepared.messages {
				calls += len(message.ToolCalls)
				if message.Role == "tool" && message.ToolCallID == "ask" {
					results++
					if !strings.Contains(message.Content, `"unanswered"`) || prepared.messages[i-1].Role != "assistant" {
						t.Fatalf("unanswered result reordered: %+v", prepared.messages)
					}
				}
			}
			if calls != 1 || results != 1 || prepared.messages[len(prepared.messages)-1].Content != user.Content {
				t.Fatalf("interrupted question lost its boundary: calls=%d results=%d", calls, results)
			}
		})
	}
}
