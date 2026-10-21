package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/workerproto"
)

func executionStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Create(ctx, "session", "execution"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, "session", "run", "user", "verify execution"); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func TestRunOverviewRetainsTerminalStatusWithoutDuplicatingReply(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			st, ctx := executionStore(t)
			for _, progress := range []workerproto.Result{
				{RunID: "run", Kind: "agent_task", Task: &workerproto.AgentTask{ID: "main", Status: "succeeded"}},
				{RunID: "run", Kind: "source", Content: "https://example.com/source"},
				{RunID: "run", Kind: "context_notice", Content: "Context compacted"},
			} {
				if _, err := st.AppendRunEvent(ctx, "session", "run", progress.Kind, progress); err != nil {
					t.Fatal(err)
				}
			}
			// Older sources and notices must be discoverable without opening the
			// process or relying on the most recent event page.
			for i := 0; i < 105; i++ {
				if _, err := st.AppendModelDelta(ctx, "session", "run", 1, 0, "text"); err != nil {
					t.Fatal(err)
				}
			}
			body := strings.Repeat("Detailed upstream explanation. ", 500)
			finished, err := st.FinishRun(ctx, "session", "run", "reply", status, body)
			if err != nil || finished == nil {
				t.Fatalf("finish: %+v %v", finished, err)
			}
			overview, err := st.RunOverview(ctx, "session", "run")
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, event := range overview.Events {
				counts[event.Kind]++
				if event.Kind == "model_delta" {
					t.Fatal("overview must not load conversation output")
				}
				if event.Kind != "run_finished" {
					continue
				}
				var data map[string]any
				if err := json.Unmarshal(event.Data, &data); err != nil {
					t.Fatal(err)
				}
				if len(data) != 2 || data["runId"] != "run" || data["status"] != status || event.Sequence != finished.Sequence || event.CreatedAt != finished.CreatedAt {
					t.Fatalf("terminal summary must only retain identity, state and event metadata: %+v", event)
				}
			}
			for _, kind := range []string{"agent_task", "source", "context_notice", "run_finished"} {
				if counts[kind] != 1 {
					t.Fatalf("missing overview content %s: %v", kind, counts)
				}
			}
			stored, err := st.EventsAfter(ctx, "session", finished.Sequence-1, 1)
			if err != nil || len(stored) != 1 {
				t.Fatalf("stored terminal event: %+v %v", stored, err)
			}
			var original struct {
				Reason  string
				Message *Message
			}
			if err := json.Unmarshal(stored[0].Data, &original); err != nil {
				t.Fatal(err)
			}
			if status == "interrupted" {
				if original.Reason != body {
					t.Fatal("overview projection changed the stored interruption explanation")
				}
			} else if original.Message == nil || original.Message.Content != body || original.Message.Failure != (status == "failed") {
				t.Fatal("overview projection changed the stored reply")
			}
		})
	}
}

