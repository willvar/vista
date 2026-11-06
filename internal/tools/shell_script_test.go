package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"vista/internal/platform"
	"vista/internal/toolinput"
	"vista/internal/workerproto"
)

func longTestScript() string {
	comments := strings.Repeat("# 中文🙂 multiline source\n", 5500)
	if runtime.GOOS == "windows" {
		return comments + "Write-Output (Get-Location).Path; Write-Output 'complete 中文🙂'"
	}
	return comments + "pwd\nprintf 'complete 中文🙂'"
}

func assertScriptsRemoved(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(platform.ShellScriptDirectory(stateDir, os.Getpid()))
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		t.Fatalf("temporary command source remains: %d files, %v", len(entries), err)
	}
}

func TestLongShellScriptExecutesWithOriginalWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	result, err := RunShell(context.Background(), state, dir, longTestScript(), 10*time.Second)
	if err != nil || !result.Complete || result.ExitCode != 0 || !strings.Contains(result.Preview, "complete 中文🙂") || !strings.Contains(result.Preview, dir) {
		t.Fatalf("long source was changed or truncated: %+v, %v", result, err)
	}
	assertScriptsRemoved(t, state)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunShell(ctx, state, dir, longTestScript(), time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command was run: %v", err)
	}
	assertScriptsRemoved(t, state)
}

func TestShellOverBudgetHasNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "not-created")
	_, err := RunShell(context.Background(), state, dir, strings.Repeat("x", toolinput.MaxArgumentsBytes+1), time.Second)
	var inputErr *toolinput.Error
	if !errors.As(err, &inputErr) || inputErr.ActualBytes != toolinput.MaxArgumentsBytes+1 || inputErr.MaxBytes != toolinput.MaxArgumentsBytes {
		t.Fatalf("missing recoverable input diagnostics: %v", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("over-budget input created artifacts: %v", err)
	}
}

func TestLongBackgroundScriptKeepsBoundedMetadataAndCleansOnClose(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	manager := NewBackgroundManager(func(_ context.Context, job BackgroundJob) error {
		if len(job.Command) > workerproto.MaxCommandPreviewBytes || !utf8.ValidString(job.Command) {
			t.Errorf("command preview violates the Worker contract: %d bytes", len(job.Command))
		}
		encoded, _ := json.Marshal(job)
		if len(encoded) >= 16<<10 {
			t.Errorf("long source leaked into background metadata: %d bytes", len(encoded))
		}
		return nil
	})
	defer manager.Close()
	script := longTestScript()
	if runtime.GOOS == "windows" {
		script += "\nStart-Sleep -Seconds 30"
	} else {
		script += "\nsleep 30"
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	job, err := manager.Start(runCtx, state, "run", dir, script, 0, nil)
	if err != nil || job.Status != "running" || len(job.Command) >= len(script) {
		t.Fatalf("long background start: %+v, %v", job, err)
	}
	cancelRun() // A completed/canceled reply does not own the background job.
	deadline := time.Now().Add(10 * time.Second)
	for {
		output, _ := os.ReadFile(job.OutputPath)
		if strings.Contains(string(output), "complete 中文🙂") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("long background script did not execute completely: %q", output)
		}
		time.Sleep(20 * time.Millisecond)
	}
	entries, err := os.ReadDir(platform.ShellScriptDirectory(state, os.Getpid()))
	if err != nil || len(entries) != 1 {
		t.Fatalf("active script source removed prematurely: %v, %v", entries, err)
	}
	manager.Close()
	assertScriptsRemoved(t, state)
}

func TestShellCommandPreviewFitsWorkerBudget(t *testing.T) {
	for _, script := range []string{
		strings.Repeat("x", workerproto.MaxCommandPreviewBytes),
		strings.Repeat("x", workerproto.MaxCommandPreviewBytes+1),
		strings.Repeat("界🙂\x00", 1000),
	} {
		preview := ShellCommandPreview(script, nil)
		if len(preview) > workerproto.MaxCommandPreviewBytes || !utf8.ValidString(preview) {
			t.Fatalf("preview exceeds byte or UTF-8 bounds: %d bytes", len(preview))
		}
		if len(script) <= workerproto.MaxCommandPreviewBytes && preview != script {
			t.Fatal("an in-budget command was altered")
		}
		if len(script) > workerproto.MaxCommandPreviewBytes && !strings.HasSuffix(preview, "\n[Command preview truncated.]") {
			t.Fatal("truncation was not marked")
		}
	}
}
