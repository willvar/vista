package platform

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TryLock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
func IsLockBusy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }

func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func Detach(cmd *exec.Cmd) {
	// Detaching the console alone still leaves the Server in an SSH/terminal
	// Job that may kill it when the launching session closes.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
}
func AttachToParent(cmd *exec.Cmd) { PrepareCommand(cmd) }
func PrepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
func OpenBrowser(url string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	PrepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func Terminate(p *os.Process) error { return KillProcessGroup(p) }
func ShellCommand(ctx context.Context, script string) *exec.Cmd {
	// PowerShell keeps Unicode text intact and supports multiline commands.
	// Windows PowerShell serializes uncaught errors as CLIXML when invoked
	// with EncodedCommand, even with OutputFormat=Text. Format caught errors
	// explicitly so artifacts and model tool results contain readable text.
	script = "[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); $OutputEncoding = [Console]::OutputEncoding; $ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; try {\n" + script + "\nif ($LASTEXITCODE) { exit $LASTEXITCODE }\n} catch { [Console]::Error.WriteLine(($_ | Out-String)); exit 1 }"
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[2*i:], unit)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-InputFormat", "Text", "-OutputFormat", "Text", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	PrepareCommand(cmd)
	cmd.Cancel = func() error { return KillProcessGroup(cmd.Process) }
	return cmd
}
func ProcessSignal(*os.ProcessState) string { return "" }

type processJob struct {
	process *os.Process
	handle  windows.Handle
}

var jobs = struct {
	sync.Mutex
	byPID map[int]processJob
}{byPID: make(map[int]processJob)}

var processScope struct {
	sync.Once
	handle windows.Handle // Kept until process exit; never inherited.
	err    error
}

func newProcessJob(flags uint32) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = flags
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func ownProcessTree() error {
	processScope.Do(func() {
		// Explicitly detached lifecycle commands must survive Server exit.
		// Managed children still inherit this scope at creation, and their
		// individual Jobs forbid breakaway before they are resumed.
		job, err := newProcessJob(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
		if err == nil {
			err = windows.AssignProcessToJobObject(job, windows.CurrentProcess())
			if err != nil {
				windows.CloseHandle(job)
			}
		}
		processScope.handle, processScope.err = job, err
	})
	return processScope.err
}

// Start contains the process before any of its code can run. In particular a
// fast shell cannot launch children and exit before assignment to its Job.
// The parent's noninheritable Job handle also kills the tree on parent death.
func Start(cmd *exec.Cmd) (err error) {
	// Children inherit this outer scope atomically at CreateProcess. It also
	// covers parent death between creating a suspended child and assigning its
	// individual Job, before any durable Worker ownership record exists.
	if err := ownProcessTree(); err != nil {
		return fmt.Errorf("create process supervision scope: %w", err)
	}
	job, err := newProcessJob(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		PrepareCommand(cmd)
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}
	registered := false
	defer func() {
		if err != nil {
			_ = cmd.Process.Kill()
			if registered {
				_ = KillProcessGroup(cmd.Process)
			} else {
				windows.CloseHandle(job)
			}
			_ = cmd.Wait()
		}
	}()
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	err = windows.AssignProcessToJobObject(job, h)
	windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("contain process: %w", err)
	}
	jobs.Lock()
	jobs.byPID[cmd.Process.Pid] = processJob{cmd.Process, job}
	registered = true
	jobs.Unlock()
	if err = resumeProcess(cmd.Process.Pid); err != nil {
		return err
	}
	return nil
}

func resumeProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(h)
		windows.CloseHandle(h)
		return err
	}
	return fmt.Errorf("find suspended thread for process %d: %w", pid, err)
}

func KillProcessGroup(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	jobs.Lock()
	defer jobs.Unlock()
	job, ok := jobs.byPID[p.Pid]
	if !ok || job.process != p {
		return p.Kill()
	}
	err := windows.TerminateJobObject(job.handle, 1)
	if err == nil {
		delete(jobs.byPID, p.Pid)
		_ = windows.CloseHandle(job.handle)
	}
	return err
}

func WorkerSessionStart(pid int) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	return processStart(h)
}
func processStart(h windows.Handle) (uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

func KillOwnedSession(pid int, start uint64) error {
	if pid <= 1 || start == 0 {
		return errors.New("invalid Worker ownership record")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // Job handles already closed when the previous Server exited.
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	current, err := processStart(h)
	if err != nil {
		return err
	}
	if current != start {
		return ErrIdentityMismatch
	}
	if err := KillSessionDescendants(pid); err != nil {
		return err
	}
	if state, err := windows.WaitForSingleObject(h, 0); err == nil && state == windows.WAIT_OBJECT_0 {
		return nil
	}
	err = windows.TerminateProcess(h, 1)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// TerminateProcess also reports ACCESS_DENIED for a process that has
		// already terminated, before its signaled state becomes observable.
		if state, waitErr := windows.WaitForSingleObject(h, 1000); waitErr == nil && state == windows.WAIT_OBJECT_0 {
			return nil
		}
	}
	return err
}

func KillSessionDescendants(pid int) error {
	if pid <= 1 {
		return errors.New("invalid Worker process ID")
	}
	jobs.Lock()
	job, ok := jobs.byPID[pid]
	jobs.Unlock()
	if ok {
		return KillProcessGroup(job.process)
	}
	// A Worker can also clean up its own children. Its parent retains the Job
	// handle, so forced death still cleans descendants that outlive their root.
	return killDescendants(pid)
}

func killDescendants(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var entries []windows.ProcessEntry32
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries = append(entries, entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	owned := map[uint32]bool{uint32(pid): true}
	var first error
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if owned[entry.ProcessID] || !owned[entry.ParentProcessID] {
				continue
			}
			owned[entry.ProcessID] = true
			changed = true
			h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, entry.ProcessID)
			if err == nil {
				err = windows.TerminateProcess(h, 1)
				windows.CloseHandle(h)
			}
			if err != nil && !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && first == nil {
				first = err
			}
		}
	}
	return first
}

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")
var getProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
var globalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")

func memoryInfo() (rss, peak uint64) {
	var counters struct {
		Size             uint32
		PageFaults       uint32
		PeakWorkingSet   uintptr
		WorkingSet       uintptr
		PeakPagedPool    uintptr
		PagedPool        uintptr
		PeakNonPagedPool uintptr
		NonPagedPool     uintptr
		Pagefile         uintptr
		PeakPagefile     uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	ok, _, _ := getProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if ok == 0 {
		return 0, 0
	}
	return uint64(counters.WorkingSet), uint64(counters.PeakWorkingSet)
}
func RSS() uint64 {
	current, peak := memoryInfo()
	recordPeakRSS(max(current, peak))
	return current
}
func PeakRSS() uint64 {
	RSS()
	return observedPeakRSS.Load()
}
func AvailableMemory() (uint64, error) {
	var status struct {
		Size              uint32
		Load              uint32
		TotalPhysical     uint64
		AvailablePhysical uint64
		TotalPagefile     uint64
		AvailablePagefile uint64
		TotalVirtual      uint64
		AvailableVirtual  uint64
		ExtendedVirtual   uint64
	}
	status.Size = uint32(unsafe.Sizeof(status))
	ok, _, err := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return 0, err
	}
	return status.AvailablePhysical, nil
}
