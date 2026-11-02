//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestBackgroundTimeoutIsOptionalAndHasNoDailyLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout *int64
		invalid bool
	}{
		{name: "omitted"},
		{name: "two days", timeout: new(int64(172800))},
		{name: "zero", timeout: new(int64(0)), invalid: true},
		{name: "negative", timeout: new(int64(-1)), invalid: true},
		{name: "duration overflow", timeout: new(int64(1<<63 - 1)), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := continuationFixture(t)
			events := make(chan tools.BackgroundJob, 8)
			manager := tools.NewBackgroundManager(func(_ context.Context, job tools.BackgroundJob) error {
				events <- job
				return nil
			})
			defer manager.Close()
			arguments := map[string]any{"directory": root, "command": "printf ready"}
			if tc.timeout != nil {
				arguments["timeoutSeconds"] = *tc.timeout
			}
			encoded, _ := json.Marshal(arguments)
			var call provider.ToolCall
			call.ID, call.Function.Name, call.Function.Arguments = "background", "start_background", string(encoded)
			runtime := toolRuntime{jobs: manager, parent: workerproto.Command{RunID: "run"}}
			output, err := runtime.startBackground(context.Background(), call)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "timeoutSeconds") || len(events) != 0 {
					t.Fatalf("invalid timeout started a job: output=%s err=%v events=%d", output, err, len(events))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if json.Unmarshal([]byte(output), &result) != nil || result["jobId"] == "" || result["status"] != "running" {
				t.Fatalf("invalid start result: %s", output)
			}
			started := <-events
			if tc.timeout == nil {
				if _, exists := result["expiresAt"]; exists || started.ExpiresAt != "" {
					t.Fatalf("omitted timeout acquired an expiry: output=%s job=%+v", output, started)
				}
			} else {
				expires, err := time.Parse(time.RFC3339Nano, started.ExpiresAt)
				remaining := time.Until(expires)
				if err != nil || result["expiresAt"] != started.ExpiresAt || remaining < 47*time.Hour || remaining > 48*time.Hour {
					t.Fatalf("explicit timeout was lost or capped: output=%s job=%+v err=%v", output, started, err)
				}
			}
			if info, err := os.Stat(fmt.Sprint(result["outputPath"])); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("start result lost output file: %s %v", output, err)
			}
		})
	}
}

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
