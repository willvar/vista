package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func checklistRunFixture(t *testing.T, db *store.Store, url string) workerproto.Command {
	t.Helper()
	user, _, err := db.BeginRun(context.Background(), "session", "checklist-run", "input", "Complete and verify the work.")
	if err != nil {
		t.Fatal(err)
	}
	command := continuationCommand(url, user.Content)
	command.SessionID, command.RunID, command.MessageSequence = "session", "checklist-run", user.Sequence
	return command
}

func checklistHistorySink(db *store.Store, command workerproto.Command, final *store.ModelOutputRef) func(workerproto.Result) error {
	sink := historyTestSink(db, command, final)
	return func(result workerproto.Result) error {
		if result.Kind == "checklist" {
			return persistTestChecklist(db, command, result)
		}
		return sink(result)
	}
}

func TestChecklistCompletionBlocksUnfinishedAnswers(t *testing.T) {
	for _, entry := range []string{"plain", "finish", "empty"} {
		for _, status := range []string{"pending", "in_progress"} {
			for _, resolved := range []string{"done", "blocked", "clear"} {
				t.Run(entry+"/"+status+"/"+resolved, func(t *testing.T) {
					db, ctx := checkpointFixture(t)
					var requests atomic.Int32
					model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body struct{ Messages []provider.ChatMessage }
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							return
						}
						n := requests.Add(1)
						if n == 1 {
							args, _ := json.Marshal(map[string]any{"items": []store.ChecklistItem{{Text: "Verify output", Status: status}}, "finish": entry == "finish"})
							checklistTextTurn(w, "Initial response.", scriptedToolCall("initial", "update_checklist", string(args)))
							return
						}
						if entry != "finish" && n == 2 {
							text := "Premature final answer."
							if entry == "empty" {
								text = ""
							}
							continuationAnswer(w, text)
							return
						}
						last := body.Messages[len(body.Messages)-1]
						if last.Role != "user" || !strings.Contains(last.Content, "Verify output") || !strings.Contains(last.Content, status) {
							t.Errorf("unfinished checklist did not reach the next model decision: %+v", last)
						}
						items := []store.ChecklistItem{}
						if resolved != "clear" {
							items = append(items, store.ChecklistItem{Text: "Verify output", Status: resolved})
						}
						args, _ := json.Marshal(map[string]any{"items": items, "finish": true})
						checklistTextTurn(w, "Verified final answer.", scriptedToolCall("resolved", "update_checklist", string(args)))
					}))
					defer model.Close()
					command := checklistRunFixture(t, db, model.URL)
					var final store.ModelOutputRef
					answer, err := Run(ctx, command, checklistHistorySink(db, command, &final))
					wantRequests := int32(3)
					if entry == "finish" {
						wantRequests = 2
					}
					if err != nil || answer != "Verified final answer." || requests.Load() != wantRequests || final.Turn != wantRequests {
						t.Fatalf("unfinished work ended early: answer=%q requests=%d final=%+v err=%v", answer, requests.Load(), final, err)
					}
					items, err := db.RunChecklist(ctx, command.SessionID, command.RunID)
					if err != nil || resolved == "clear" && len(items) != 0 || resolved != "clear" && (len(items) != 1 || items[0].Status != resolved) {
						t.Fatalf("completion did not use the committed replacement: %+v %v", items, err)
					}
				})
			}
		}
	}
}

func TestChecklistCompletionDoesNotSpendRequestsWithoutOpenWork(t *testing.T) {
	for _, status := range []string{"absent", "empty", "done", "blocked"} {
		t.Run(status, func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				continuationAnswer(w, "Complete or blocked.")
			}))
			defer model.Close()
			command := checklistRunFixture(t, db, model.URL)
			if status != "absent" {
				items := []store.ChecklistItem{}
				if status != "empty" {
					items = append(items, store.ChecklistItem{Text: "Work", Status: status})
				}
				if _, err := db.ReplaceChecklist(ctx, command.SessionID, command.RunID, items); err != nil {
					t.Fatal(err)
				}
			}
			var final store.ModelOutputRef
			answer, err := Run(ctx, command, checklistHistorySink(db, command, &final))
			if err != nil || answer != "Complete or blocked." || requests.Load() != 1 {
				t.Fatalf("unnecessary completion request: answer=%q requests=%d err=%v", answer, requests.Load(), err)
			}
		})
	}
}
