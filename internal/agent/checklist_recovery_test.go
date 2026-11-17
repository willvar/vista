package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func seedOpenChecklist(t *testing.T, db *store.Store, command workerproto.Command) {
	t.Helper()
	if _, err := db.ReplaceChecklist(context.Background(), command.SessionID, command.RunID, []store.ChecklistItem{{Text: "Verify output", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
}

func TestChecklistRecoveryStopsWithoutProgress(t *testing.T) {
	for _, entry := range []string{"plain", "cosmetic finish"} {
		t.Run(entry, func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if entry == "cosmetic finish" {
					args := fmt.Sprintf(`{"items":[{"text":"Private unfinished item %d","status":"pending"}],"finish":true}`, n)
					checklistTextTurn(w, "Finished.", scriptedToolCall(fmt.Sprintf("finish-%d", n), "update_checklist", args))
				} else {
					continuationAnswer(w, "Finished.")
				}
			}))
			defer model.Close()
			command := checklistRunFixture(t, db, model.URL)
			seedOpenChecklist(t, db, command)
			var final store.ModelOutputRef
			answer, err := Run(ctx, command, checklistHistorySink(db, command, &final))
			if err == nil || !strings.Contains(err.Error(), "unfinished work") || strings.Contains(err.Error(), "Private unfinished") || answer != "" || final.Turn != 0 || requests.Load() != 3 {
				t.Fatalf("unbounded or falsely successful recovery: answer=%q requests=%d final=%+v err=%v", answer, requests.Load(), final, err)
			}
			items, readErr := db.RunChecklist(ctx, command.SessionID, command.RunID)
			if readErr != nil || len(items) != 1 || items[0].Status != "pending" {
				t.Fatalf("runtime rewrote unfinished state to finish: %+v %v", items, readErr)
			}
		})
	}
}

func TestChecklistRecoveryUsefulWorkIsNotACallBudget(t *testing.T) {
	db, ctx := checkpointFixture(t)
	path := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(path, []byte("Existing verification evidence."), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": path})
	var requests, reads atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 9 {
			checklistTextTurn(w, "Verified.", scriptedToolCall("resolved", "update_checklist", `{"items":[{"text":"Verify output","status":"done"}],"finish":true}`))
		} else if n%2 == 0 {
			checklistTextTurn(w, "Reading evidence.", scriptedToolCall(fmt.Sprintf("read-%d", n), "read_file", string(args)))
		} else {
			continuationAnswer(w, "Attempt to finish.")
		}
	}))
	defer model.Close()
	command := checklistRunFixture(t, db, model.URL)
	seedOpenChecklist(t, db, command)
	var final store.ModelOutputRef
	sink := checklistHistorySink(db, command, &final)
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "tool" && result.Tool.Name == "read_file" && result.Status == "succeeded" {
			reads.Add(1)
		}
		return sink(result)
	})
	if err != nil || answer != "Verified." || requests.Load() != 9 || reads.Load() != 4 {
		t.Fatalf("real tool results did not refresh correction opportunity: answer=%q requests=%d reads=%d err=%v", answer, requests.Load(), reads.Load(), err)
	}
}

func TestChecklistRecoveryIgnoresFailedReplacement(t *testing.T) {
	db, ctx := checkpointFixture(t)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			checklistTextTurn(w, "Finished.", scriptedToolCall("failed-save", "update_checklist", `{"items":[{"text":"Verify output","status":"done"}],"finish":true}`))
		case 2:
			continuationAnswer(w, "Finished anyway.")
		default:
			var body struct{ Messages []provider.ChatMessage }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, `"status":"pending"`) {
				t.Error("failed checklist save replaced the authoritative state")
			}
			checklistTextTurn(w, "Verification is unavailable.", scriptedToolCall("blocked", "update_checklist", `{"items":[{"text":"Verify output","status":"blocked"}],"finish":true}`))
		}
	}))
	defer model.Close()
	command := checklistRunFixture(t, db, model.URL)
	seedOpenChecklist(t, db, command)
	var final store.ModelOutputRef
	sink := checklistHistorySink(db, command, &final)
	var saves atomic.Int32
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "checklist" && saves.Add(1) == 1 {
			return errors.New("injected checklist save failure")
		}
		return sink(result)
	})
	if err != nil || answer != "Verification is unavailable." || requests.Load() != 3 {
		t.Fatalf("failed replacement bypassed completion checks: answer=%q requests=%d err=%v", answer, requests.Load(), err)
	}
}

func TestChecklistRecoveryPreservesStopBoundaries(t *testing.T) {
	for _, reason := range []string{"cancel", "request failure", "question", "notice save failure"} {
		t.Run(reason, func(t *testing.T) {
			db, fixtureCtx := checkpointFixture(t)
			ctx, cancel := context.WithCancel(fixtureCtx)
			defer cancel()
			var requests, corrections atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch reason {
				case "request failure":
					http.Error(w, "permanent request failure", http.StatusBadRequest)
				case "question":
					checklistTextTurn(w, "Need the target.", scriptedToolCall("ask", "question", `{"questions":[{"id":"target","question":"Which target?","options":[]}]}`))
				default:
					continuationAnswer(w, "Attempt to finish.")
				}
			}))
			defer model.Close()
			command := checklistRunFixture(t, db, model.URL)
			seedOpenChecklist(t, db, command)
			var final store.ModelOutputRef
			sink := checklistHistorySink(db, command, &final)
			_, err := Run(ctx, command, func(result workerproto.Result) error {
				if reason == "cancel" && result.Kind == "model_result" {
					cancel()
				}
				if result.Kind == "model_history" && result.Status == "join" {
					corrections.Add(1)
					if reason == "notice save failure" {
						return errors.New("injected correction save failure")
					}
				}
				return sink(result)
			})
			if requests.Load() != 1 || final.Turn != 0 || reason == "question" && err != nil || reason != "question" && err == nil {
				t.Fatalf("stop boundary was overridden: requests=%d final=%+v err=%v", requests.Load(), final, err)
			}
			wantCorrections := int32(0)
			if reason == "notice save failure" {
				wantCorrections = 1
			}
			if corrections.Load() != wantCorrections {
				t.Fatalf("unexpected corrections at stop boundary: %d", corrections.Load())
			}
		})
	}
}

