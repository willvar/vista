//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestCommandsRunInDirectoriesContainingSpecialFiles(t *testing.T) {
	for _, name := range []string{"shell", "start_background"} {
		t.Run(name, func(t *testing.T) {
			db, emit := outputToolStore(t)
			ctx := context.Background()
			message, err := db.RevertTarget(ctx, "session", "user")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			if err := syscall.Mkfifo(filepath.Join(directory, "pipe"), 0600); err != nil {
				t.Fatal(err)
			}
			events := make(chan tools.BackgroundJob, 3)
			jobs := tools.NewBackgroundManager(func(_ context.Context, job tools.BackgroundJob) error {
				events <- job
				return nil
			})
			defer jobs.Close()
			runtime := &toolRuntime{parent: workerproto.Command{SessionID: "session", RunID: "run", TaskID: "main", MessageSequence: message.Sequence}, emit: emit, jobs: jobs}
			var call provider.ToolCall
			call.ID, call.Function.Name = "command", name
			arguments, _ := json.Marshal(map[string]any{"directory": directory, "command": "printf ready", "timeoutSeconds": 5})
			call.Function.Arguments = string(arguments)
			output, err := runtime.execute(ctx, call)
			if err != nil {
				t.Fatal("unrelated special file blocked command execution", output, err)
			}
			if name == "shell" {
				var result tools.ShellResult
				if err := json.Unmarshal([]byte(output), &result); err != nil || result.Preview != "ready" || result.ExitCode != 0 {
					t.Fatal("shell did not run", output, err)
				}
				return
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case job := <-events:
					if job.Status == "starting" || job.Status == "running" {
						continue
					}
					if job.Status != "succeeded" || !job.Complete {
						t.Fatal("background command did not succeed", job)
					}
					data, err := os.ReadFile(job.OutputPath)
					if err != nil || string(data) != "ready" {
						t.Fatal("background output lost", string(data), err)
					}
					return
				case <-timer.C:
					t.Fatal("background command did not finish")
				}
			}
		})
	}
}

func TestShellCommandFailurePreservesOutputAndFailedArtifact(t *testing.T) {
	for _, tc := range []struct {
		script, want, signal string
		exit                 int
	}{
		{"printf evidence; exit 7", "exit code 7", "", 7},
		{"printf evidence; kill -TERM $$", "SIGTERM", "SIGTERM", -1},
	} {
		t.Run(tc.want, func(t *testing.T) {
			db, emit := outputToolStore(t)
			message, err := db.RevertTarget(context.Background(), "session", "user")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			path := filepath.Join(directory, "source.txt")
			if err := os.WriteFile(path, []byte("original"), 0640); err != nil {
				t.Fatal(err)
			}
			var call provider.ToolCall
			call.ID, call.Function.Name = "command", "shell"
			arguments, _ := json.Marshal(map[string]any{"directory": directory, "command": "printf changed > source.txt; " + tc.script})
			call.Function.Arguments = string(arguments)
			runtime := &toolRuntime{parent: workerproto.Command{SessionID: "session", RunID: "run", TaskID: "main", MessageSequence: message.Sequence}, emit: emit}
			output, err := runtime.execute(context.Background(), call)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("command reported success: %s %v", output, err)
			}
			var result tools.ShellResult
			if json.Unmarshal([]byte(output), &result) != nil || result.Preview != "evidence" || result.ExitCode != tc.exit || result.Signal != tc.signal {
				t.Fatalf("outcome lost: %s", output)
			}
			artifact, err := db.GetArtifact(context.Background(), "session", result.ArtifactID)
			if err != nil || artifact.Status != "failed" || !strings.Contains(artifact.Error, tc.want) {
				t.Fatalf("artifact falsely succeeded: %+v %v", artifact, err)
			}
			if model := mergeToolError(output, result.ExitError()); !strings.Contains(model, "evidence") || !strings.Contains(model, tc.want) {
				t.Fatalf("model cannot handle command failure: %s", model)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "changed" {
				t.Fatal("failed command changes were not retained", string(data), err)
			}
		})
	}
}
