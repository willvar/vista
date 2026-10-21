package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestWorkerTaskInstructionsSurviveOverview(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(ctx, "session", "task instructions"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "session", "run", "user", "work"); err != nil {
		t.Fatal(err)
	}
	instructions := "检查任务说明保留。\n" + strings.Repeat("逐项验证开始、完成、读取时的多行中文说明。\n", 20)
	var frames bytes.Buffer
	for _, status := range []string{"started", "succeeded"} {
		record := workerproto.Result{RunID: "run", Kind: "agent_task", TaskID: "main/child", ParentTaskID: "main", Depth: 1, Task: &workerproto.AgentTask{ID: "main/child", ParentID: "main", Title: instructions, Status: status}}
		if err := json.NewEncoder(&frames).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	w := &worker{cmd: &exec.Cmd{}, stopping: true, done: make(chan struct{})}
	app := &App{ctx: ctx, store: db, dir: t.TempDir(), workers: map[string]*worker{"session": w}}
	app.readWorkerOutput("session", w, bufio.NewScanner(&frames))
	overview, err := db.RunOverview(ctx, "session", "run")
	if err != nil || len(overview.Events) != 2 {
		t.Fatalf("task overview: events=%d error=%v", len(overview.Events), err)
	}
	for index, event := range overview.Events {
		var record workerproto.Result
		if err := json.Unmarshal(event.Data, &record); err != nil || record.Task == nil || record.Task.Title != instructions || record.Task.Status != []string{"started", "succeeded"}[index] {
			t.Fatalf("task instructions lost in overview: %+v error=%v", record, err)
		}
	}
}

func TestTaskInstructionBoundsAndEncoding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		depth int
		id    string
		title string
		valid bool
	}{
		{"child maximum", 1, "main/child", strings.Repeat("x", 2048), true},
		{"child too long", 1, "main/child", strings.Repeat("x", 2049), false},
		{"child invalid UTF8", 1, "main/child", string([]byte{0xff}), false},
		{"root maximum", 0, "main", strings.Repeat("x", 300), true},
		{"root too long", 0, "main", strings.Repeat("x", 301), false},
		{"root cannot claim child allowance", 1, "main", strings.Repeat("x", 301), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := workerproto.Result{RunID: "run", Kind: "agent_task", Depth: tc.depth, TaskID: tc.id, Task: &workerproto.AgentTask{ID: tc.id, Title: tc.title, Status: "started"}}
			if tc.id != "main" {
				result.ParentTaskID, result.Task.ParentID = "main", "main"
			}
			if valid := validAgentTaskProgress(t.TempDir(), result); valid != tc.valid {
				t.Fatalf("task validation: got=%v want=%v", valid, tc.valid)
			}
		})
	}
}
