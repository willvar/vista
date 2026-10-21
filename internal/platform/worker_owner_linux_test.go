//go:build linux

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRecordedWorkerSessionCleansOrphanAfterLeaderDeath(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & echo $! > '"+pidFile+"'; wait")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	start, err := WorkerSessionStart(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	for i := 0; i < 100; i++ {
		data, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if child > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("shell child did not start")
	}
	if err := KillOwnedSession(cmd.Process.Pid, start+1); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong Worker identity was accepted: %v", err)
	}
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("identity mismatch killed child: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := KillOwnedSession(cmd.Process.Pid, start); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		status, readErr := os.ReadFile("/proc/" + strconv.Itoa(child) + "/status")
		if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(status), "State:\tZ")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("orphan child %d survived startup cleanup", child)
}