func TestRunOverviewCountsEveryRequestAndKeepsLatestToolState(t *testing.T) {
	st, ctx := executionStore(t)
	firstEvents, err := st.EventsAfter(ctx, "session", 0, 1)
	if err != nil || len(firstEvents) != 1 {
		t.Fatalf("first run event: %+v %v", firstEvents, err)
	}
	firstSequence := firstEvents[0].Sequence
	const calls = 257
	for turn := 1; turn <= calls; turn++ {
		depth, task := 0, "main"
		if turn%2 == 0 {
			depth, task = 1, "child"
		}
		request := workerproto.Result{RunID: "run", Turn: int32(turn), Depth: depth, TaskID: task, ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}}
		if _, err := st.RecordModelRequestEvent(ctx, "session", request); err != nil {
			t.Fatal(err)
		}
	}
	summary := workerproto.Result{RunID: "run", ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "summary-model", Purpose: "summary"}}
	if _, err := st.RecordModelRequestEvent(ctx, "session", summary); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"started", "failed", "succeeded"} {
		result := workerproto.Result{RunID: "run", Kind: "tool", TaskID: "child", ParentTaskID: "main", Status: status, Content: strings.Repeat("测", 500), Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell", Input: &workerproto.ToolInput{Command: "git status", Directory: "/work", TimeoutSeconds: 30}, OutputRef: &workerproto.OutputRef{ArtifactID: "artifact", Bytes: 1500}}}
		if _, err := st.AppendRunEvent(ctx, "session", "run", "tool", result); err != nil {
			t.Fatal(err)
		}
	}
	task := workerproto.Result{RunID: "run", Kind: "agent_task", Task: &workerproto.AgentTask{ID: "child", ParentID: "main", Title: "Verify files", Status: "succeeded"}}
	if _, err := st.AppendRunEvent(ctx, "session", "run", "agent_task", task); err != nil {
		t.Fatal(err)
	}
	last, err := st.AppendModelDelta(ctx, "session", "run", int32(calls+1), 0, "last output", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	overview, err := st.RunOverview(ctx, "session", "run")
	if err != nil {
		t.Fatal(err)
	}
	if overview.ModelCalls != calls || overview.FirstSequence != firstSequence || overview.ThroughSequence != last.Sequence || overview.LastActivityAt != last.CreatedAt || overview.StartedAt == "" || overview.FinishedAt != nil {
		t.Fatalf("incomplete overview: %+v", overview)
	}
	if len(overview.Events) == 0 || overview.Events[0].Sequence <= overview.FirstSequence {
		t.Fatal("run boundary must come from original events, including the first user message omitted from overview")
	}
	latestPage, err := st.EventsBefore(ctx, "session", 0, 100)
	if err != nil || len(latestPage) != 100 || latestPage[0].Sequence <= overview.FirstSequence {
		t.Fatalf("long run fixture does not cross the event page boundary: %+v %v", latestPage, err)
	}
	toolCount := 0
	for _, event := range overview.Events {
		if event.Kind == "model_delta" {
			t.Fatal("overview contains model output")
		}
		if event.CreatedAt == "" {
			t.Fatal("event lost its timestamp")
		}
		if event.Kind != "tool" {
			continue
		}
		toolCount++
		var result workerproto.Result
		if err := json.Unmarshal(event.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "succeeded" || result.TaskID != "child" || result.Tool.OutputRef == nil || !result.Tool.Truncated || len(result.Content) > 512 || !utf8.ValidString(result.Content) {
			t.Fatalf("tool latest metadata lost: %+v", result)
		}
		if result.Tool.Input == nil || result.Tool.Input.Command != "git status" || result.Tool.Input.Directory != "/work" || result.Tool.Input.TimeoutSeconds != 30 {
			t.Fatalf("overview lost shell display metadata: %+v", result.Tool)
		}
	}
	if toolCount != 1 {
		t.Fatalf("expected latest state per call, got %d", toolCount)
	}
	if _, err := st.RunOverview(ctx, "other-session", "run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session overview: %v", err)
	}
	if _, err := st.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	overview, err = st.RunOverview(ctx, "session", "run")
	if err != nil || overview.FinishedAt == nil || overview.ModelCalls != calls || overview.FirstSequence != firstSequence {
		t.Fatalf("finished overview: %+v %v", overview, err)
	}
	_, nextStart, err := st.BeginRun(ctx, "session", "next-run", "next-user", "next request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordModelRequest(ctx, "session", "next-run", "openai-chat", "model"); err != nil {
		t.Fatal(err)
	}
	next, err := st.RunOverview(ctx, "session", "next-run")
	if err != nil || next.FirstSequence != nextStart.Sequence || next.FirstSequence <= overview.ThroughSequence {
		t.Fatalf("first sequence crossed run boundaries: %+v %v", next, err)
	}
}

func TestChildAndAuxiliaryRequestsCannotOverwriteRootModel(t *testing.T) {
	st, ctx := executionStore(t)
	for _, request := range []workerproto.Result{
		{RunID: "run", TaskID: "main", ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "root-model", Key: "root-key"}},
		{RunID: "run", Depth: 1, TaskID: "child", ParentTaskID: "main", Turn: 2, ModelUse: &workerproto.ModelUse{Provider: "openai-responses", ID: "child-model", Key: "child-key"}},
		{RunID: "run", TaskID: "main", ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "summary-model", Purpose: "summary"}},
	} {
		if _, err := st.RecordModelRequestEvent(ctx, "session", request); err != nil {
			t.Fatal(err)
		}
	}
	var model, key string
	if err := st.db.QueryRow("SELECT model_id,model_key FROM runs WHERE id='run'").Scan(&model, &key); err != nil {
		t.Fatal(err)
	}
	if model != "root-model" || key != "root-key" {
		t.Fatalf("root model overwritten: %s %s", model, key)
	}
}

func TestChildReportOutputKeepsTaskMetadataAndCompletion(t *testing.T) {
	st, ctx := executionStore(t)
	const body = "Detailed subtask report"
	event, err := st.AppendModelDelta(ctx, "session", "run", 3, 1, body, "child", "main")
	if err != nil {
		t.Fatal(err)
	}
	var data workerproto.Result
	if err := json.Unmarshal(event.Data, &data); err != nil || data.TaskID != "child" || data.ParentTaskID != "main" {
		t.Fatalf("fragment metadata: %+v %v", data, err)
	}
	page, err := st.ReadModelOutput(ctx, "session", "run", 3, 0, 4096)
	if err != nil || page.Complete {
		t.Fatalf("unfinished report: %+v %v", page, err)
	}
	progress := workerproto.Result{RunID: "run", Kind: "agent_task", Task: &workerproto.AgentTask{ID: "child", ParentID: "main", Status: "succeeded", Report: &workerproto.OutputRef{RunID: "run", Turn: 3, Bytes: int64(len(body))}}}
	if _, err := st.AppendRunEvent(ctx, "session", "run", "agent_task", progress); err != nil {
		t.Fatal(err)
	}
	page, err = st.ReadModelOutput(ctx, "session", "run", 3, 0, 4096)
	if err != nil || !page.Complete || page.Content != body {
		t.Fatalf("completed report: %+v %v", page, err)
	}
}

