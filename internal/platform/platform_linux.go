//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func TryLock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func IsLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
func Detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func OpenBrowser(url string) error {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	// A desktop launcher can stay alive with its browser. Server readiness
	// must not wait for it; reap it if it exits while this CLI is still alive.
	go func() { _ = cmd.Wait() }()
	return nil
}
func AttachToParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGTERM}
}
func Start(cmd *exec.Cmd) error { return cmd.Start() }
func PrepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
func Terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
func ShellCommand(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	PrepareCommand(cmd)
	return cmd
}
func KillProcessGroup(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGKILL) }

func ProcessSignal(state *os.ProcessState) string {
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return unix.SignalName(status.Signal())
		}
	}
	return ""
}

func WorkerSessionStart(pid int) (uint64, error) {
	session, start, err := procIdentity(pid)
	if err != nil {
		return 0, err
	}
	if session != pid {
		return 0, errors.New("Worker is not its session leader")
	}
	return start, nil
}

// KillOwnedSession only acts when a live leader still has its recorded identity,
// or when the leader is gone and remaining members retain its Linux session ID.
func KillOwnedSession(pid int, start uint64) error {
	if pid <= 1 || start == 0 {
		return errors.New("invalid Worker ownership record")
	}
	session, currentStart, err := procIdentity(pid)
	if err == nil && (session != pid || currentStart != start) {
		return ErrIdentityMismatch
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := KillSessionDescendants(pid); err != nil {
		return err
	}
	if currentStart == start {
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		if session, now, err := procIdentity(pid); err == nil && session == pid && now == start {
			if err := unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
	}
	return nil
}

// KillSessionDescendants kills processes still in a Worker-owned Linux session.
// The Worker must remain unreaped while this runs, so its session ID cannot be
// mistaken for a reused PID. A process that calls setsid escapes this scope.
func KillSessionDescendants(sessionID int) error {
	if sessionID <= 1 {
		return errors.New("invalid Worker session ID")
	}
	var first error
	for pass := 0; pass < 3; pass++ {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		found := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == sessionID || pid <= 1 {
				continue
			}
			candidate, start, err := procIdentity(pid)
			if err != nil || candidate != sessionID {
				continue
			}
			found = true
			fd, err := unix.PidfdOpen(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				continue
			}
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			checkSession, checkStart, checkErr := procIdentity(pid)
			if checkErr == nil && checkSession == sessionID && checkStart == start {
				if err := unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) && first == nil {
					first = err
				}
			}
			_ = unix.Close(fd)
		}
		if !found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return first
}

func procIdentity(pid int) (sessionID int, startTime uint64, err error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return 0, 0, errors.New("short process stat")
	}
	sessionID, err = strconv.Atoi(fields[3])
	if err != nil {
		return 0, 0, err
	}
	startTime, err = strconv.ParseUint(fields[19], 10, 64)
	return sessionID, startTime, err
}

func RSS() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(fields[1], 10, 64)
	current := pages * uint64(os.Getpagesize())
	recordPeakRSS(current)
	return current
}
func PeakRSS() uint64 {
	RSS()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil && usage.Maxrss >= 0 {
		// Linux reports ru_maxrss in KiB; it can be below a statm RSS sample.
		recordPeakRSS(uint64(usage.Maxrss) * 1024)
	}
	return observedPeakRSS.Load()
}
func AvailableMemory() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	var available uint64
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, errors.New("invalid MemAvailable")
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			available = kb * 1024
			break
		}
	}
	if available == 0 {
		return 0, errors.New("MemAvailable is unavailable")
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err == nil {
		for _, line := range strings.Split(string(cgroup), "\n") {
			if !strings.HasPrefix(line, "0::") {
				continue
			}
			remaining, limited, err := cgroupAvailable("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
			if err != nil {
				return 0, err
			}
			if limited && remaining < available {
				available = remaining
			}
			break
		}
	}
	return available, nil
}

func cgroupAvailable(root, current string) (uint64, bool, error) {
	path := filepath.Join(root, strings.TrimPrefix(filepath.Clean("/"+current), "/"))
	var available uint64
	limited := false
	for {
		maxBytes, err := os.ReadFile(filepath.Join(path, "memory.max"))
		if err == nil && strings.TrimSpace(string(maxBytes)) != "max" {
			maximum, parseErr := strconv.ParseUint(strings.TrimSpace(string(maxBytes)), 10, 64)
			if parseErr != nil {
				return 0, false, fmt.Errorf("invalid cgroup memory.max: %w", parseErr)
			}
			currentBytes, readErr := os.ReadFile(filepath.Join(path, "memory.current"))
			if readErr != nil {
				return 0, false, fmt.Errorf("read cgroup memory.current: %w", readErr)
			}
			used, parseErr := strconv.ParseUint(strings.TrimSpace(string(currentBytes)), 10, 64)
			if parseErr != nil {
				return 0, false, fmt.Errorf("invalid cgroup memory.current: %w", parseErr)
			}
			remaining := uint64(0)
			if used < maximum {
				remaining = maximum - used
			}
			if !limited || remaining < available {
				available = remaining
			}
			limited = true
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, false, fmt.Errorf("read cgroup memory.max: %w", err)
		}
		if path == root {
			break
		}
		parent := filepath.Dir(path)
		if parent == path || !strings.HasPrefix(parent+string(filepath.Separator), root+string(filepath.Separator)) && parent != root {
			break
		}
		path = parent
	}
	return available, limited, nil
}
