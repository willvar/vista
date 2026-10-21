package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func TryLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func IsLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && info.Proc.P_stat != 5 // SZOMB
}
func Detach(cmd *exec.Cmd)         { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func AttachToParent(cmd *exec.Cmd) { Detach(cmd) }
func PrepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
func Start(cmd *exec.Cmd) error { return cmd.Start() }
func OpenBrowser(url string) error {
	cmd := exec.Command("/usr/bin/open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func Terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
func ShellCommand(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	PrepareCommand(cmd)
	return cmd
}
func KillProcessGroup(p *os.Process) error { return unix.Kill(-p.Pid, unix.SIGKILL) }
func ProcessSignal(state *os.ProcessState) string {
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return unix.SignalName(status.Signal())
		}
	}
	return ""
}

func procIdentity(pid int) (sessionID int, startTime uint64, err error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, err
	}
	sid, err := unix.Getsid(pid)
	start := info.Proc.P_starttime
	return sid, uint64(start.Sec)*1_000_000 + uint64(start.Usec), err
}
func WorkerSessionStart(pid int) (uint64, error) {
	sid, start, err := procIdentity(pid)
	if err != nil {
		return 0, err
	}
	if sid != pid {
		return 0, errors.New("Worker is not its session leader")
	}
	return start, nil
}
func KillOwnedSession(pid int, start uint64) error {
	if pid <= 1 || start == 0 {
		return errors.New("invalid Worker ownership record")
	}
	sid, current, err := procIdentity(pid)
	if err == nil && (sid != pid || current != start) {
		return ErrIdentityMismatch
	}
	if err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := KillSessionDescendants(pid); err != nil {
		return err
	}
	if sid, now, err := procIdentity(pid); err == nil && sid == pid && now == start {
		if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	return nil
}

// Session membership survives a shell's exit and reparenting. As on Linux,
// commands that explicitly create a different session leave this scope.
func KillSessionDescendants(sessionID int) error {
	if sessionID <= 1 {
		return errors.New("invalid Worker session ID")
	}
	var first error
	for pass := 0; pass < 3; pass++ {
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
		if err != nil {
			return err
		}
		found := false
		for _, info := range processes {
			pid := int(info.Proc.P_pid)
			if pid <= 1 || pid == sessionID || info.Proc.P_stat == 5 {
				continue
			}
			sid, start, err := procIdentity(pid)
			if err != nil || sid != sessionID {
				continue
			}
			found = true
			if sid, now, err := procIdentity(pid); err == nil && sid == sessionID && start == now {
				if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) && first == nil {
					first = err
				}
			}
		}
		if !found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return first
}

// These system utilities work without cgo, on both Intel and Apple Silicon.
func systemOutput(path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}
func RSS() uint64 {
	data, err := systemOutput("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid()))
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	current := kb * 1024
	recordPeakRSS(current)
	return current
}
func PeakRSS() uint64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err == nil && usage.Maxrss >= 0 {
		// Darwin reports ru_maxrss in bytes.
		recordPeakRSS(uint64(usage.Maxrss))
	}
	RSS()
	return observedPeakRSS.Load()
}
func AvailableMemory() (uint64, error) {
	data, err := systemOutput("/usr/bin/vm_stat")
	if err != nil {
		return 0, err
	}
	return parseVMStat(string(data))
}
func parseVMStat(data string) (uint64, error) {
	header, lines, ok := strings.Cut(data, "\n")
	if !ok {
		return 0, errors.New("invalid vm_stat output")
	}
	_, size, ok := strings.Cut(header, "page size of ")
	if !ok {
		return 0, errors.New("vm_stat page size unavailable")
	}
	value, _, _ := strings.Cut(size, " ")
	pageSize, err := strconv.ParseUint(value, 10, 64)
	if err != nil || pageSize == 0 {
		return 0, errors.New("invalid vm_stat page size")
	}
	var pages uint64
	var freeFound bool
	for _, line := range strings.Split(lines, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "Pages free" && name != "Pages inactive" && name != "Pages speculative") {
			continue
		}
		count, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid vm_stat %s: %w", name, err)
		}
		freeFound = freeFound || name == "Pages free"
		pages += count
	}
	if !freeFound {
		return 0, errors.New("vm_stat free pages unavailable")
	}
	return pages * pageSize, nil
}
