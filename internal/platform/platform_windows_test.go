package platform

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsParentDeathClosesJobs(t *testing.T) {
	root := t.TempDir()
	parent := nativeHelper("parent", root)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	worker := waitNativePID(t, filepath.Join(root, "worker.pid"))
	leaf := waitNativePID(t, filepath.Join(root, "leaf.pid"))
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitNativeExit(t, worker)
	waitNativeExit(t, leaf)
}

func TestWindowsDetachedProcessSurvivesLauncher(t *testing.T) {
	root := t.TempDir()
	launcher := nativeHelper("idle", filepath.Join(root, "unused.pid"))
	launcher.Args = []string{launcher.Path, "-test.run=^TestWindowsDetachHelper$", "--", root}
	if err := launcher.Run(); err != nil {
		t.Fatal(err)
	}
	pid := waitNativePID(t, filepath.Join(root, "detached.pid"))
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill(); _ = process.Release() }()
	if !IsAlive(pid) {
		t.Fatal("detached process died with its launcher")
	}
}

func TestWindowsDetachHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	root := os.Args[len(os.Args)-1]
	job, err := newProcessJob()
	if err != nil {
		os.Exit(2)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		os.Exit(2)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		os.Exit(2)
	}
	cmd := nativeHelper("idle", filepath.Join(root, "detached.pid"))
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWindowsParentDeathDuringLaunch(t *testing.T) {
	root := t.TempDir()
	parent := nativeHelper("idle", filepath.Join(root, "unused.pid"))
	parent.Args = []string{parent.Path, "-test.run=^TestWindowsLaunchHelper$", "--", root}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	child := waitNativePID(t, filepath.Join(root, "suspended.pid"))
	_ = parent.Process.Kill()
	waitNativeExit(t, child)
}

func TestWindowsLaunchHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	root := os.Args[len(os.Args)-1]
	if err := ownProcessTree(); err != nil {
		os.Exit(2)
	}
	cmd := nativeHelper("idle", filepath.Join(root, "unused.pid"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(root, "suspended.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestWindowsNativeCommandExitCode(t *testing.T) {
	for _, tc := range []struct {
		script string
		code   int
	}{
		{"cmd.exe /d /c 'echo Vista diagnostic 1>&2 & exit 0'", 0},
		{"cmd.exe /d /c 'exit 9'", 9},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := ShellCommand(ctx, tc.script)
		var output strings.Builder
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := Start(cmd); err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = cmd.Wait()
		_ = KillProcessGroup(cmd.Process)
		cancel()
		if cmd.ProcessState.ExitCode() != tc.code || strings.Contains(output.String(), "CLIXML") {
			t.Fatalf("%s: exit=%d, want %d, output=%q", tc.script, cmd.ProcessState.ExitCode(), tc.code, output.String())
		}
	}
}
