package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := TryLock(first); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(second); !IsLockBusy(err) {
		t.Fatalf("concurrent lock accepted or misclassified: %v", err)
	}
	first.Close()
	if err := TryLock(second); err != nil {
		t.Fatalf("close did not release lock: %v", err)
	}
}

func TestNativeMemory(t *testing.T) {
	if available, err := AvailableMemory(); err != nil || available == 0 {
		t.Fatalf("available memory: %d, %v", available, err)
	}
	current := RSS()
	if peak := PeakRSS(); current == 0 || peak < current {
		t.Fatalf("RSS=%d peak=%d", current, peak)
	}
	if !IsAlive(os.Getpid()) || IsAlive(-1) {
		t.Fatal("process liveness is incorrect")
	}
}

func TestNativeOwnedSession(t *testing.T) {
	root := t.TempDir()
	outside := nativeHelper("idle", filepath.Join(root, "outside.pid"))
	if err := outside.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outside.Process.Kill(); _ = outside.Wait() }()
	cmd := nativeHelper("tree", root)
	AttachToParent(cmd)
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = KillSessionDescendants(cmd.Process.Pid)
		_ = cmd.Wait()
	}()
	leaf := waitNativePID(t, filepath.Join(root, "leaf.pid"))
	start, err := WorkerSessionStart(cmd.Process.Pid)
	if err != nil || start == 0 {
		t.Fatalf("Worker identity: %d %v", start, err)
	}
	if err := KillOwnedSession(cmd.Process.Pid, start+1); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity mismatch was accepted: %v", err)
	}
	if !nativeRunning(leaf) {
		t.Fatal("identity mismatch killed the child")
	}
	// Recover an orphan, including when the leader dies before cleanup.
	_ = cmd.Process.Kill()
	if err := KillOwnedSession(cmd.Process.Pid, start); err != nil {
		t.Fatal(err)
	}
	waitNativeExit(t, leaf)
	if !IsAlive(outside.Process.Pid) {
		t.Fatal("cleanup escaped the owned process tree")
	}
}

func TestNativeShell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := "printf 'Vista 中文'; exit 7"
	if runtime.GOOS == "windows" {
		script = "Write-Output 'Vista 中文'; exit 7"
	}
	cmd := ShellCommand(ctx, script)
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	_ = KillProcessGroup(cmd.Process)
	if err == nil || cmd.ProcessState.ExitCode() != 7 || !strings.Contains(output.String(), "Vista 中文") {
		t.Fatalf("exit=%v, output=%q, error=%v", cmd.ProcessState, output.String(), err)
	}
}

func TestNativeShellErrorsRemainText(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := "printf 'Vista tool failure' >&2; exit 1"
	if runtime.GOOS == "windows" {
		script = "Write-Error 'Vista tool failure'"
	}
	cmd := ShellCommand(ctx, script)
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	_ = KillProcessGroup(cmd.Process)
	if err == nil || !strings.Contains(output.String(), "Vista tool failure") || strings.Contains(output.String(), "CLIXML") {
		t.Fatalf("shell errors were lost or encoded: %q, %v", output.String(), err)
	}
}

func TestNativeProcessHelper(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--vista-helper" {
		return
	}
	kind, path := args[len(args)-2], args[len(args)-1]
	switch kind {
	case "idle":
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
	case "tree":
		cmd := nativeHelper("idle", filepath.Join(path, "leaf.pid"))
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
	case "parent":
		cmd := nativeHelper("tree", path)
		AttachToParent(cmd)
		if err := Start(cmd); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(filepath.Join(path, "worker.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			os.Exit(2)
		}
	default:
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func nativeHelper(kind, path string) *exec.Cmd {
	return exec.Command(os.Args[0], "-test.run=^TestNativeProcessHelper$", "--", "--vista-helper", kind, path)
}

func waitNativePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process marker was not created: %s", path)
	return 0
}

func nativeRunning(pid int) bool {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil || strings.HasPrefix(string(data[strings.LastIndexByte(string(data), ')')+1:]), " Z") {
			return false
		}
	}
	return IsAlive(pid)
}

func waitNativeExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !nativeRunning(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived cleanup", pid)
}
