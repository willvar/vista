package store

import (
	"encoding/json"
	"testing"

	"vista/internal/workerproto"
)

func TestDetachedTracePersistsAfterForegroundFinalization(t *testing.T) {
	for _, ending := range []string{"succeeded", "interrupted", "failed"} {
		t.Run(ending, func(t *testing.T) {
			st, ctx := executionStore(t)
			task := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: "main/child", ParentTaskID: "main", Depth: 1,
				Task: &workerproto.AgentTask{ID: "main/child", ParentID: "main", Join: "detached", Title: "background", Status: "started"}}
			if event, err := st.RecordAgentTask(ctx, "session", task); err != nil || event == nil {
				t.Fatalf("register: %v %v", event, err)
			}
			tool := workerproto.Result{RunID: "run", Kind: "tool", TaskID: task.TaskID, ParentTaskID: "main", Depth: 1, Status: "started", Tool: &workerproto.ToolProgress{CallID: "child/read", Name: "subagent/read_file"}}
			if _, err := st.AppendRunEvent(ctx, "session", "run", "tool", tool, task.TaskID); err != nil {
				t.Fatal(err)
			}
			finished, err := st.FinishRun(ctx, "session", "run", "answer", ending, "parent answer")
			if err != nil || finished == nil {
				t.Fatalf("finish: %v %v", finished, err)
			}
			var final struct {
				Status       string
				UnknownTools int
			}
			if err := json.Unmarshal(finished.Data, &final); err != nil || final.Status != ending || final.UnknownTools != 0 {
				t.Fatalf("background tool spoiled foreground: %s %v", finished.Data, err)
			}
			sessions, err := st.List(ctx)
			if err != nil || len(sessions) != 1 || !sessions[0].Running || sessions[0].Executing {
				t.Fatalf("session lifetime: %+v %v", sessions, err)
			}
			if _, _, err := st.BeginRun(ctx, "session", "next-run", "next-input", "next"); err != nil {
				t.Fatalf("foreground blocked: %v", err)
			}
			tool.Status = "succeeded"
			if event, err := st.AppendRunEvent(ctx, "session", "run", "tool", tool, task.TaskID); err != nil || event == nil {
				t.Fatalf("late tool: %v %v", event, err)
			}
			// A background task may delegate further after its foreground ended.
			nested := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: "main/child/nested", ParentTaskID: task.TaskID, Depth: 2,
				Task: &workerproto.AgentTask{ID: "main/child/nested", ParentID: task.TaskID, Join: "required", Status: "started"}}
			if event, err := st.RecordAgentTask(ctx, "session", nested); err != nil || event == nil {
				t.Fatalf("register nested: %v %v", event, err)
			}
			request := nested
			request.ModelUse = &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}
			if event, err := st.RecordModelRequestEvent(ctx, "session", request); err != nil || event == nil {
				t.Fatalf("late request: %v %v", event, err)
			}
			if event, err := st.AppendModelDelta(ctx, "session", "run", 3, 2, "late report", nested.TaskID, nested.ParentTaskID); err != nil || event == nil {
				t.Fatalf("late output: %v %v", event, err)
			}
			if event, err := st.RecordArtifact(ctx, Artifact{SessionID: "session", RunID: "run", ID: "artifact", TaskID: nested.TaskID, Status: "started", OutputPath: "/tmp/trace"}); err != nil || event == nil {
				t.Fatalf("late artifact: %v %v", event, err)
			}
			if event, err := st.RecordJob(ctx, Job{SessionID: "session", RunID: "run", ID: "job", TaskID: nested.TaskID, Status: "starting", OutputPath: "/tmp/job"}); err != nil || event == nil {
				t.Fatalf("late job: %v %v", event, err)
			}
			nested.Task.Status, nested.Task.Report = "succeeded", &workerproto.OutputRef{RunID: "run", Turn: 3, Bytes: 11}
			if event, err := st.RecordAgentTask(ctx, "session", nested); err != nil || event == nil {
				t.Fatalf("late completion: %v %v", event, err)
			}
			page, err := st.ReadFullModelOutput(ctx, "session", "run", 3)
			if err != nil || !page.Complete || page.Content != "late report" {
				t.Fatalf("report not readable: %+v %v", page, err)
			}
			for _, id := range []string{"main", "unknown", nested.TaskID} {
				if event, err := st.AppendModelDelta(ctx, "session", "run", 4, 1, "late unowned output", id); err != nil || event != nil {
					t.Fatalf("unowned task %s appended: %v %v", id, event, err)
				}
			}
			task.Task.Status = "succeeded"
			if _, err := st.RecordAgentTask(ctx, "session", task); err != nil {
				t.Fatal(err)
			}
			if event, err := st.RecordAgentTask(ctx, "session", task); err != nil || event != nil {
				t.Fatalf("duplicate completion: %v %v", event, err)
			}
			if event, err := st.FinishRun(ctx, "session", "run", "extra-answer", "succeeded", "late answer"); err != nil || event != nil {
				t.Fatalf("duplicate formal reply: %v %v", event, err)
			}
			messages, err := st.MessagesBefore(ctx, "session", 0, 10)
			want := 3
			if ending == "interrupted" {
				want = 2
			}
			if err != nil || len(messages) != want {
				t.Fatalf("unexpected formal messages: %+v %v", messages, err)
			}
		})
	}
}

func TestWorkerLossInterruptsDetachedTasksOfFinishedRuns(t *testing.T) {
	st, ctx := executionStore(t)
	task := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: "child", ParentTaskID: "main", Depth: 1,
		Task: &workerproto.AgentTask{ID: "child", ParentID: "main", Join: "detached", Status: "started"}}
	if _, err := st.RecordAgentTask(ctx, "session", task); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	events, err := st.InterruptRunning(ctx, "session", "Worker stopped")
	if err != nil || len(events) != 1 || events[0].Kind != "agent_task" {
		t.Fatalf("interrupt background: %+v %v", events, err)
	}
	if active, err := st.ActiveAgentTask(ctx, "session", "run", task.TaskID); err != nil || active {
		t.Fatalf("still active: %t %v", active, err)
	}
	counts, err := st.SessionCounts(ctx)
	if err != nil || counts.Running != 0 {
		t.Fatalf("still running: %+v %v", counts, err)
	}
	if event, err := st.RecordAgentTask(ctx, "session", task); err != nil || event != nil {
		t.Fatalf("old work restarted: %v %v", event, err)
	}
}

func TestMainTaskCompletionDoesNotHideAnUnconfirmedToolFromFinalization(t *testing.T) {
	st, ctx := executionStore(t)
	task := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: "main", Task: &workerproto.AgentTask{ID: "main", Status: "started"}}
	if _, err := st.RecordAgentTask(ctx, "session", task); err != nil {
		t.Fatal(err)
	}
	tool := workerproto.Result{RunID: "run", TaskID: "main", Kind: "tool", Status: "started", Tool: &workerproto.ToolProgress{CallID: "write", Name: "write_file"}}
	if _, err := st.AppendRunEvent(ctx, "session", "run", "tool", tool, "main"); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "succeeded"
	if _, err := st.RecordAgentTask(ctx, "session", task); err != nil {
		t.Fatal(err)
	}
	finished, err := st.FinishRun(ctx, "session", "run", "answer", "succeeded", "claimed success")
	if err != nil || finished == nil {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	var result struct {
		Status       string
		UnknownTools int
	}
	if err := json.Unmarshal(finished.Data, &result); err != nil || result.Status != "failed" || result.UnknownTools != 1 {
		t.Fatalf("false success: %s %v", finished.Data, err)
	}
}
