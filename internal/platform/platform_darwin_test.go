package platform

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeReapedOwnedSession(t *testing.T) {
	root := t.TempDir()
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
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // Reap the leader before recovering its surviving children.
	if _, _, err := procIdentity(cmd.Process.Pid); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("reaped Worker identity: %v", err)
	}
	if !nativeRunning(leaf) {
		t.Fatal("child exited before recovery was exercised")
	}
	if err := KillOwnedSession(cmd.Process.Pid, start); err != nil {
		t.Fatal(err)
	}
	waitNativeExit(t, leaf)
}

func TestVMStatAccountsForPageSizeAndReclaimableMemory(t *testing.T) {
	for _, pageSize := range []string{"4096", "16384"} {
		input := "Mach Virtual Memory Statistics: (page size of " + pageSize + " bytes)\nPages free: 100.\nPages active: 200.\nPages inactive: 50.\nPages speculative: 10.\nPages purgeable: 20.\n"
		want := uint64(160 * 4096)
		if pageSize == "16384" {
			want = 160 * 16384
		}
		if got, err := parseVMStat(input); err != nil || got != want {
			t.Fatalf("page size %s: got %d, want %d, error %v", pageSize, got, want, err)
		}
	}
	for _, input := range []string{"", "no page size\nPages free: 100.", "page size of 4096 bytes\nPages active: 1.", "page size of 4096 bytes\nPages free: invalid."} {
		if _, err := parseVMStat(input); err == nil {
			t.Fatalf("invalid vm_stat accepted: %q", input)
		}
	}
}
