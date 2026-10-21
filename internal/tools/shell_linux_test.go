//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunShellCapturesOutputAndExit(t *testing.T) {
	root := t.TempDir()
	result, err := RunShell(context.Background(), filepath.Join(root, "state"), root, "printf 'hello'; exit 7", time.Second)
	if err != nil || !result.Complete || result.ExitCode != 7 || result.Preview != "hello" {
		t.Fatalf("unexpected shell result: %+v %v", result, err)
	}
	if result.Truncated || result.PreviewBytes != 5 || result.NextOffset != 5 {
		t.Fatalf("short output pagination: %+v", result)
	}
	data, err := os.ReadFile(result.OutputPath)
	if err != nil || string(data) != "hello" {
		t.Fatalf("saved output: %q %v", data, err)
	}
	if _, err := RunShell(context.Background(), root, ".", "pwd", time.Second); err == nil {
		t.Fatal("relative shell directory was accepted")
	}
}

func TestShellPreviewContinuesAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	content := strings.Repeat("a", maxShellPreview-1) + "你好" + strings.Repeat("b", 9000)
	path := filepath.Join(root, "input.txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := RunShell(context.Background(), stateDir, root, "cat input.txt", time.Second)
	if err != nil || !result.Truncated || result.PreviewBytes != maxShellPreview-1 || result.NextOffset != int64(result.PreviewBytes) || result.Bytes != int64(len(content)) {
		t.Fatalf("long shell output: %+v, %v", result, err)
	}
	recovered := result.Preview
	for offset := result.NextOffset; offset < result.Bytes; {
		page, err := ReadOutputPage(stateDir, result.OutputPath, offset, 0)
		if err != nil || page.NextOffset <= offset {
			t.Fatalf("continuation at %d: %+v, %v", offset, page, err)
		}
		recovered += page.Content
		offset = page.NextOffset
	}
	if recovered != content {
		t.Fatal("shell preview and continuation lost or duplicated UTF-8 output")
	}
}

func TestRunShellTimeoutKillsDescendantsAndLimitsOutput(t *testing.T) {
	root := t.TempDir()
	result, err := RunShell(context.Background(), filepath.Join(root, "state"), root, "sleep 30 & echo $!; wait", 500*time.Millisecond)
	if err == nil || result.Complete {
		t.Fatalf("timed-out command reported complete: %+v %v", result, err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(result.Preview))
	if parseErr != nil {
		t.Fatalf("child PID missing from preview %q: %v", result.Preview, parseErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
		if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(status), "State:\tZ")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived command timeout: %v", pid, syscall.Kill(pid, 0))
		}
		time.Sleep(20 * time.Millisecond)
	}
	result, err = RunShell(context.Background(), filepath.Join(root, "state"), root, "head -c 17000000 /dev/zero", 5*time.Second)
	if !errors.Is(err, ErrOutputLimit) || result.Complete || result.Bytes != maxShellOutput {
		t.Fatalf("output limit failed: %+v %v", result, err)
	}
	info, err := os.Stat(result.OutputPath)
	if err != nil || info.Size() != maxShellOutput {
		t.Fatalf("partial output not saved: %+v %v", info, err)
	}
}

func TestShellDoesNotRunBeforeArtifactStartAcknowledgment(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "should-not-exist")
	blocked := errors.New("artifact start was not acknowledged")
	var finished ShellResult
	result, err := RunShellRecorded(context.Background(), filepath.Join(root, "state"), root, "touch '"+marker+"'", time.Second, func(start ShellResult) error {
		if start.ArtifactID == "" || start.OutputPath == "" {
			t.Fatalf("missing artifact identity: %+v", start)
		}
		return blocked
	}, func(out ShellResult, cause error) error {
		finished = out
		if !errors.Is(cause, blocked) {
			t.Fatalf("unexpected completion cause: %v", cause)
		}
		return nil
	})
	if !errors.Is(err, blocked) || result.ArtifactID == "" || finished.ArtifactID != result.ArtifactID {
		t.Fatalf("unacknowledged artifact result: %+v %+v %v", result, finished, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell ran before artifact acknowledgment: %v", err)
	}
}
