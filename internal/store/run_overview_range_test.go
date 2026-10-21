package store

import (
	"encoding/json"
	"errors"
	"testing"

	"vista/internal/workerproto"
)

func TestRunOverviewRangeSeparatesRepliesToolsAndChecklists(t *testing.T) {
	st, ctx := executionStore(t)
	appendProgress := func(kind string, result workerproto.Result) {
		t.Helper()
		result.RunID, result.Kind = "run", kind
		if _, err := st.AppendRunEvent(ctx, "session", "run", kind, result); err != nil {
			t.Fatal(err)
		}
	}
	request := func(turn int32) {
		t.Helper()
		if _, err := st.RecordModelRequestEvent(ctx, "session", workerproto.Result{RunID: "run", Turn: turn, TaskID: "main", ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}}); err != nil {
			t.Fatal(err)
		}
	}
	tool := func(call, status string) {
		appendProgress("tool", workerproto.Result{Status: status, TaskID: "main", Tool: &workerproto.ToolProgress{CallID: call, Name: "price", Summary: call}})
	}
	appendProgress("agent_task", workerproto.Result{Task: &workerproto.AgentTask{ID: "main", Title: "original", Status: "started"}})
	request(1)
	tool("first-price", "started")
	tool("first-price", "succeeded")
	if _, err := st.ReplaceChecklist(ctx, "session", "run", []ChecklistItem{{Text: "first price", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	const firstAnswer = "The first price is 10."
	if _, err := st.AppendModelDelta(ctx, "session", "run", 1, 0, firstAnswer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.QueueInput(ctx, "session", "follow-up", "now", "Check another price."); err != nil {
		t.Fatal(err)
	}
	followup, err := st.DeliverInput(ctx, "session", "run", "follow-up", "second-user", CompletedReply{ID: "first-reply", Content: firstAnswer, Output: ModelOutputRef{Turn: 1, Bytes: len(firstAnswer)}})
	if err != nil || followup == nil {
		t.Fatalf("follow-up: %+v %v", followup, err)
	}
	boundary := followup.Sequence - 1
	request(2)
	tool("second-price", "started")
	tool("second-price", "failed")
	request(3)
	if _, err := st.ReplaceChecklist(ctx, "session", "run", []ChecklistItem{{Text: "second price", Status: "blocked"}}); err != nil {
		t.Fatal(err)
	}
	appendProgress("agent_task", workerproto.Result{Task: &workerproto.AgentTask{ID: "main", Status: "succeeded"}})
	finished, err := st.FinishRun(ctx, "session", "run", "second-reply", "succeeded", "The second price is unavailable.")
	if err != nil || finished == nil {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	first, err := st.RunOverviewRange(ctx, "session", "run", 0, boundary)
	if err != nil || first.ModelCalls != 1 || first.FirstSequence != 1 || first.ThroughSequence != boundary || first.FinishedAt == nil || *first.FinishedAt != first.LastActivityAt {
		t.Fatalf("first reply snapshot: %+v %v", first, err)
	}
	second, err := st.RunOverviewRange(ctx, "session", "run", boundary, 0)
	if err != nil || second.ModelCalls != 2 || second.FirstSequence != followup.Sequence || second.ThroughSequence != finished.Sequence || second.FinishedAt == nil || second.StartedAt == first.StartedAt || second.LastActivityAt != finished.CreatedAt {
		t.Fatalf("second reply snapshot: %+v %v", second, err)
	}
	for index, snapshot := range []RunOverview{first, second} {
		wantCall, wantChecklist, wantTaskStatus := "first-price", "first price", "started"
		if index == 1 {
			wantCall, wantChecklist, wantTaskStatus = "second-price", "second price", "succeeded"
		}
		tools, checklists, tasks := 0, 0, 0
		for _, event := range snapshot.Events {
			if index == 0 && event.Sequence > boundary || index == 1 && event.Sequence <= boundary {
				t.Fatalf("event crossed reply boundary: %+v", event)
			}
			var result workerproto.Result
			if err := json.Unmarshal(event.Data, &result); err != nil {
				t.Fatal(err)
			}
			switch event.Kind {
			case "tool":
				tools++
				if result.Tool == nil || result.Tool.CallID != wantCall || result.Status == "started" {
					t.Fatalf("wrong stage tool: %+v", result)
				}
			case "agent_task":
				tasks++
				if result.Task == nil || result.Task.Status != wantTaskStatus {
					t.Fatalf("task state leaked across stages: %+v", result)
				}
			case "checklist":
				checklists++
				var value struct{ Items []ChecklistItem }
				if err := json.Unmarshal(event.Data, &value); err != nil || len(value.Items) != 1 || value.Items[0].Text != wantChecklist {
					t.Fatalf("checklist leaked across stages: %+v %v", value, err)
				}
			}
		}
		if tools != 1 || checklists != 1 || tasks != 1 {
			t.Fatalf("stage %d tools=%d checklists=%d tasks=%d", index, tools, checklists, tasks)
		}
	}
	all, err := st.RunOverview(ctx, "session", "run")
	if err != nil || all.ModelCalls != 3 || all.FinishedAt == nil || *all.FinishedAt != *second.FinishedAt {
		t.Fatalf("whole run snapshot changed: %+v %v", all, err)
	}
	empty, err := st.RunOverviewRange(ctx, "session", "run", finished.Sequence, 0)
	if err != nil || empty.FirstSequence != 0 || empty.ThroughSequence != 0 || empty.ModelCalls != 0 || empty.StartedAt != "" || empty.FinishedAt != nil || len(empty.Events) != 0 {
		t.Fatalf("empty range leaked run data: %+v %v", empty, err)
	}
	for _, bounds := range [][2]int64{{-1, 0}, {0, -1}, {2, 2}, {3, 2}} {
		if _, err := st.RunOverviewRange(ctx, "session", "run", bounds[0], bounds[1]); !errors.Is(err, ErrRunEventRange) {
			t.Fatalf("invalid bounds %v: %v", bounds, err)
		}
	}
}