func TestOutputTaskLinksSurviveListsCompletionAndInterruption(t *testing.T) {
	st, ctx := executionStore(t)
	artifact := Artifact{ID: "artifact", SessionID: "session", RunID: "run", OutputPath: filepath.Join(t.TempDir(), "output"), Status: "started", CallID: "call", TaskID: "child", ParentTaskID: "main"}
	if _, err := st.RecordArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job", SessionID: "session", RunID: "run", OutputPath: filepath.Join(t.TempDir(), "job-output"), Status: "starting", CallID: "job-call", TaskID: "child", ParentTaskID: "main"}
	if _, err := st.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	// Missing associations on a subsequent progress record retain their identity.
	artifact.Status, artifact.CallID, artifact.TaskID, artifact.ParentTaskID = "succeeded", "", "", ""
	if _, err := st.RecordArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InterruptJobs(ctx, "session", "worker stopped"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := st.Artifacts(ctx, "session")
	if err != nil || len(artifacts) != 1 || artifacts[0].CallID != "call" || artifacts[0].TaskID != "child" || artifacts[0].ParentTaskID != "main" {
		t.Fatalf("artifact list links: %+v %v", artifacts, err)
	}
	jobs, err := st.Jobs(ctx, "session")
	if err != nil || len(jobs) != 1 || jobs[0].Status != "interrupted" || jobs[0].CallID != "job-call" || jobs[0].TaskID != "child" {
		t.Fatalf("job list links: %+v %v", jobs, err)
	}
	got, err := st.GetJob(ctx, "session", "job")
	if err != nil || got.CallID != "job-call" || got.ParentTaskID != "main" {
		t.Fatalf("job links: %+v %v", got, err)
	}
}

func TestRunOverviewKeepsFirstReasoningPerTaskAndTurn(t *testing.T) {
	st, ctx := executionStore(t)
	appendReasoning := func(result workerproto.Result) Event {
		t.Helper()
		event, err := st.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", result)
		if err != nil {
			t.Fatal(err)
		}
		return *event
	}
	appendReasoning(workerproto.Result{RunID: "run", Turn: 1})
	first := appendReasoning(workerproto.Result{RunID: "run", Turn: 1, Content: "First reasoning fragment."})
	long := strings.Repeat("Full reasoning 思考🙂. ", 1000)
	for range 105 {
		appendReasoning(workerproto.Result{RunID: "run", Turn: 1, Content: long})
	}
	child := appendReasoning(workerproto.Result{RunID: "run", TaskID: "child", Turn: 1, Content: "Child reasoning."})
	next := appendReasoning(workerproto.Result{RunID: "run", TaskID: "main", Turn: 2, Content: "Next reasoning."})
	appendReasoning(workerproto.Result{RunID: "run", TaskID: "main", Turn: 2, Content: "Tail."})
	appendReasoning(workerproto.Result{RunID: "run", Turn: 0, Content: "Invalid turn."})
	if _, err := st.AppendRunEvent(ctx, "session", "run", "model_reasoning_delta", map[string]any{"runId": "run", "turn": 3, "purpose": "summary", "content": "Auxiliary summary."}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "failed"} {
		if status == "failed" {
			if _, err := st.FinishRun(ctx, "session", "run", "reply", status, ""); err != nil {
				t.Fatal(err)
			}
		}
		overview, err := st.RunOverview(ctx, "session", "run")
		if err != nil {
			t.Fatal(err)
		}
		var reasoning []Event
		for _, event := range overview.Events {
			if event.Kind == "model_reasoning_delta" {
				reasoning = append(reasoning, event)
			}
		}
		want := []Event{first, child, next}
		if len(reasoning) != len(want) {
			t.Fatalf("reasoning-only runs must remain discoverable with one fragment per task/turn: %+v", reasoning)
		}
		for i, event := range reasoning {
			if event.Sequence != want[i].Sequence || string(event.Data) != string(want[i].Data) || event.CreatedAt != want[i].CreatedAt {
				t.Fatalf("reasoning fragment lost its identity or original order: %+v", event)
			}
		}
	}
	scoped, err := st.RunOverviewRange(ctx, "session", "run", child.Sequence, next.Sequence)
	if err != nil || len(scoped.Events) != 1 || scoped.Events[0].Sequence != next.Sequence {
		t.Fatalf("reasoning crossed reply range: %+v %v", scoped, err)
	}
	page, err := st.RunTurnEvents(ctx, "session", "run", 1, child.Sequence)
	if err != nil || len(page) != 100 {
		t.Fatalf("full history: %d %v", len(page), err)
	}
	var full workerproto.Result
	if err := json.Unmarshal(page[0].Data, &full); err != nil || full.Content != long {
		t.Fatalf("overview shortened stored reasoning: %d bytes, %v", len(full.Content), err)
	}
}
