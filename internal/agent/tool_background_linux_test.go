//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestBackgroundCommandRecordsUseDisplayProjection(t *testing.T) {
	root, _ := continuationFixture(t)
	ctx := context.Background()
	db, err := store.Open(filepath.Join(root, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(ctx, "session", "background display"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "session", "run", "user", "start job"); err != nil {
		t.Fatal(err)
	}
	parent := workerproto.Command{
		RunID: "run", TaskID: "main/child", ParentTaskID: "main",
		Model: modelcfg.Model{
			APIKey: "model-credential", AWSAccessID: "aws-access", AWSSecret: "aws-secret", AWSSession: "aws-session",
			OpenRouter: &modelcfg.OpenRouterOptions{APIKeys: map[string]string{"vendor": "routing-credential"}},
		},
		MCPServers:      []workerproto.MCPServer{{Token: "mcp-token", ClientSecret: "mcp-secret"}},
		SearchProviders: []workerproto.SearchProvider{{APIKey: "search-credential"}},
	}
	directory := filepath.Join(root, "mcp-secret")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// The output intentionally contains credentials: command metadata is redacted,
	// but the user's actual program output must be preserved byte for byte.
	wantOutput := strings.Join(commandSecrets(parent), " ")
	script := `API_TOKEN=unconfigured-secret; test "$API_TOKEN" = unconfigured-secret && printf '%s' '` + wantOutput + `'`
	args, _ := json.Marshal(map[string]any{"directory": directory, "command": script, "timeoutSeconds": 5})
	var call provider.ToolCall
	call.ID, call.Function.Name, call.Function.Arguments = "background", "start_background", string(args)
	display := toolInput(call, parent)
	events := make(chan store.Event, 8)
	manager := tools.NewBackgroundManager(func(ctx context.Context, job tools.BackgroundJob) error {
		event, err := db.RecordJob(ctx, store.Job{
			ID: job.ID, SessionID: "session", RunID: job.RunID, CallID: job.CallID, TaskID: job.TaskID, ParentTaskID: job.ParentTaskID,
			Directory: job.Directory, Command: job.Command, OutputPath: job.OutputPath, Status: job.Status,
			Bytes: job.Bytes, ExitCode: job.ExitCode, Complete: job.Complete, Error: job.Error,
		})
		if err == nil && event != nil {
			events <- *event
		}
		return err
	})
	defer manager.Close()
	runtime := toolRuntime{jobs: manager, parent: parent, depth: 1}
	output, err := runtime.startBackground(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		JobID      string `json:"jobId"`
		OutputPath string `json:"outputPath"`
	}
	if err := json.Unmarshal([]byte(output), &started); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		var event store.Event
		select {
		case event = <-events:
		case <-time.After(5 * time.Second):
			t.Fatal("background command did not finish")
		}
		var job store.Job
		if err := json.Unmarshal(event.Data, &job); err != nil {
			t.Fatal(err)
		}
		if job.Command != display.Command || job.Directory != display.Directory {
			t.Fatalf("job metadata bypassed tool display projection: %+v", job)
		}
		if job.CallID != "main/child/background" || job.TaskID != parent.TaskID || job.ParentTaskID != parent.ParentTaskID {
			t.Fatalf("background identity lost: %+v", job)
		}
		for _, secret := range append(commandSecrets(parent), "unconfigured-secret") {
			if strings.Contains(job.Command, secret) || strings.Contains(job.Directory, secret) {
				t.Fatalf("%s event exposed credential %q", job.Status, secret)
			}
		}
		if i == 2 && (job.Status != "succeeded" || !job.Complete || job.ExitCode != 0) {
			t.Fatalf("redaction changed execution: %+v", job)
		}
	}
	stored, err := db.GetJob(ctx, "session", started.JobID)
	if err != nil || stored.Command != display.Command || stored.Directory != display.Directory {
		t.Fatalf("stored metadata: %+v, %v", stored, err)
	}
	data, err := os.ReadFile(started.OutputPath)
	if err != nil || string(data) != wantOutput {
		t.Fatalf("output modified: %q, %v", data, err)
	}
}
