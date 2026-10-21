package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/agentconfig"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func nestedWorkerRecords(depth int) []workerproto.Result {
	parentID := "main" + strings.Repeat("/child", depth-1)
	taskID := parentID + "/child"
	base := workerproto.Result{RunID: "run", Turn: int32(depth), Depth: depth, TaskID: taskID, ParentTaskID: parentID}
	request := base
	request.Kind, request.ModelUse = "model_request", &workerproto.ModelUse{Provider: "openai-chat", ID: "nested-model", ReasoningEffort: "high"}
	reasoning := base
	reasoning.Kind, reasoning.Content = "model_reasoning_delta", "Consider the assigned work."
	delta := base
	delta.Kind, delta.Content = "model_delta", "Nested Agent report."
	result := base
	result.Kind, result.ModelResult = "model_result", &workerproto.ModelResult{Provider: "openai-chat", ID: "nested-model", FinishReason: "stop", ReasoningEffort: "high", Usage: &workerproto.ModelUsage{InputTokens: 10, OutputTokens: 5}}
	source := base
	source.Kind, source.Content = "source", "https://example.com/reference"
	task := base
	task.Kind, task.Task = "agent_task", &workerproto.AgentTask{ID: taskID, ParentID: parentID, Status: "succeeded", Report: &workerproto.OutputRef{RunID: "run", Turn: int32(depth), Bytes: int64(len(delta.Content))}}
	started := base
	started.Kind, started.Task = "agent_task", &workerproto.AgentTask{ID: taskID, ParentID: parentID, Status: "started"}
	return []workerproto.Result{started, request, reasoning, delta, result, source, task}
}

// Exercise the real Server JSON transport reader and Store, without starting
// a worker process, HTTP server, or model request.
func TestWorkerNestedDepthRecordsPersistAndRemainReadable(t *testing.T) {
	for _, depth := range []int{3, agentconfig.MaxDelegationDepth} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Create(ctx, "session", "nested execution"); err != nil {
				t.Fatal(err)
			}
			_, initial, err := db.BeginRun(ctx, "session", "run", "user", "work")
			if err != nil {
				t.Fatal(err)
			}
			records := nestedWorkerRecords(depth)
			var frames bytes.Buffer
			for _, record := range records {
				if err := json.NewEncoder(&frames).Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			w := &worker{cmd: &exec.Cmd{}, stopping: true, done: make(chan struct{})}
			app := &App{ctx: ctx, store: db, dir: t.TempDir(), workers: map[string]*worker{"session": w}}
			app.readWorkerOutput("session", w, bufio.NewScanner(&frames))
			events, err := db.EventsAfter(ctx, "session", initial.Sequence, 100)
			if err != nil || len(events) != len(records) {
				t.Fatalf("persisted records at depth %d: count=%d error=%v", depth, len(events), err)
			}
			for index, event := range events {
				var record workerproto.Result
				if err := json.Unmarshal(event.Data, &record); err != nil || event.Kind != records[index].Kind || record.Depth != depth || record.TaskID != records[index].TaskID || record.ParentTaskID != records[index].ParentTaskID {
					t.Fatalf("record identity at depth %d: kind=%s data=%+v error=%v", depth, event.Kind, record, err)
				}
				if event.Kind == "model_result" && (record.ModelResult == nil || record.ModelResult.Usage == nil || record.ModelResult.Usage.InputTokens != 10) {
					t.Fatalf("model result lost usage at depth %d: %+v", depth, record.ModelResult)
				}
			}
			page, err := db.ReadFullModelOutput(ctx, "session", "run", int32(depth))
			if err != nil || page.Content != records[3].Content || !page.Complete {
				t.Fatalf("nested report at depth %d: %+v error=%v", depth, page, err)
			}
		})
	}
}

func TestWorkerRejectsDepthBeyondMaximumBeforePersisting(t *testing.T) {
	for _, record := range nestedWorkerRecords(agentconfig.MaxDelegationDepth + 1) {
		t.Run(record.Kind, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Create(ctx, "session", "invalid descendant"); err != nil {
				t.Fatal(err)
			}
			_, initial, err := db.BeginRun(ctx, "session", "run", "user", "work")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			w := &worker{cmd: &exec.Cmd{}, stopping: true, done: make(chan struct{})}
			app := &App{ctx: ctx, store: db, dir: t.TempDir(), workers: map[string]*worker{"session": w}}
			app.readWorkerOutput("session", w, bufio.NewScanner(bytes.NewReader(append(encoded, '\n'))))
			events, err := db.EventsAfter(ctx, "session", initial.Sequence, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("depth beyond maximum persisted: count=%d error=%v", len(events), err)
			}
			if _, err := db.ReadFullModelOutput(ctx, "session", "run", record.Turn); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("invalid descendant has readable output: %v", err)
			}
		})
	}
}