func TestChecklistRecoveryRestoresCommittedStateAndContext(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%t", compact), func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if n == 1 {
					continuationAnswer(w, "Original attempted final answer.")
					return
				}
				if compact && n == 2 {
					continuationAnswer(w, "The implementation exists; output verification is still pending.")
					return
				}
				var body struct{ Messages []provider.ChatMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				last := body.Messages[len(body.Messages)-1]
				if !strings.Contains(last.Content, "Verify output") || !strings.Contains(last.Content, "pending") {
					t.Error("restoration lost the current run checklist")
				}
				resumeTurn := int32(2)
				if compact {
					resumeTurn = 3
				}
				if n == resumeTurn {
					if !compact {
						original, corrections := 0, 0
						for _, message := range body.Messages {
							if message.Role == "assistant" && message.Content == "Original attempted final answer." {
								original++
							}
							if strings.HasPrefix(message.Content, checklistCompletionPrefix) {
								corrections++
							}
						}
						if original != 1 || corrections != 1 {
							t.Errorf("completion history lost or duplicated: original=%d corrections=%d", original, corrections)
						}
					}
					continuationAnswer(w, "Another premature answer.")
					return
				}
				checklistTextTurn(w, "Verified after recovery.", scriptedToolCall("resolved", "update_checklist", `{"items":[{"text":"Verify output","status":"done"}],"finish":true}`))
			}))
			defer model.Close()
			command := checklistRunFixture(t, db, model.URL)
			seedOpenChecklist(t, db, command)
			var final store.ModelOutputRef
			sink := checklistHistorySink(db, command, &final)
			_, err := Run(ctx, command, func(result workerproto.Result) error {
				if err := sink(result); err != nil {
					return err
				}
				if result.Kind == "model_history" && result.Status == "join" {
					return errors.New("injected loss after correction commit")
				}
				return nil
			})
			if err == nil || requests.Load() != 1 {
				t.Fatalf("failed correction commit did not stop inference: requests=%d err=%v", requests.Load(), err)
			}
			nextExecution := func(id, prompt, action string) {
				t.Helper()
				if _, err := db.FinishRun(ctx, command.SessionID, command.RunID, "end-"+command.RunID, "interrupted", "test execution boundary"); err != nil {
					t.Fatal(err)
				}
				user, _, err := db.BeginRun(ctx, command.SessionID, id, "input-"+id, prompt)
				if err != nil {
					t.Fatal(err)
				}
				command.RunID, command.MessageSequence, command.Prompt, command.Action = id, user.Sequence, user.Content, action
				sink = checklistHistorySink(db, command, &final)
			}
			if compact {
				nextExecution("maintenance", "Compact the retained history.", "compact")
				if err := CompactSession(ctx, command, sink); err != nil {
					t.Fatal(err)
				}
			}
			nextExecution("resumed", "Continue using the recorded evidence.", "resume")
			items, err := db.RunChecklist(ctx, command.SessionID, command.RunID)
			if err != nil || len(items) != 0 {
				t.Fatalf("a new execution inherited the old checklist: %+v %v", items, err)
			}
			// Model-history restoration and current-run checklist loading are
			// separate contracts; a fresh run does not inherit a prior plan.
			seedOpenChecklist(t, db, command)
			answer, err := Run(ctx, command, sink)
			want := int32(3)
			if compact {
				want = 4
			}
			if err != nil || answer != "Verified after recovery." || requests.Load() != want {
				t.Fatalf("restored checklist was not enforced: answer=%q requests=%d err=%v", answer, requests.Load(), err)
			}
		})
	}
}

func TestChecklistRecoveryStateChangesAndTaskScope(t *testing.T) {
	var completion checklistCompletion
	completion.replace([]workerproto.ChecklistItem{{Text: "First name", Status: "pending"}})
	for range 2 {
		if notice, err := completion.check(); err != nil || !notice.RuntimeData {
			t.Fatalf("missing initial correction: %+v %v", notice, err)
		}
	}
	completion.observe(workerproto.Result{TaskID: "main/child", Depth: 1, Kind: "tool", Status: "succeeded", Tool: &workerproto.ToolProgress{Name: "read_file"}})
	completion.observe(workerproto.Result{TaskID: "main", Kind: "tool", Status: "succeeded", Tool: &workerproto.ToolProgress{Name: "update_checklist"}})
	completion.replace([]workerproto.ChecklistItem{{Text: "Cosmetic rename", Status: "pending"}})
	if _, err := completion.check(); err == nil {
		t.Fatal("child activity or cosmetic checklist updates reset the correction limit")
	}
	completion.replace([]workerproto.ChecklistItem{{Text: "Cosmetic rename", Status: "in_progress"}})
	if _, err := completion.check(); err != nil {
		t.Fatal("pending to in-progress transition did not refresh corrections", err)
	}
	completion.replace([]workerproto.ChecklistItem{{Text: "Cosmetic rename", Status: "pending"}})
	completion.replace([]workerproto.ChecklistItem{{Text: "Cosmetic rename", Status: "in_progress"}})
	if _, err := completion.check(); err != nil {
		t.Fatal(err)
	}
	if _, err := completion.check(); err == nil {
		t.Fatal("oscillating statuses counted the same progress repeatedly")
	}
}
