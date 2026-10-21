//go:build linux

package platform

import (
	"bufio"
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

func TestKillSessionDescendantsKeepsWorkerSessionScoped(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _, err := procIdentity(child)
	if err != nil || sessionID != cmd.Process.Pid {
		t.Fatalf("child session: %d, want %d, error %v", sessionID, cmd.Process.Pid, err)
	}
	if err := KillSessionDescendants(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := os.ReadFile("/proc/" + strconv.Itoa(child) + "/status")
		if errors.Is(err, os.ErrNotExist) || (err == nil && strings.Contains(string(status), "State:\tZ")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session descendant %d survived cleanup: %v", child, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCgroupAvailableUsesTightestAncestor(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "user.slice")
	child := filepath.Join(parent, "session.scope")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(parent, "memory.max"):     "8589934592",
		filepath.Join(parent, "memory.current"): "4294967296",
		filepath.Join(child, "memory.max"):      "6442450944",
		filepath.Join(child, "memory.current"):  "1073741824",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	available, limited, err := cgroupAvailable(root, "/user.slice/session.scope")
	if err != nil || !limited || available != 4<<30 {
		t.Fatalf("ancestor limit: available=%d limited=%v error=%v", available, limited, err)
	}
	if err := os.WriteFile(filepath.Join(parent, "memory.max"), []byte("max"), 0600); err != nil {
		t.Fatal(err)
	}
	available, limited, err = cgroupAvailable(root, "/user.slice/session.scope")
	if err != nil || !limited || available != 5<<30 {
		t.Fatalf("scope limit: available=%d limited=%v error=%v", available, limited, err)
	}
}

func TestPeakRSSCoversCurrentAndNeverFalls(t *testing.T) {
	current := RSS()
	first := PeakRSS()
	if first < current {
		t.Fatalf("peak RSS %d is below current RSS %d", first, current)
	}
	for range 10 {
		current = RSS()
		next := PeakRSS()
		if next < current || next < first {
			t.Fatalf("peak RSS fell or missed current: previous=%d current=%d next=%d", first, current, next)
		}
		first = next
	}
}
